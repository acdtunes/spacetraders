package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// tourGateFees resolves the per-departure-gate fee table for this solve.
//
// Nil-reader and empty-table both yield nil, and nil means every crossing prices at the
// solver's flat charge — the pre-table behaviour. There is no error path on purpose: a
// pricing refinement must never be the reason a tour fails to plan.
func (h *RunTourCoordinatorHandler) tourGateFees(
	ctx context.Context, cmd *RunTourCoordinatorCommand,
) []routing.GateFee {
	if h.gateFees == nil {
		return nil
	}
	return gateFeeConstraints(h.gateFees.GateFees(ctx, cmd.PlayerID))
}

// tourPerHopToll resolves what one gate hop currently costs this fleet, in seconds, for the
// marginal term of the solver's crossing charge.
//
// 0 IS THE FAIL-OPEN VALUE and every path that is not a real measurement lands on it: an
// unwired estimator, too few measured hops, and — belt-and-braces on the one reading that
// must never reach the objective — a non-positive one, since a crossing that gave time back
// would make every distant candidate look free. 0 serializes to nothing, so the solver falls
// back to its env override and then to its fitted default.
func (h *RunTourCoordinatorHandler) tourPerHopToll(
	ctx context.Context, cmd *RunTourCoordinatorCommand,
) int {
	if h.jumpTolls == nil {
		return 0
	}
	seconds := h.jumpTolls.PerHopTollSeconds(ctx, cmd.PlayerID)
	if seconds <= 0 {
		return 0
	}
	return seconds
}

// tourAPISaturation resolves how hard the shared API request budget is binding for this
// plan. 0 IS THE FAIL-OPEN VALUE that every non-reading lands on — unwired estimator, thin
// window, real headroom, and a negative reading the objective must never see.
func (h *RunTourCoordinatorHandler) tourAPISaturation(ctx context.Context) int {
	if h.apiSaturation == nil {
		return 0
	}
	permille := h.apiSaturation.SaturationPermille(ctx)
	if permille <= 0 {
		return 0
	}
	return permille
}

// planForState assembles the market snapshot + era-scoped coordinates over allowedSystems
// and calls the depth-aware planner for the given ship state. It is the plan core shared
// by the live tour (planAndReserve — ship state + tour graph derived from the hull's real
// position) and the reposition pre-flight (planAtCandidate — a SYNTHETIC ship state
// positioned at a candidate system, over that candidate's tour graph, to price the tour
// the hull WOULD fly there without moving it first).
func (h *RunTourCoordinatorHandler) planForState(
	ctx context.Context,
	shipState routing.TourShipState,
	allowedSystems []string,
	cmd *RunTourCoordinatorCommand,
	budget tourPlanBudget,
) (*routing.TourPlan, []routing.TourGoodSnapshot, []routing.TourMarketAbsorption, error) {
	req, err := h.buildTourPlanRequest(ctx, shipState, allowedSystems, cmd, budget)
	if err != nil {
		return nil, nil, nil, err
	}
	absorptionView := h.assembleAbsorption(ctx, cmd.PlayerID, cmd.ContainerID)
	plan, err := h.solveTourPlan(ctx, req, absorptionView)
	if err != nil {
		return nil, nil, nil, err
	}
	return plan, req.snapshot, absorptionView, nil
}

// tourPlanRequest is everything the solver is asked for EXCEPT the netted absorption view:
// the tour graph, its market snapshot and coordinates, the deposit candidates, and the
// resolved constraints. None of it reads the absorption ledger, so it is assembled outside
// the planning gates — which is also what makes the gates knowable, since `systems` is the
// FINAL graph (gate neighbours plus any admitted far sinks) and therefore a superset of
// every system this plan could reserve a sink in.
type tourPlanRequest struct {
	shipState routing.TourShipState
	snapshot  []routing.TourGoodSnapshot
	waypoints []routing.TourWaypoint
	deposits  []routing.TourDepositCandidate
	cons      routing.TourConstraints
	systems   []string
}

// buildTourPlanRequest assembles the market snapshot + era-scoped coordinates over
// allowedSystems and resolves the solver constraints. The constraint carries the resolved
// model version so the solver fails closed on a mismatch rather than silently using a
// stale model.
func (h *RunTourCoordinatorHandler) buildTourPlanRequest(
	ctx context.Context,
	shipState routing.TourShipState,
	allowedSystems []string,
	cmd *RunTourCoordinatorCommand,
	budget tourPlanBudget,
) (*tourPlanRequest, error) {
	snapshot, waypoints, err := tradingsvc.BuildTourSnapshot(ctx, h.marketRepo, h.waypointRepo, allowedSystems, cmd.PlayerID, h.clock.Now(), h.rankerAgeCaps, h.stalenessDiscount)
	if err != nil {
		return nil, err
	}
	// Drop the effective blocklist from the good universe BEFORE any downstream consumer sees
	// it, so a blocklisted good is never chosen as tour cargo (buy source OR sell sink). No-op
	// when it's empty, so the default path is byte-identical. Tour cargo universe only —
	// refueling never reads this snapshot.
	snapshot = filterBlocklistedCargo(snapshot, h.effectiveCargoBlocklist(ctx, cmd.PlayerID))
	// Pull the richest sinks the gate-neighbour horizon HIDES back into the tour graph,
	// behind an explicit bound and only where reach and freshness both survive the haul
	// (admitFarSinks). Empty whenever the bound, the guards or the wiring say so, leaving
	// the graph exactly as the candidate walk produced it.
	far := h.admitFarSinks(ctx, cmd, allowedSystems, snapshot)
	allowedSystems = append(append([]string(nil), allowedSystems...), far.systems...)
	snapshot = append(snapshot, far.rows...)
	waypoints = append(waypoints, far.waypoints...)
	// Take the BUY side away from any market this hull sold into recently, so no plan can
	// re-source a good out of the price impact of its own dump. Applied to the FINAL graph so
	// an admitted far sink is covered too; a hull with no such history keeps the snapshot
	// exactly as built.
	snapshot = h.filterSameMarketRebuys(ctx, cmd, snapshot)
	// sp-mtvg: make the horizon's dropped exotic lanes LOUD. Read against the FINAL graph,
	// so what it counts is the value still out of reach after capture. Best-effort and
	// read-only — it never touches snapshot/plan and any error is swallowed (RULINGS #4).
	h.recordUnreachableLanes(ctx, allowedSystems, snapshot, cmd.PlayerID)
	// Assemble haul-to-storage deposit candidates for the planner to price against arb
	// sells. Empty when pre-positioning is off, no warehouse is in the tour graph, or
	// the capital ceiling is unreadable (fail closed) — the tour then plans pure arb,
	// unchanged.
	deposits := h.depositCandidates(ctx, cmd, allowedSystems, budget.reserve)
	// The solver's money guard is spend_cap = max(0, max_spend − working_capital_reserve)
	// (tour_solver.py, score_sequence) — a CASH contract: max_spend is the cash the
	// caller lets the tour touch, the reserve a keep-back. That pairing only holds on
	// the EXPLICIT --max-spend path. Under the DYNAMIC budget (cmd.MaxSpend == 0 → 25%
	// of live treasury, re-resolved per tour), maxSpend is already a spend BUDGET — the
	// capital guard is the 25% sizing plus the per-buy live-balance floor
	// (reserveHeadroom, proportional to live treasury) — so forwarding the ABSOLUTE
	// fleet reserve would subtract the guard a second time and zero the planner for any
	// treasury below 4×reserve (25%×T ≤ reserve). The dynamic path hands the planner a
	// reserve of 0; execution-time floors are untouched.
	// Resolved through the shared rule (plannerReserveFor), which is also what callers predict
	// the solver's spend_cap verdict with — so the reserve the request is BUILT with and the
	// reserve a caller PREDICTS with can never drift apart.
	plannerReserve := plannerReserveFor(cmd, budget.reserve)
	cons := routing.TourConstraints{
		MaxHops:          budget.maxHops,
		MinMarginPerUnit: cmd.MinMargin,
		// The solver re-filters the snapshot on this cap, so it must be a BACKSTOP, not a
		// second opinion: BuildTourSnapshot above already dropped each row against ITS OWN
		// activity's fitted cap, and a tighter flat value here would silently re-drop the
		// long-lived WEAK rows that pass deliberately kept.
		MaxSnapshotAgeMinutes: int(h.rankerAgeCaps.Widest().Minutes()),
		MaxSpend:              budget.maxSpend,
		WorkingCapitalReserve: plannerReserve,
		AllowedSystems:        allowedSystems,
		ExpectedModelVersion:  budget.modelVersion,
		// 0 (the daemon/CLI default) => the solver's MAX_TOUR_SYSTEMS default (2), so
		// the wire and plan are byte-identical to today; a positive knob raises the
		// per-tour distinct-system cap.
		MaxTourSystems: cmd.MaxTourSystems,
		// Closed-tour mode. false/"" (every current caller) => the solver plans an
		// OPEN tour byte-identical to today; true makes each planned tour end at the
		// anchor via an appended, honestly-priced no-trade return leg.
		Closed:       cmd.ClosedTours,
		AnchorSystem: cmd.AnchorSystem,
		// The gate-hop distance between every pair of allowedSystems, so the solver
		// prices a cross-system crossing by its REAL hop count instead of a flat 1 hop. Empty
		// at the default cap (every crossing is a single hop the flat charge prices exactly) =>
		// byte-identical to today; only a widened horizon (MaxTourSystems > 2) populates it.
		// A far sink's distances are merged in from the reachability check that ADMITTED it,
		// so the crossing is priced on exactly the hops the guard verified.
		InterSystemHops: mergeInterSystemHops(h.tourInterSystemHops(ctx, allowedSystems, cmd), far.hops),
		// Recovery-externality charge. 0 (the unarmed default) leaves
		// the solver's pairing order untouched; a positive weight makes a hull prefer the
		// sink the fleet is not still recovering at equal spread. PREFERENCE only — the
		// solver's min-margin gate keeps testing the raw margin (RULINGS #4: no guard is
		// tightened as a side effect).
		ExternalityWeight: cmd.ExternalityWeight,
		// The per-departure-gate fee table, learned from the ledger's own recorded
		// jumps, so a crossing's first hop is priced by the gate it leaves rather than by the
		// fleet mean. Nil reader (every existing test, and any daemon that has not wired it)
		// or an empty ledger => nil => every crossing prices at the flat charge =>
		// byte-identical to today.
		GateFees: h.tourGateFees(ctx, cmd),
		// The MARGINAL term of a crossing, measured from the hops the fleet has actually
		// flown. 0 (nil reader / too few hops) leaves the solver on its fitted default.
		InterSystemTravelPerHopSeconds: h.tourPerHopToll(ctx, cmd),
		// The second resource a tour spends. 0 (nil reader / headroom / a thin window)
		// leaves the solver ranking on credits per hour.
		APISaturationPermille: h.tourAPISaturation(ctx),
	}
	return &tourPlanRequest{
		shipState: shipState, snapshot: snapshot, waypoints: waypoints,
		deposits: deposits, cons: cons, systems: allowedSystems,
	}, nil
}

// solveTourPlan calls the depth-aware planner with the assembled request and the netted
// absorption the solver plans AROUND. absorptionView is empty when the ledger is unwired /
// the consult is killed / the read failed (fail-OPEN — the conditional Reserve is the hard
// backstop), which plans against full depth. Annotating the undiscounted per-tranche basis
// at this ONE seam is what stops a plan reaching a money guard without one (RULINGS #4).
func (h *RunTourCoordinatorHandler) solveTourPlan(
	ctx context.Context,
	req *tourPlanRequest,
	absorptionView []routing.TourMarketAbsorption,
) (*routing.TourPlan, error) {
	plan, err := h.planner.OptimizeTradeTour(ctx, req.snapshot, req.waypoints, req.shipState, req.cons, req.deposits, absorptionView)
	if err != nil {
		return nil, err
	}
	tradingsvc.AnnotateRawPlanBasis(plan, req.snapshot)
	return plan, nil
}

// filterBlocklistedCargo drops every snapshot row whose good is in the blocklist,
// removing the good from the tour's cargo universe entirely — as both a buy source (Ask)
// and a sell sink (Bid) — so the solver can never plan a buy or sell leg for it. An
// empty/nil blocklist is a true no-op: the SAME slice is returned (zero copy), keeping the
// default path byte-identical. Exact good-symbol match, mirroring the pre_positioning
// blocklist (good symbols are canonical uppercase).
func filterBlocklistedCargo(snapshot []routing.TourGoodSnapshot, block map[string]bool) []routing.TourGoodSnapshot {
	if len(block) == 0 {
		return snapshot
	}
	kept := make([]routing.TourGoodSnapshot, 0, len(snapshot))
	for _, row := range snapshot {
		if block[row.Good] {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

// recordUnreachableLanes is the sp-mtvg out-of-horizon lane diagnostic. Given the
// just-built in-scope snapshot, it finds each good the hull can SOURCE cheaply within the
// tour graph whose best sink (across ALL systems) lies OUTSIDE it — a profitable lane the
// 1-gate-hop horizon structurally hides from the solver (the source and its sink never
// co-occur in one snapshot, so no filter ever "rejects" the good; it simply never has a
// sell destination present). It counts every such lane on
// tour_candidates_dropped_total{reason=counterparty_system_unreachable} and names the
// richest few by spread in one log line, converting the silent leak into a legible signal
// so the class can never again be misdiagnosed as a price/volume filter.
//
// Read-only, best-effort, nil-safe: an unset scanner (tests / metrics-disabled), an empty
// snapshot, or any read error yields no diagnostic and never touches the plan (RULINGS #4).
// The guarded 1-hop horizon itself is unchanged — this only makes what it drops visible.
func (h *RunTourCoordinatorHandler) recordUnreachableLanes(
	ctx context.Context,
	allowedSystems []string,
	snapshot []routing.TourGoodSnapshot,
	playerID int,
) {
	if h.sinkScanner == nil || len(snapshot) == 0 {
		return
	}
	goods := inScopeSourcedGoods(snapshot)
	if len(goods) == 0 {
		return
	}
	sinks, err := h.sinkScanner.BestSinksAcrossSystems(ctx, goods, playerID, h.listingMaxAge(ctx, playerID), h.clock.Now())
	if err != nil || len(sinks) == 0 {
		return
	}
	dropped := computeUnreachableLanes(allowedSystems, snapshot, sinks)
	if len(dropped) == 0 {
		return
	}
	metrics.RecordTourCandidateDropped(playerID, unreachableLaneReason, len(dropped))
	// Name the richest lanes by spread (bounded) so the counter's rate carries exemplars.
	top := dropped
	if len(top) > unreachableLaneLogTopN {
		top = top[:unreachableLaneLogTopN]
	}
	parts := make([]string, 0, len(top))
	for _, l := range top {
		parts = append(parts, fmt.Sprintf("%s %s(%d)->%s@%s(%d) spread %d/u",
			l.Good, l.SourceWaypoint, l.Ask, l.SinkWaypoint, l.SinkSystem, l.Bid, l.Spread))
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Tour horizon dropped %d profitable lane(s) whose best sink is beyond the gate-neighbor graph (sp-mtvg): %s",
		len(dropped), strings.Join(parts, "; ")),
		map[string]interface{}{
			"action":          "tour_candidates_dropped",
			"reason":          unreachableLaneReason,
			"count":           len(dropped),
			"allowed_systems": strings.Join(allowedSystems, ","),
		})
}

// unreachableLane is one profitable lane the tour horizon hides: a good sourceable in the
// tour graph whose best sink sits in an out-of-graph system.
type unreachableLane struct {
	Good           string
	SourceWaypoint string
	SinkWaypoint   string
	SinkSystem     string
	Ask            int
	Bid            int
	Spread         int
}

// inScopeSourcedGoods returns the goods with a positive in-scope BUY quote (Ask>0) in the
// snapshot — the goods the hull can actually source within the tour graph, the only ones
// whose out-of-graph sinks are a genuine missed lane rather than noise.
func inScopeSourcedGoods(snapshot []routing.TourGoodSnapshot) []string {
	seen := map[string]bool{}
	var goods []string
	for _, r := range snapshot {
		if r.Ask > 0 && !seen[r.Good] {
			seen[r.Good] = true
			goods = append(goods, r.Good)
		}
	}
	return goods
}

// computeUnreachableLanes is the pure detection core of the out-of-horizon diagnostic.
// For each good with a cheap in-scope source (min Ask>0 in the snapshot), it flags the good when
// its best sink (from `sinks`, the global cross-system scan) lies OUTSIDE allowedSystems
// and clears the materiality floor. Returned richest-spread-first. Pure — no clock, no
// metrics, no IO — so the flagging rules are unit-tested directly.
func computeUnreachableLanes(
	allowedSystems []string,
	snapshot []routing.TourGoodSnapshot,
	sinks map[string]market.GlobalSinkResult,
) []unreachableLane {
	cheapestAsk := map[string]int{}
	sourceWp := map[string]string{}
	for _, r := range snapshot {
		if r.Ask <= 0 {
			continue
		}
		if cur, ok := cheapestAsk[r.Good]; !ok || r.Ask < cur {
			cheapestAsk[r.Good] = r.Ask
			sourceWp[r.Good] = r.Waypoint
		}
	}
	allowed := map[string]bool{}
	for _, s := range allowedSystems {
		allowed[s] = true
	}
	var dropped []unreachableLane
	for good, ask := range cheapestAsk {
		sink, ok := sinks[good]
		if !ok || allowed[sink.SystemSymbol] {
			continue // no known sink, or the sink is already reachable in the tour graph
		}
		spread := sink.Bid - ask
		if spread < unreachableLaneMinSpreadPerUnit {
			continue
		}
		dropped = append(dropped, unreachableLane{
			Good: good, SourceWaypoint: sourceWp[good], SinkWaypoint: sink.WaypointSymbol,
			SinkSystem: sink.SystemSymbol, Ask: ask, Bid: sink.Bid, Spread: spread,
		})
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Spread > dropped[j].Spread })
	return dropped
}

// tourSystems is the default tour graph: the hull's current system plus every system
// one gate hop away with fresh market data (the planner scopes each tour to
// maxTourSystems within this allowed set). Neighbor discovery fails open to home-only.
// Threads cmd so the candidate set can be widened past 1 gate hop once the solver
// clamp is lifted (arming-gated in tourSystemsFrom); byte-identical at the defaults.
func (h *RunTourCoordinatorHandler) tourSystems(ctx context.Context, ship *navigation.Ship, cmd *RunTourCoordinatorCommand) []string {
	return h.tourSystemsFrom(ctx, ship.CurrentLocation().SystemSymbol, cmd)
}

// tourSystemsFrom is tourSystems generalized to an arbitrary home system. The live tour
// centers it on the hull's current system; the reposition pre-flight centers it on a
// candidate system to build that candidate's tour graph.
//
// At the default (effectiveCandidateHopDepth <= 1) it returns the 1-hop set
// (oneHopTourSystems). Only when the arming gate opens (a configured depth > 1 AND the
// solver clamp lifted) does it widen, and the widened set is floored to the 1-hop set so
// it can never go narrower.
func (h *RunTourCoordinatorHandler) tourSystemsFrom(ctx context.Context, home string, cmd *RunTourCoordinatorCommand) []string {
	if cmd.MVTLoop {
		return []string{h.mvtReconcileScope(ctx, cmd, home)}
	}
	oneHop := h.oneHopTourSystems(ctx, home, cmd)
	if h.effectiveCandidateHopDepth(cmd) <= 1 {
		return oneHop // DEFAULT PATH — the 1-hop set
	}
	return h.widenedTourSystems(ctx, home, cmd, oneHop)
}

// oneHopTourSystems is home + every 1-gate-hop neighbor with fresh data, deduped, fail-open
// to home-only. It is the default result AND the floor the widened branch never goes below.
func (h *RunTourCoordinatorHandler) oneHopTourSystems(ctx context.Context, home string, cmd *RunTourCoordinatorCommand) []string {
	systems := []string{home}
	seen := map[string]bool{home: true}
	for _, n := range h.oneHopNeighbors(ctx, home, cmd) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		systems = append(systems, n)
	}
	return systems
}

// oneHopNeighbors resolves home's gated neighbors, DURABLE-FIRST once armed: gate topology is
// static within an era, so the live per-plan read buys an answer already on disk.
func (h *RunTourCoordinatorHandler) oneHopNeighbors(ctx context.Context, home string, cmd *RunTourCoordinatorCommand) []string {
	if !cmd.TourNeighborsDurableFirst || h.legs.gateGraph == nil {
		return h.legs.neighborSystems(ctx, home, cmd.PlayerID)
	}
	// A durable read that yields nothing falls back to the live query rather than collapse
	// the tour graph to home-only, silently shrinking a hull's trading reach.
	if durable := h.legs.gatedNeighborSystems(ctx, home, cmd.PlayerID); len(durable) > 0 {
		return durable
	}
	return h.legs.neighborSystems(ctx, home, cmd.PlayerID)
}

func (h *RunTourCoordinatorHandler) tourShipState(ship *navigation.Ship) routing.TourShipState {
	cargo := map[string]int{}
	if c := ship.Cargo(); c != nil {
		for _, item := range c.Inventory {
			// Never offer reserved cargo (staged outfitting modules, or an
			// operator-protected good) to the planner as sellable/liquidatable
			// inventory — the tour must not PLAN to sell what the executor will
			// refuse to sell, and its projected profit must not book phantom
			// module-liquidation revenue. Non-reserved held cargo is still carried
			// forward and liquidated as launch inventory.
			if ship.IsCargoReserved(item.Symbol) {
				continue
			}
			cargo[item.Symbol] = item.Units
		}
	}
	fuelCurrent, fuelCapacity := 0, ship.FuelCapacity()
	if f := ship.Fuel(); f != nil {
		fuelCurrent, fuelCapacity = f.Current, f.Capacity
	}
	return routing.TourShipState{
		ShipSymbol:      ship.ShipSymbol(),
		CurrentWaypoint: ship.CurrentLocation().Symbol,
		CurrentSystem:   ship.CurrentLocation().SystemSymbol,
		HoldCapacity:    ship.CargoCapacity(),
		FuelCurrent:     fuelCurrent,
		FuelCapacity:    fuelCapacity,
		EngineSpeed:     ship.EngineSpeed(),
		Cargo:           cargo,
	}
}

// readTourModelVersion reads "<fit_version>@<era>" from the checked-in artifact so the
// constraint binds the planner to the exact fitted model (spec: mismatch → the solver
// fails closed). Any read/parse failure surfaces as an error the caller fails open on.
func readTourModelVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read model artifact: %w", err)
	}
	var art struct {
		FitVersion int    `json:"fit_version"`
		Era        string `json:"era"`
	}
	if err := json.Unmarshal(data, &art); err != nil {
		return "", fmt.Errorf("parse model artifact: %w", err)
	}
	if art.Era == "" {
		return "", fmt.Errorf("model artifact missing era")
	}
	return fmt.Sprintf("%d@%s", art.FitVersion, art.Era), nil
}
