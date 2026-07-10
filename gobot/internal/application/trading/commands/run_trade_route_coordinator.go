package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// defaultMaxVisits is the RUN's total visit budget when the command does not set
// one (sp-1hj5: one run OWNS its --max-visits). The budget spans the whole run —
// every circuit the outer loop commits to draws from the same allowance, so
// "--max-visits 12" means twelve buy+sell visits in total, not twelve per lane —
// and the run may not end while budget remains unless a margin/starvation/error
// exit fires. It also keeps a lane whose bid never decays (or a mispriced fake)
// from spinning forever; the bid-floor discipline is the real per-lane stop.
const defaultMaxVisits = 50

// Finish-current-leg bounds (sp-1hj5, sp-7yej invariant 1): once a leg's buy has
// filled the hold, the run must not end without a real attempt to complete the
// leg's sell. liquidateHeld retries the deliver+sell half until the hold is
// empty or liquidationMaxFailures CONSECUTIVE attempts fail (any sold tranche
// resets the count — partial progress is progress), sleeping
// liquidationRetryBackoff between failed attempts so a transient cause (the
// incident's instant jump failure, a nav-cache race, an importer volume tick)
// has time to clear. Bounded so a genuinely-unsellable load still exits — as an
// HONEST failure (CargoStranded → success=false), never a laden success=true.
const (
	liquidationMaxFailures  = 3
	liquidationRetryBackoff = 20 * time.Second
)

// tradeRouteDockRetryLimit bounds how many times h.dock resyncs the ship from the
// API and re-issues the dock while waiting for the nav-cache race to clear (the
// arrival event flipping a stale IN_TRANSIT to IN_ORBIT). Bounded so a genuinely
// undockable ship can never spin forever — it aborts the circuit cleanly with the
// verbatim cause instead (sp-ynuf, mirroring the goods-factory dock race sp-n7yp).
const tradeRouteDockRetryLimit = 3

// defaultMaxCircuits bounds the OUTER loop (sp-wlev scope amendment): how many
// distinct lanes a single run will commit to before stopping regardless of
// margin. The bid-floor discipline and starvation detection are the real stops;
// this is only a safety rail against a persistently-wrong ranking cycling
// forever.
const defaultMaxCircuits = 20

// noProgressStarvationLimit bounds how many consecutive circuits may commit to
// a lane and fly zero visits before the run calls it starvation and stops. One
// zero-visit circuit can be a transient live-recheck miss; several in a row
// means the system has nothing left worth absorbing a hull into.
const noProgressStarvationLimit = 3

// defaultWorkingCapitalReserve is the fallback hard spend floor (sp-bp6f): a
// circuit must not execute a buy that would drop LIVE treasury below this
// line. Sized to the exact level the 2026-07-09 incident called "danger" —
// treasury bottomed at 43,041 and briefly went negative (-30,537) before the
// captain intervened — so a circuit now stops BEFORE crossing back into that
// zone instead of the failure being discovered after the fact. Overridable
// per-run via RunTradeRouteCoordinatorCommand.WorkingCapitalReserve (0 → this
// default) and via the daemon's "working_capital_reserve" launch-config key
// (mirroring max_visits' own "0 → coordinator default" convention), so the
// captain can raise or lower the floor operationally — e.g. to the current
// contract+factory working-capital need the incident notes called out —
// without a redeploy.
const defaultWorkingCapitalReserve = 50000

// negativeMarginAbortVisits bounds how many visits a circuit's OWN realized
// margin (this circuit's sell revenue minus its buy cost, reset to zero every
// fresh lane commitment) may run negative before the circuit aborts (sp-bp6f
// fix #2). This is the exact incident shape: repeated tranches walk the
// source ask up while the destination bid gets crushed, so the STALE ranked
// spread still looks positive long after the circuit's actual fills have
// turned loss-making. Small enough to catch the pattern within a circuit's
// early visits before it compounds; not so small that a single noisy fill
// (e.g. one visit's fees) trips it.
const negativeMarginAbortVisits = 3

// exitReason* enumerates why the outer circuit loop stopped (sp-wlev: circuits
// must loop until a margin-exit or starvation-exit, never one-and-done — a
// hull that flies one lane and idles wastes duty cycle, the 20x gap this
// feature targets).
const (
	// exitReasonMarginExhausted: a fresh re-scan found nothing that clears the
	// discipline floor — every lane in the system (and its jump-gate neighbors)
	// is currently sub-floor.
	exitReasonMarginExhausted = "margin_exhausted"
	// exitReasonStarvation: repeated re-scans kept committing to a lane that flew
	// zero visits — the system has stopped absorbing new circuits.
	exitReasonStarvation = "starvation"
	// exitReasonMaxCircuits: the outer safety bound (defaultMaxCircuits) tripped
	// while the run was still productive.
	exitReasonMaxCircuits = "max_circuits"
	// exitReasonError: a navigate/dock/buy/sell leg failed mid-circuit; see
	// AbortReason for the verbatim cause.
	exitReasonError = "error"
	// exitReasonStaleAsk: the live source ask moved beyond tolerance of the
	// ranked basis just before the first buy; see RankedSourceAsk/LiveSourceAsk.
	exitReasonStaleAsk = "stale_ask"
	// exitReasonNoLanes: the market cache had nothing to rank at all.
	exitReasonNoLanes = "no_lanes"
	// exitReasonSpendFloor: a circuit was about to buy a tranche that would drop
	// live treasury below the working-capital reserve floor (sp-bp6f); see
	// SpendFloorAbort/TreasuryAtAbort/ReserveFloor.
	exitReasonSpendFloor = "spend_floor"
	// exitReasonNegativeMargin: a circuit's own realized margin ran negative for
	// negativeMarginAbortVisits consecutive visits - the lane is losing money on
	// its actual recent fills, not just a stale ranked spread (sp-bp6f); see
	// NegativeMarginAbort/RealizedCircuitMargin.
	exitReasonNegativeMargin = "negative_margin"
	// exitReasonCargoBlocked: the hull had no free cargo hold to buy a tranche
	// (sp-xwa1) — a non-empty hull can't trade at all, so it parks pre-flight with
	// a structured reason instead of burning starvation circuits or buying a
	// sliver mid-buy; see CargoBlocked/CargoBlockReason.
	exitReasonCargoBlocked = "cargo_blocked"
	// exitReasonMaxVisits: the RUN consumed its whole visit budget
	// (cmd.MaxVisits, default defaultMaxVisits) — a clean stop at a leg
	// boundary with the hold empty (sp-1hj5: one run owns its --max-visits).
	exitReasonMaxVisits = "max_visits"
	// exitReasonCargoStranded: the run ended still holding cargo bought this
	// run after the bounded finish-current-leg liquidation failed to sell it
	// (sp-1hj5). This exit is a container FAILURE — the response's
	// CompletionOutcome vetoes the runner's success=true (sp-7yej invariant 2);
	// see CargoStranded/CargoStrandedUnits/CargoStrandedReason.
	exitReasonCargoStranded = "cargo_stranded"
	// exitReasonUnroutable: an operator-directed (--dest) cross-system lane was
	// selected but its sell system is not reachable over the gate graph (sp-7gr2).
	// The run stops CLEANLY and EMPTY before the circuit's first buy — refusing to
	// buy a tranche it cannot deliver — rather than crashing laden at the gate the
	// way the arb-run incident did; see RoutabilityAbort.
	exitReasonUnroutable = "unroutable"
)

// minFreeCargoForCircuit is the smallest free hold (in units) a hull must have
// before a circuit will fly it (sp-xwa1). Below this the hull cannot buy even a
// one-unit tranche, so flying it wastes a cross-system round trip on nothing — the
// exact non-empty-hull starvation the pre-flight cargo check exists to catch. Held
// at 1 (i.e. "any free hold at all"): sub-viable slivers above zero are left to the
// tranche sizing (trading.VisitTranche) rather than an arbitrary larger threshold.
const minFreeCargoForCircuit = 1

// MarketRefresher live-refreshes one waypoint's market from the API into the cache.
// The coordinator uses it once, before the first buy, to re-read the source ask live
// and abort if it has run away from the stale basis the lane was ranked on (hazard b,
// sp-2sam). Kept as a narrow port (not an import of the ship package) to avoid a cycle
// — the CLI composition root injects the concrete MarketScanner. A nil refresher
// disables the guard, so callers that cannot scan (e.g. tests) simply skip it.
type MarketRefresher interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

// RunTradeRouteCoordinatorCommand asks the coordinator to fly one idle hull
// through the top-ranked arbitrage circuit in a system until the margin dies.
type RunTradeRouteCoordinatorCommand struct {
	ShipSymbol   string
	SystemSymbol string
	PlayerID     int
	ContainerID  string
	// MaxVisits is the RUN's total visit budget (0 → defaultMaxVisits): the
	// whole run — across every circuit the outer loop commits to — flies at
	// most this many buy+sell visits, and may not stop early while budget
	// remains unless a margin/starvation/error exit fires (sp-1hj5: one run
	// owns its --max-visits; it is not a per-lane bound).
	MaxVisits int
	// WorkingCapitalReserve is the hard spend floor (sp-bp6f): 0 → defaultWorkingCapitalReserve.
	WorkingCapitalReserve int
	// TargetDest is the operator-directed lane override (sp-xwa1, the CLI's
	// --dest flag): a destination waypoint (e.g. "X1-ABC-D1") or bare system
	// symbol (e.g. "X1-ABC") the coordinator must target instead of letting
	// the ranker auto-pick. Empty → undirected auto-scan, unchanged. See
	// selectLane/laneMatchesTarget in run_trade_route_coordinator_travel.go.
	TargetDest string
}

// RunTradeRouteCoordinatorResponse reports the realised circuit economics. Net
// profit is revenue − acquisition cost; fuel is a live cost outside this ledger.
type RunTradeRouteCoordinatorResponse struct {
	ShipSymbol     string
	Good           string
	SourceWaypoint string
	DestWaypoint   string
	Visits         int
	UnitsTraded    int
	TotalCost      int
	TotalRevenue   int
	NetProfit      int
	Completed      bool
	Error          string
	// NoDisciplinedLane is set when profitable lanes were ranked but NONE cleared the
	// bid-floor discipline (trading.MinBidMargin), so the circuit flew nothing by
	// design. It distinguishes a disciplined "nothing worth flying" from "no lane at
	// all" — both leave Good=="" and Visits==0 — so the caller reports the reason
	// instead of a silent zero-visit success (sp-sh6w).
	NoDisciplinedLane bool
	// BestSubFloorSpread is the highest per-unit spread among the ranked lanes when
	// NoDisciplinedLane is set: how close the best standing lane came to the floor.
	BestSubFloorSpread int
	// StaleAskAbort is set when a live re-read of the source ask (taken at the source
	// before the first buy) had moved beyond trading.StaleAskMovePercent from the basis
	// the lane was ranked on. The lane's ranked spread was stale, so the run aborted
	// before buying rather than risk a bad fill (sp-2sam hazard b, a -196k precedent).
	// A selected lane that aborts this way is NOT a silent zero — it reports WHY.
	StaleAskAbort bool
	// RankedSourceAsk and LiveSourceAsk are the basis the lane was ranked on and the
	// live ask read at the source, populated when StaleAskAbort is set.
	RankedSourceAsk int
	LiveSourceAsk   int
	// SpendFloorAbort is set when a circuit was about to buy a tranche that would
	// drop live treasury below the working-capital reserve floor (sp-bp6f) — the
	// buy was skipped and the circuit stopped BEFORE spending past the line,
	// rather than the breach being discovered after the fact. TreasuryAtAbort and
	// ReserveFloor are the live credits observed and the reserve in effect; a
	// fail-closed abort (the live treasury read itself failed) leaves
	// TreasuryAtAbort at zero since no live figure was actually obtained.
	SpendFloorAbort bool
	TreasuryAtAbort int
	ReserveFloor    int
	// NegativeMarginAbort is set when THIS circuit's own realized margin (its
	// sell revenue minus its buy cost, not the cross-run cumulative totals) ran
	// negative for negativeMarginAbortVisits consecutive visits — the lane is
	// actively losing money on its recent fills (sp-bp6f fix #2).
	// RealizedCircuitMargin is that circuit-local net margin when it was set.
	NegativeMarginAbort   bool
	RealizedCircuitMargin int
	// CargoBlocked is set when the hull had no free cargo hold to buy a tranche
	// (sp-xwa1) — either detected pre-flight (a non-empty hull the run never should
	// have flown) or before a buy leg once accumulated cargo fills the hold. The hull
	// parks rather than failing mid-buy or buying a useless sliver. CargoBlockReason
	// is the operator-facing prose (good/needed/free); the structured park is also
	// logged in the message text so `container logs` shows WHY (renderer drops metadata).
	CargoBlocked     bool
	CargoBlockReason string
	// AbortReason explains why a SELECTED lane (Good set) flew fewer visits than the
	// margin would allow — a navigate/dock/buy/sell leg failed mid-circuit. It exists
	// because three successive zero-visit bugs (r3cl, sh6w, sp-2sam) each needed a live
	// re-run to discover WHY the loop stopped: the failing leg's reason was logged but
	// never surfaced to the caller, so the printed result was a bare 'Visits: 0'. With
	// this, the next occurrence is self-diagnosing. Empty on a clean margin-death stop.
	AbortReason string
	// ExitReason explains why the OUTER circuit loop stopped (sp-wlev scope
	// amendment: circuits loop until a margin-exit or starvation-exit, never
	// one-and-done). See the exitReason* constants for the full set.
	ExitReason string
	// RoutabilityAbort is set when an operator-directed (--dest) cross-system lane
	// was selected but its sell system is unreachable over the gate graph (sp-7gr2),
	// so the run stopped EMPTY before the first buy rather than buying a tranche it
	// could not deliver. AbortReason carries the operator-facing prose naming both
	// systems; ExitReason is exitReasonUnroutable.
	RoutabilityAbort bool
	// Circuits counts how many distinct lanes the outer loop committed to and
	// attempted this run, regardless of whether each one was productive.
	Circuits int
	// CargoStranded is set when the run ended still holding cargo it bought
	// this run, after the bounded finish-current-leg liquidation could not
	// sell it down (sp-1hj5). A stranded run is a container FAILURE: the
	// runner reads it through CompletionOutcome (sp-7yej invariant 2) and
	// refuses success=true — the exact laden success=true that released
	// TORWIND-19 holding 18 lab_instruments. CargoStrandedUnits is how many
	// units remain aboard; CargoStrandedReason is the failure signature
	// (embedding the leg failure's verbatim cause) the runner reports.
	CargoStranded       bool
	CargoStrandedUnits  int
	CargoStrandedReason string
}

// CompletionOutcome implements common.CompletionReporter (sp-7yej invariant 2,
// honest completion): the container runner refuses success=true when the run
// ended holding cargo bought this run. Deliberately threaded via the response
// (not a non-nil Go error, arb-run's sp-5nqx shape) so the runner's restart
// loop never re-runs the coordinator against a stranded hold — a re-run cannot
// resume the dynamically-ranked lane and would trade AROUND the stranded cargo.
func (r *RunTradeRouteCoordinatorResponse) CompletionOutcome() (bool, string) {
	if r.CargoStranded {
		return false, r.CargoStrandedReason
	}
	return true, ""
}

// Compile-time pin: the trade-route response participates in the runner's
// honest-completion contract.
var _ common.CompletionReporter = (*RunTradeRouteCoordinatorResponse)(nil)

// RunTradeRouteCoordinatorHandler runs a pure-arbitrage circuit on a single hull:
// it loads the named ship (already claimed for it by the daemon container runner via
// the ship_symbol metadata, so release-on-death is the runner's job — sp-zewt), ranks
// lanes from cache (trading.RankSpreads), selects the deepest lane that clears the
// bid-floor discipline (trading.FirstDisciplinedLane — so it never picks a top-capped
// lane the executor would refuse), then flies it in disciplined tranches — ≤18u/visit,
// and only while the destination bid clears basis+1000 (trading.MarginAlive) — looping
// until the margin dies.
//
// It reuses the same driven ports as the fabrication coordinators (mediator for
// navigate/dock/purchase/sell, ship + market repositories), so ship movement and
// trades go through the exact command handlers the daemon uses — in the daemon that
// means NavigateRouteCommand resolves to the RouteExecutor-backed handler (orbit →
// refuel → NavigateDirect → arrival events), which is why running as a container
// subsumes the CLI runner's hand-rolled in-process nav and its patches (sp-2sam
// self-collision, sp-sj7p orbit-before-nav): the container never spawns a re-claiming
// child navigate.
type RunTradeRouteCoordinatorHandler struct {
	mediator        common.Mediator
	shipRepo        navigation.ShipRepository
	marketRepo      market.MarketRepository
	marketRefresher MarketRefresher // optional; nil disables the live stale-ask guard
	clock           shared.Clock    // used only for the cross-system jump-cooldown wait (sp-wlev)
	// apiClient is used only to live-read treasury for the working-capital spend
	// floor (sp-bp6f). Optional; nil disables the guard entirely (fails OPEN,
	// mirroring marketRefresher's own optional-port contract) rather than
	// defaulting to a real client the way clock defaults to RealClock — a caller
	// that cannot supply one (e.g. most tests) simply runs without the guard.
	apiClient domainPorts.APIClient
	// gateGraph resolves multi-jump routes over the persisted cross-system gate
	// adjacency (sp-7gr2). Optional; nil keeps travel()'s legacy single-jump
	// assumption (a direct origin→dest edge) so every existing caller/test is
	// unaffected. The daemon injects a real, fetch-through GateGraph via
	// SetGateGraph so a multi-hop gap (KA42→PA3→UQ16→JP61) is actually crossed.
	gateGraph GateGraph
}

// GateGraph resolves multi-jump routes over the persisted cross-system gate
// adjacency (sp-7gr2). travel() walks Path hop-by-hop (each hop a single
// directly-connected jump); the composing arb coordinator's pre-buy guard uses
// Routable to refuse a cross-system buy whose sell leg is unreachable BEFORE
// spending (the incident bought first, then crashed laden at the home gate with
// no route to JP61). Path returns the ordered system hop path inclusive of both
// ends, or a gategraph.ErrUnroutable-wrapped error naming both systems.
type GateGraph interface {
	Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error)
	Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error)
}

// NewRunTradeRouteCoordinatorHandler wires the coordinator. It does not own a
// container repository: the daemon container runner claims and releases the
// hull through the normal container lifecycle (ship_symbol metadata → createShipAssignments
// on start, releaseShipAssignments on every terminal path), so this handler only reads
// ship/market state and flies the circuit. A nil marketRefresher disables the live
// stale-ask guard (the circuit still runs on the ranked basis); the daemon injects a
// real MarketScanner so the guard is active. A nil clock defaults to shared.RealClock;
// tests inject a shared.MockClock so the cross-system jump-cooldown wait (sp-wlev) is
// instant instead of a real sleep. A nil apiClient disables the working-capital
// spend-floor guard (sp-bp6f) — the circuit runs without live-checking treasury
// before each buy; the daemon injects the real APIClient so the guard is active in
// production, the same fail-open contract marketRefresher already uses.
func NewRunTradeRouteCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *RunTradeRouteCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunTradeRouteCoordinatorHandler{
		mediator:        mediator,
		shipRepo:        shipRepo,
		marketRepo:      marketRepo,
		marketRefresher: marketRefresher,
		clock:           clock,
		apiClient:       apiClient,
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver (sp-7gr2). The daemon
// injects a persisted, fetch-through GateGraph so travel() can cross a multi-hop
// gap and the composing arb coordinator can pre-check routability. Left unset
// (nil), travel() keeps the legacy single-jump behavior (one direct origin→dest
// edge), so no existing caller or test changes. Mirrors the SetSpendLedger
// optional-injection idiom rather than churning the constructor signature.
func (h *RunTradeRouteCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// gateGraphResolver exposes the wired resolver (or nil) so the composing arb
// coordinator runs its pre-buy routability guard through the SAME instance
// travel() uses — one graph, one cache, one source of truth.
func (h *RunTradeRouteCoordinatorHandler) gateGraphResolver() GateGraph {
	return h.gateGraph
}

// Handle executes the trade-route command.
func (h *RunTradeRouteCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunTradeRouteCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunTradeRouteCoordinatorResponse{ShipSymbol: cmd.ShipSymbol}

	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}

	response.Completed = true
	return response, nil
}

func (h *RunTradeRouteCoordinatorHandler) execute(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	response *RunTradeRouteCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID

	containerID := cmd.ContainerID
	if containerID == "" {
		containerID = utils.GenerateContainerID("trade-route", cmd.ShipSymbol)
	}
	// Link buy/sell ledger transactions to this trade-route operation.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(containerID, "trade_route"))

	// Step 1: load the hull the container runner already claimed for this circuit. The
	// idle-gap discipline (only take a genuinely idle hull, never steal one mid-task)
	// is enforced at the container-start boundary (DaemonServer.StartTradeRoute checks
	// IsIdle before persisting the container); by the time this runs the runner has
	// assigned the hull to this container via the ship_symbol metadata, and it will be
	// force-released on every terminal path (completion, crash, cancel) — so this
	// handler neither claims nor releases (sp-zewt retires the vjwb orphan-on-death).
	ship, err := h.loadShip(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return err
	}

	// Step 2+3: loop the circuit — sp-wlev scope amendment. A hull that flies one
	// lane to its bid-floor and then idles wastes duty cycle (the 20x gap this
	// feature targets is DUTY CYCLE, not per-trade economics), so re-rank lanes
	// from fresh cache after every circuit and keep committing to whichever lane
	// still clears the discipline floor until margins collapse everywhere, the
	// system stops absorbing new circuits (starvation), a leg errors, a stale ask
	// aborts, or the safety bound trips.
	noProgressStreak := 0
	exitReason := ""

	// The RUN owns its visit budget (sp-1hj5): resolved once here, drawn down by
	// every circuit's visits. The old per-circuit reading of MaxVisits let a
	// 12-visit grant fly 12 visits per LANE (unbounded across re-ranks) while any
	// mid-leg abort ended the run after a fraction of the grant.
	runMaxVisits := cmd.MaxVisits
	if runMaxVisits <= 0 {
		runMaxVisits = defaultMaxVisits
	}

	for circuitNum := 0; circuitNum < defaultMaxCircuits; circuitNum++ {
		// Budget check at the leg boundary: a run whose visits consumed the
		// grant stops CLEANLY and EMPTY here — never mid-leg (sp-7yej
		// invariant 1: safe-exit points only).
		if response.Visits >= runMaxVisits {
			exitReason = exitReasonMaxVisits
			logger.Log("INFO", fmt.Sprintf("Run visit budget consumed (%d/%d) - ending run at the leg boundary", response.Visits, runMaxVisits), map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"visits":      response.Visits,
				"max_visits":  runMaxVisits,
			})
			break
		}

		lanes, err := h.scanLanes(ctx, cmd.SystemSymbol, playerID, ship.CargoCapacity(), cmd.TargetDest)
		if err != nil {
			return fmt.Errorf("failed to scan arbitrage lanes: %w", err)
		}
		if len(lanes) == 0 {
			exitReason = exitReasonNoLanes
			logger.Log("INFO", "No profitable arbitrage lane in cache - releasing ship", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"system":      cmd.SystemSymbol,
			})
			break
		}

		// The scan ranks lanes by volume-capped spread and deliberately keeps sub-floor
		// lanes visible (it is an observation tool). The executor, however, refuses any
		// lane whose per-unit spread is below MinBidMargin (runCircuit's MarginAlive gate)
		// — so the top capped-spread lane can be one that flies ZERO visits. Select the
		// DEEPEST lane that actually clears the discipline floor, so a selected lane always
		// flies >=1 visit instead of a silent zero-visit run (sp-sh6w). When cmd.TargetDest
		// is set (sp-xwa1's --dest override), selectLane pins to that directed lane instead
		// of the ranker's top pick; see selectLane's doc for the full contract.
		lane, ok := selectLane(lanes, cmd.TargetDest)
		if !ok {
			exitReason = exitReasonMarginExhausted
			// Only report "nothing to fly" if this run never flew anything at all — a
			// later re-scan finding nothing (after earlier circuits DID trade) is a clean
			// margin-exhausted stop, not the "no lane at all" case (sp-wlev).
			if response.Good == "" {
				response.NoDisciplinedLane = true
				response.BestSubFloorSpread = bestSpreadPerUnit(lanes)
			}
			logger.Log("INFO", "No lane clears the discipline floor - releasing ship without trading", map[string]interface{}{
				"ship_symbol":           cmd.ShipSymbol,
				"system":                cmd.SystemSymbol,
				"floor":                 trading.MinBidMargin,
				"best_sub_floor_spread": bestSpreadPerUnit(lanes),
				"ranked_lane_count":     len(lanes),
				"circuits_flown":        response.Circuits,
			})
			break
		}

		// Routability guard (sp-7gr2), directed lane only: an operator who pins a
		// cross-system --dest must not have the hull buy a tranche it then cannot
		// deliver — the arb-run incident (bought, flew to the gate, found no route to
		// JP61, crashed laden) in the circuit's directed form. Verify the selected
		// lane's sell system is reachable over the gate graph BEFORE the circuit's
		// first buy; unroutable (or an unverifiable check → fail closed) stops the run
		// CLEANLY and EMPTY. Scoped to the directed path (TargetDest set): the
		// undirected auto-scan re-ranks and would just re-select the same lane, and its
		// cross-system lanes are already ranking-penalized + caught honestly by
		// travel()/liquidation if flown. No gate graph wired skips the guard (fail-open
		// on the missing port, matching travel()'s own single-jump fallback).
		if cmd.TargetDest != "" && h.gateGraph != nil {
			laneSrcSystem := shared.ExtractSystemSymbol(lane.SourceWaypoint)
			laneDstSystem := shared.ExtractSystemSymbol(lane.DestWaypoint)
			if laneSrcSystem != laneDstSystem {
				routable, rerr := h.gateGraph.Routable(ctx, laneSrcSystem, laneDstSystem, playerID)
				if rerr != nil || !routable {
					response.RoutabilityAbort = true
					if rerr != nil {
						response.AbortReason = fmt.Sprintf("could not verify a jump-gate route from %s to %s for directed lane %s - not committing the buy (fail-closed): %v", laneSrcSystem, laneDstSystem, lane.Good, rerr)
					} else {
						response.AbortReason = fmt.Sprintf("no jump-gate route from %s (buy %s) to %s (sell %s) for directed lane %s - not committing a buy that cannot be delivered", laneSrcSystem, lane.SourceWaypoint, laneDstSystem, lane.DestWaypoint, lane.Good)
					}
					logger.Log("WARNING", response.AbortReason, map[string]interface{}{
						"ship_symbol": cmd.ShipSymbol, "good": lane.Good,
						"source": lane.SourceWaypoint, "dest": lane.DestWaypoint,
						"source_system": laneSrcSystem, "dest_system": laneDstSystem,
					})
					exitReason = exitReasonUnroutable
					break
				}
			}
		}

		// Pre-flight cargo gate (sp-xwa1): a hull with no free hold cannot buy a
		// tranche, so park BEFORE committing the circuit rather than burning
		// starvation cycles on a non-empty hull or flying a cross-system round trip
		// to buy nothing (the exact zero-tranche starvation the root cause named).
		// Checked here, once a lane is chosen, so the park reason names the good the
		// hull would have traded; runCircuit re-checks before each buy leg as
		// accumulated cargo fills the hold, covering mid-circuit fills this can't see.
		if free := ship.AvailableCargoSpace(); free < minFreeCargoForCircuit {
			exitReason = exitReasonCargoBlocked
			response.CargoBlocked = true
			response.CargoBlockReason = fmt.Sprintf(
				"hull has %d free cargo unit(s), needs >=%d to buy %s at %s",
				free, minFreeCargoForCircuit, lane.Good, lane.SourceWaypoint)
			cargoBlockedLog(logger, lane.Good, minFreeCargoForCircuit, free, "hull has no free hold to buy a tranche")
			break
		}

		response.Good = lane.Good
		response.SourceWaypoint = lane.SourceWaypoint
		response.DestWaypoint = lane.DestWaypoint
		response.Circuits++

		// sp-q1ca: this line used to print no structured payload — the captain could
		// not tell which lane a daemon picked, or whether cross-system lanes were even
		// scanned, without inferring it from nav destinations. laneLogPayload carries
		// the SELECTED lane's full identity (both endpoints' waypoint+system, margin,
		// cross-system flag); laneLogCandidates attaches the top-ranked shortlist so a
		// penalized-but-present cross-system lane (see rankLanesWithGatePenalty) is
		// verifiable in the log even when a home lane wins the selection.
		selectionPayload := laneLogPayload(lane)
		selectionPayload["ship_symbol"] = cmd.ShipSymbol
		selectionPayload["circuit"] = response.Circuits
		selectionPayload["candidates"] = laneLogCandidates(lanes)
		// sp-149h: put the payload in the MESSAGE TEXT, not just the metadata map the
		// `container logs` renderer drops — the captain greps the CLI output to verify
		// which lane (and whether a cross-system one) was picked and at what margin.
		logger.Log("INFO", laneSelectionMessage(lane, lanes), selectionPayload)

		visitsBefore := response.Visits
		ship = h.runCircuit(ctx, cmd, lane, ship, response, runMaxVisits)

		if response.CargoStranded {
			// The circuit ended laden and the bounded finish-current-leg
			// liquidation could not empty the hold (sp-1hj5). Checked before
			// AbortReason: a stranded exit usually carries the failed leg's
			// AbortReason too, and stranded is the truth that matters — this
			// run is a FAILURE (CompletionOutcome vetoes the runner's
			// success=true, sp-7yej invariant 2).
			exitReason = exitReasonCargoStranded
			break
		}
		if response.AbortReason != "" {
			exitReason = exitReasonError
			break
		}
		if response.StaleAskAbort {
			exitReason = exitReasonStaleAsk
			break
		}
		if response.SpendFloorAbort {
			exitReason = exitReasonSpendFloor
			break
		}
		if response.NegativeMarginAbort {
			exitReason = exitReasonNegativeMargin
			break
		}
		if response.CargoBlocked {
			// A buy leg found the hold filled with cargo bought this circuit (the
			// pre-flight gate above catches a hull that starts non-empty). Either way
			// the hull can't buy — stop the run, don't re-select into the same wall.
			exitReason = exitReasonCargoBlocked
			break
		}
		if response.Visits == visitsBefore {
			// The ranked lane flew zero visits (e.g. the live re-check killed it
			// immediately). Bound how many consecutive commitments can go nowhere
			// before calling it starvation, so a persistently-wrong ranking can't
			// spin forever re-selecting the same dead lane.
			noProgressStreak++
			if noProgressStreak >= noProgressStarvationLimit {
				exitReason = exitReasonStarvation
				break
			}
			continue
		}
		noProgressStreak = 0
	}
	if exitReason == "" {
		exitReason = exitReasonMaxCircuits
	}
	response.ExitReason = exitReason

	response.NetProfit = response.TotalRevenue - response.TotalCost
	logger.Log("INFO", "Trade-route run complete", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"good":          response.Good,
		"circuits":      response.Circuits,
		"exit_reason":   response.ExitReason,
		"visits":        response.Visits,
		"units_traded":  response.UnitsTraded,
		"total_cost":    response.TotalCost,
		"total_revenue": response.TotalRevenue,
		"net_profit":    response.NetProfit,
	})
	return nil
}

// runCircuit flies one lane commitment and ENFORCES the finish-current-leg rule
// (sp-1hj5, sp-7yej invariant 1) at its single exit: however the visit loop
// stopped — budget consumed, margin death, a failed leg, volume exhaustion —
// cargo bought this run may still be aboard (a leg interrupted between its buy
// and its sell, or a partial sell's carryover). Before the circuit may return,
// liquidateHeld makes a bounded deliver+sell effort to empty the hold; if cargo
// STILL remains, the run is marked CargoStranded so the container runner
// terminates it as a FAILURE (invariant 2: honest completion) instead of the
// laden success=true of the incident. Structurally, every exit path from the
// visit loop funnels through this epilogue — a future exit condition cannot
// bypass the laden check.
//
// It returns the ship pointer current as of wherever the circuit ended: travel()
// may return a freshly-reloaded pointer after a cross-system jump, so the outer
// loop (sp-wlev) must carry this forward into its next circuit rather than reuse
// its own now-stale reference.
func (h *RunTradeRouteCoordinatorHandler) runCircuit(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
	runMaxVisits int,
) *navigation.Ship {
	logger := common.LoggerFromContext(ctx)

	ship, held := h.flyVisits(ctx, cmd, lane, ship, response, runMaxVisits)
	if held == 0 {
		return ship
	}

	// FINISH-CURRENT-LEG (sp-1hj5): the loop stopped between a buy and the sell
	// that closes it. Complete the leg before the run may end.
	logger.Log("WARNING", fmt.Sprintf(
		"Circuit stopped holding %d %s bought this run - finishing the current leg before the run may end (%s)",
		held, lane.Good, exitCause(response)), map[string]interface{}{
		"action": "finish_current_leg",
		"good":   lane.Good,
		"held":   held,
		"dest":   lane.DestWaypoint,
	})
	ship, held = h.liquidateHeld(ctx, cmd, lane, ship, response, held)
	if held == 0 {
		logger.Log("INFO", "Current leg finished: held cargo fully sold at destination - run exits empty", map[string]interface{}{
			"action": "finish_current_leg_done",
			"good":   lane.Good,
			"dest":   lane.DestWaypoint,
		})
		return ship
	}

	// HONEST COMPLETION (sp-7yej invariant 2): still laden after the bounded
	// effort. Mark the run stranded — the runner reads this through
	// CompletionOutcome and refuses success=true — and emit the one structured
	// cargo_aboard_exit record every laden exit shares (sp-149h).
	response.CargoStranded = true
	response.CargoStrandedUnits = held
	response.CargoStrandedReason = fmt.Sprintf(
		"stranded cargo: %d unsold units of %s aboard %s (lane %s -> %s): %s",
		held, lane.Good, cmd.ShipSymbol, lane.SourceWaypoint, lane.DestWaypoint, exitCause(response))
	cargoAboardExitLog(logger, "ERROR", lane, held, response.CargoStrandedReason)
	return ship
}

// exitCause names why the visit loop stopped, for the finish-current-leg and
// stranded records: the failed leg's verbatim cause when one aborted, otherwise
// the orderly-exit shape (so a margin-death exit with carryover aboard still
// reports something meaningful).
func exitCause(response *RunTradeRouteCoordinatorResponse) string {
	if response.AbortReason != "" {
		return response.AbortReason
	}
	return "orderly circuit exit with cargo aboard"
}

// liquidateHeld is the finish-current-leg engine (sp-1hj5, sp-7yej invariant 1):
// deliver the held cargo to the lane's destination and sell it down to empty.
// Each attempt re-runs the full sell half — travel (idempotent when already
// there), dock, re-observe the importer, sell one tranche — so it recovers from
// exactly the failures that interrupt a leg: a transient jump/navigate error
// (the incident's 'Starting jump operation' → instant failure), the nav-cache
// dock race, an importer volume tick at zero. Consecutive-failure bounded
// (liquidationMaxFailures) with a clock backoff between failures; any sold
// tranche resets the count, so a deep hold draining 10 units a tick is progress,
// not failure. Sells here are liquidation of already-bought cargo — sunk cost
// being recovered — so the bid-floor discipline (a BUY gate) deliberately does
// not apply; revenue and units still land on the run's ledger.
func (h *RunTradeRouteCoordinatorHandler) liquidateHeld(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
	held int,
) (*navigation.Ship, int) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID

	failures := 0
	attemptFailed := func(stage string, cause error) {
		failures++
		outcome := "backing off and retrying"
		if failures >= liquidationMaxFailures {
			outcome = "giving up"
		}
		// Verbatim cause in the MESSAGE, not just the metadata the container-log
		// renderer drops (sp-ynuf/sp-iqyq convention).
		logger.Log("WARNING", fmt.Sprintf(
			"Finish-current-leg %s failed (attempt %d/%d): %v - %s",
			stage, failures, liquidationMaxFailures, cause, outcome),
			map[string]interface{}{
				"action": "finish_current_leg_retry",
				"stage":  stage,
				"held":   held,
				"error":  cause.Error(),
			})
		if failures < liquidationMaxFailures {
			h.clock.Sleep(liquidationRetryBackoff)
		}
	}

	for held > 0 && failures < liquidationMaxFailures {
		var err error
		ship, err = h.travel(ctx, ship, lane.DestWaypoint, playerID)
		if err != nil {
			attemptFailed("travel to destination", err)
			continue
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			attemptFailed("dock at destination", err)
			continue
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			attemptFailed("re-observe destination", err)
			continue
		}
		tranche := trading.VisitTranche(dstGood.TradeVolume(), held)
		if tranche <= 0 {
			attemptFailed("sell", fmt.Errorf("importer trade volume exhausted (%d) while holding %d %s", dstGood.TradeVolume(), held, lane.Good))
			continue
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, tranche, playerID)
		if err != nil {
			attemptFailed("sell", err)
			continue
		}
		if sellResp.UnitsSold <= 0 {
			attemptFailed("sell", fmt.Errorf("sell of %d %s reported zero units sold", tranche, lane.Good))
			continue
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		failures = 0 // progress resets the failure budget
		logger.Log("INFO", fmt.Sprintf("Finish-current-leg sold %d %s at %s (%d still aboard)",
			sellResp.UnitsSold, lane.Good, lane.DestWaypoint, held), map[string]interface{}{
			"action": "finish_current_leg_sell",
			"good":   lane.Good,
			"sold":   sellResp.UnitsSold,
			"held":   held,
		})
	}
	return ship, held
}

// flyVisits is the lane's visit loop: disciplined tranches until the destination
// bid falls below basis+1000, tradable volume dries up, the RUN's remaining
// visit budget is consumed, or a leg fails. It returns the current ship pointer
// and how many units bought this run are still aboard — the caller (runCircuit)
// owns what a non-empty hold means; no exit path here may claim the run.
func (h *RunTradeRouteCoordinatorHandler) flyVisits(
	ctx context.Context,
	cmd *RunTradeRouteCoordinatorCommand,
	lane trading.ArbitrageLane,
	ship *navigation.Ship,
	response *RunTradeRouteCoordinatorResponse,
	runMaxVisits int,
) (*navigation.Ship, int) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID
	reserve := cmd.WorkingCapitalReserve
	if reserve <= 0 {
		reserve = defaultWorkingCapitalReserve
	}

	held := 0
	circuitNetMargin := 0 // this circuit's own sell revenue minus its own buy cost (sp-bp6f fix #2)
	// The loop draws down the RUN's budget (response.Visits is run-cumulative,
	// sp-1hj5), not a per-lane allowance; i still indexes this circuit's own
	// attempts for the first-visit stale-ask check and the realized-margin gate.
	// Termination: every iteration either returns or completes a sell
	// (response.Visits++), so the condition strictly progresses.
	for i := 0; response.Visits < runMaxVisits; i++ {
		// Re-observe both ends: basis (source ask we pay) and the live dest bid.
		srcGood, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Source market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship, held
		}
		dstGood, err := h.observeGood(ctx, lane.DestWaypoint, lane.Good, playerID)
		if err != nil {
			logger.Log("INFO", "Destination market no longer readable - ending circuit", map[string]interface{}{
				"waypoint": lane.DestWaypoint, "good": lane.Good, "error": err.Error(),
			})
			return ship, held
		}

		basis := srcGood.SellPrice()       // ask: what we PAY buying from the source
		destBid := dstGood.PurchasePrice() // bid: what we RECEIVE selling to the dest

		// Bid-floor discipline: the edge is gone once the dest bid stops clearing
		// basis+1000. Stop here rather than grind the spread to nothing.
		if !trading.MarginAlive(destBid, basis) {
			logger.Log("INFO", "Margin dead - stopping circuit at the bid-floor", map[string]interface{}{
				"good": lane.Good, "dest_bid": destBid, "basis": basis, "floor": basis + trading.MinBidMargin,
			})
			return ship, held
		}

		// Size the tranche to the hull's AVAILABLE hold, not its total capacity: an idle
		// hull is not necessarily empty (a factory hauler benched mid-task, a pool hull
		// with leftover cargo), and the buy executor refuses any tranche larger than
		// AvailableCargoSpace. Sizing by CargoCapacity would overshoot the free hold on a
		// non-empty hull and get the buy rejected — a distinct zero-visit path from the
		// sp-2sam root cause (the navigate self-collision, fixed in the CLI runner), hardened
		// here so a residual-cargo hull still flies. AvailableCargoSpace already nets out the
		// residual cargo; held is this run's own bought-not-yet-sold units on top.
		cargoSpace := ship.AvailableCargoSpace() - held
		// Split the old "volume or hold exhausted" guard into its two distinct causes
		// (sp-xwa1). A HULL-side stall (no free hold) and a MARKET-side one (source
		// volume dried up) used to share one indistinguishable line, hiding WHICH it
		// was — the silent-cause defect the root cause named. Check the hold first: a
		// pre-buy cargo park is the same class as the pre-flight gate, parking with a
		// structured reason rather than buying a useless sliver as accumulated cargo
		// fills the hold. The outer loop reads CargoBlocked and stops the run.
		if cargoSpace < minFreeCargoForCircuit {
			response.CargoBlocked = true
			response.CargoBlockReason = fmt.Sprintf(
				"hull has %d free cargo unit(s) (holding %d bought this circuit), needs >=%d for %s",
				cargoSpace, held, minFreeCargoForCircuit, lane.Good)
			cargoBlockedLog(logger, lane.Good, minFreeCargoForCircuit, cargoSpace, "hold filled with cargo bought this circuit")
			return ship, held
		}
		buyUnits := trading.VisitTranche(srcGood.TradeVolume(), cargoSpace)
		if buyUnits <= 0 {
			logger.Log("INFO", "No tranche to buy (source market volume exhausted) - stopping circuit", map[string]interface{}{
				"good": lane.Good, "source_volume": srcGood.TradeVolume(), "cargo_space": cargoSpace,
			})
			return ship, held
		}

		// Working-capital spend floor (sp-bp6f): refuse the buy BEFORE traveling,
		// docking, or purchasing if it would drop live treasury below the reserve.
		// Must run here, ahead of Leg 1 committing to anything — a circuit that
		// already docked and spent before checking has defeated the floor's whole
		// purpose. Uses basis (this visit's live source ask, re-observed above),
		// not lane.SourceAsk (the STALE ranked basis), so the projected cost
		// reflects what the circuit is actually about to pay right now.
		if h.spendFloorBreached(ctx, buyUnits*basis, reserve, response) {
			return ship, held
		}

		// Leg 1: buy a tranche at the source (exporter). A cross-system lane
		// jumps instead of navigating (sp-wlev); travel reloads the ship
		// afterward so this pointer reflects its post-jump state.
		ship, err = h.travel(ctx, ship, lane.SourceWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to source %s failed: %v", lane.SourceWaypoint, err)
			logger.Log("WARNING", "Travel to source failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at source %s failed: %v", lane.SourceWaypoint, err)
			// Put the verbatim cause in the MESSAGE — the container-log renderer drops the
			// metadata map, so a cause hidden in {"error": ...} never reaches an operator
			// (sp-ynuf defect 1, the sp-iqyq class). A blind dock failure now names itself.
			logger.Log("WARNING", fmt.Sprintf("Dock at source %s failed: %v - ending circuit", lane.SourceWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}

		// Live-verify the ranked basis before the FIRST buy (hazard b): the lane was
		// ranked from a market cache that can be many minutes stale. Now that the hull
		// is docked at the source (the API returns live prices only with a ship present),
		// re-read the source ask and abort if it has run away from the basis the lane
		// was ranked on — buying on a stale basis has realised a large loss (a -196k
		// precedent). Only the first visit re-verifies; later visits already re-observe.
		if i == 0 && h.staleAskAborts(ctx, lane, playerID, response) {
			return ship, held
		}

		buyResp, err := h.purchase(ctx, ship.ShipSymbol(), lane.Good, buyUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("purchase of %d %s at source %s failed: %v", buyUnits, lane.Good, lane.SourceWaypoint, err)
			logger.Log("WARNING", "Purchase failed - ending circuit", map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		held += buyResp.UnitsAdded
		response.TotalCost += buyResp.TotalCost
		circuitNetMargin -= buyResp.TotalCost

		// Leg 2: sell what we hold at the destination (importer). A
		// cross-system lane jumps instead of navigating (sp-wlev).
		ship, err = h.travel(ctx, ship, lane.DestWaypoint, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("travel to destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			// Verbatim cause in the MESSAGE (sp-ynuf); the finish-current-leg
			// epilogue owns whether this becomes a recovered leg or the one
			// structured cargo_aboard_exit strand record (sp-1hj5).
			logger.Log("WARNING", fmt.Sprintf("Travel to destination %s failed with %d %s aboard: %v - finishing leg via liquidation", lane.DestWaypoint, held, lane.Good, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		if err := h.dock(ctx, ship, playerID); err != nil {
			response.AbortReason = fmt.Sprintf("dock at destination %s failed (cargo aboard): %v", lane.DestWaypoint, err)
			// Verbatim cause in the MESSAGE (not the dropped metadata field) so a blind
			// dock-at-destination failure names itself — same defect-1 fix as the source leg.
			logger.Log("WARNING", fmt.Sprintf("Dock at destination %s failed (cargo aboard): %v - ending circuit", lane.DestWaypoint, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		sellUnits := trading.VisitTranche(dstGood.TradeVolume(), held)
		if sellUnits <= 0 {
			// The importer has no tradable volume this tick while we hold cargo: not a
			// clean margin-death, so surface it rather than return silently (the one
			// early-return that used to vanish without a trace).
			response.AbortReason = fmt.Sprintf("destination %s has no sellable volume for %s while holding %d units", lane.DestWaypoint, lane.Good, held)
			logger.Log("INFO", fmt.Sprintf("No sellable tranche at destination %s (importer trade volume %d exhausted) with %d %s aboard - finishing leg via liquidation", lane.DestWaypoint, dstGood.TradeVolume(), held, lane.Good), nil)
			return ship, held
		}
		sellResp, err := h.sell(ctx, ship.ShipSymbol(), lane.Good, sellUnits, playerID)
		if err != nil {
			response.AbortReason = fmt.Sprintf("sell of %d %s at destination %s failed (cargo aboard): %v", sellUnits, lane.Good, lane.DestWaypoint, err)
			logger.Log("WARNING", fmt.Sprintf("Sell of %d %s at destination %s failed with %d aboard: %v - finishing leg via liquidation", sellUnits, lane.Good, lane.DestWaypoint, held, err), map[string]interface{}{"error": err.Error()})
			return ship, held
		}
		held -= sellResp.UnitsSold
		response.TotalRevenue += sellResp.TotalRevenue
		response.UnitsTraded += sellResp.UnitsSold
		circuitNetMargin += sellResp.TotalRevenue
		response.Visits++

		// Per-circuit negative-margin abort (sp-bp6f fix #2): this circuit's own
		// realized fills - not the stale ranked spread - are what matter once
		// trading has actually started. Gate on i+1 (visit count) so one noisy
		// early fill can't trip it; negativeMarginAbortVisits consecutive visits
		// of net-negative realized margin means the lane itself has turned
		// loss-making, e.g. the incident's repeated tranches walking the source
		// ask up while the destination bid gets crushed.
		if i+1 >= negativeMarginAbortVisits && circuitNetMargin < 0 {
			response.NegativeMarginAbort = true
			response.RealizedCircuitMargin = circuitNetMargin
			logger.Log("WARNING", "Circuit realized margin ran negative - aborting before it compounds", map[string]interface{}{
				"good": lane.Good, "visits": response.Visits, "realized_margin": circuitNetMargin,
			})
			return ship, held
		}
	}

	// The loop condition ran out: the RUN's visit budget is consumed. This is a
	// leg boundary (Visits only advances on a completed sell); the outer loop's
	// budget check turns it into the clean exitReasonMaxVisits stop (sp-1hj5).
	logger.Log("INFO", "Run visit budget consumed - ending circuit at the leg boundary", map[string]interface{}{
		"good": lane.Good, "visits": response.Visits, "max_visits": runMaxVisits,
	})
	return ship, held
}

// spendFloorBreached live-checks whether spending projectedCost right now would
// drop treasury below reserve (sp-bp6f), setting the abort fields on response
// and returning true if so — the caller must not proceed with the buy.
//
// Fails OPEN when no apiClient is wired (h.apiClient == nil): the guard is
// simply unavailable, the same optional-port contract staleAskAborts already
// uses for marketRefresher, so callers that cannot supply a live API client
// (most tests) run without it rather than being forced to fake one.
//
// Fails CLOSED on every live-read failure (an unresolvable player token, or the
// GetAgent call itself erroring): unlike staleAskAborts' infrastructure-gap
// tolerance, a guard whose entire job is stopping the treasury from going
// negative must never let a buy through just because it went blind. An API
// hiccup here aborts the circuit instead of silently trading past the floor.
func (h *RunTradeRouteCoordinatorHandler) spendFloorBreached(
	ctx context.Context,
	projectedCost int,
	reserve int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return false
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Could not resolve player token for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", "Could not read live treasury for spend-floor check - aborting circuit (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if agentData.Credits-projectedCost < reserve {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting circuit before spending", map[string]interface{}{
			"treasury": agentData.Credits, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = agentData.Credits
		response.ReserveFloor = reserve
		return true
	}

	return false
}

// staleAskAborts live-verifies the source ask before the first buy and reports
// whether the circuit must abort because the ask has moved beyond
// trading.StaleAskMovePercent from the basis the lane was ranked on (hazard b). The
// lane is ranked from a cache that can be many minutes stale; executing on a moved
// basis has realised large losses. It refreshes the source market from the API (the
// hull is docked there, so the API returns live prices), re-reads the ask, and
// compares it to lane.SourceAsk.
//
// Fail-open on infrastructure gaps, fail-closed only on a CONFIRMED move: with no
// refresher wired, or when the refresh/read itself fails, it proceeds on the ranked
// basis (a transient scan hiccup must not strand an otherwise-good circuit). Only a
// live ask that is actually present AND beyond tolerance aborts the run.
func (h *RunTradeRouteCoordinatorHandler) staleAskAborts(
	ctx context.Context,
	lane trading.ArbitrageLane,
	playerID int,
	response *RunTradeRouteCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.marketRefresher == nil {
		return false
	}

	if err := h.marketRefresher.ScanAndSaveMarket(ctx, uint(playerID), lane.SourceWaypoint); err != nil {
		logger.Log("WARNING", "Could not refresh source market to live-verify basis - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveSrc, err := h.observeGood(ctx, lane.SourceWaypoint, lane.Good, playerID)
	if err != nil {
		logger.Log("WARNING", "Could not read live source ask after refresh - proceeding on ranked basis", map[string]interface{}{
			"waypoint": lane.SourceWaypoint, "good": lane.Good, "error": err.Error(),
		})
		return false
	}

	liveAsk := liveSrc.SellPrice()
	if trading.AskMovedBeyondTolerance(liveAsk, lane.SourceAsk) {
		response.StaleAskAbort = true
		response.RankedSourceAsk = lane.SourceAsk
		response.LiveSourceAsk = liveAsk
		logger.Log("WARNING", "Source ask moved beyond tolerance since the lane was ranked - aborting circuit before first buy", map[string]interface{}{
			"good": lane.Good, "source": lane.SourceWaypoint,
			"ranked_ask": lane.SourceAsk, "live_ask": liveAsk, "tolerance_pct": trading.StaleAskMovePercent,
		})
		return true
	}

	logger.Log("INFO", "Live-verified source ask within tolerance - proceeding with the circuit", map[string]interface{}{
		"good": lane.Good, "source": lane.SourceWaypoint, "ranked_ask": lane.SourceAsk, "live_ask": liveAsk,
	})
	return false
}

// loadShip loads the hull the daemon container runner already claimed for this
// circuit. It does NOT claim or release: the runner owns the assignment lifecycle
// (createShipAssignments on start via the container's ship_symbol metadata,
// releaseShipAssignments on completion/crash/cancel), so the hull is force-released
// on every terminal path without this handler touching it (sp-zewt). The idle-gap
// discipline — only fly a genuinely idle hull, never steal one — is enforced ahead of
// time at DaemonServer.StartTradeRoute, before the container is persisted.
func (h *RunTradeRouteCoordinatorHandler) loadShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	return ship, nil
}

// scanLanes builds cross-market listings for the system from cache, plus (sp-wlev)
// every system one jump-gate hop away, and ranks them all in a single pass so
// gate-crossing lanes can surface alongside home-system ones. Aggregating BEFORE
// ranking (rather than ranking each system separately) is what lets a good that
// only exports in one system and only imports in another form a lane at all —
// trading.RankSpreads pairs listings purely by good and waypoint, indifferent to
// which system either side is in. Cross-system candidates are then penalized via
// rankLanesWithGatePenalty to reflect the jump+cooldown time cost a raw per-unit
// spread can't see.
//
// Neighbor discovery is fail-open: a system with no jump gate, or a discovery
// query that errors, simply contributes no extra listings — never an aborted
// scan. One hop only (no recursive multi-hop chase): out of scope for sp-wlev.
//
// Multi-daemon lane-dedupe semantics (sp-q1ca, confirmed): scanLanes has NO
// awareness of other concurrently-running trade-route daemons or their active
// circuits — there is no registry of in-flight lanes and no query of what any
// other hull is doing. Two daemons started at the same instant against the same
// system WILL rank identically and both select the same top lane; there is no
// explicit dedupe. Divergence observed in practice (e.g. one hull landing on a
// different lane than another started moments later) is an EMERGENT side effect
// of the shared market cache, not deliberate coordination: after a hull finishes
// a buy/sell batch, the handler issues one extra live GET of that waypoint's
// market and overwrites the cached rows (see cargo_transaction.go's
// refreshMarketData -> MarketScanner.ScanAndSaveMarket -> UpsertMarketData),
// synchronously before the command returns. h.collectSystemListings reads that
// same cache with a plain uncached query, so a daemon that scans shortly AFTER
// another hull has already traded into a lane's destination sees that lane's
// decayed bid and naturally re-ranks it lower — no locking, no CAS, last writer
// wins. Two daemons racing to scan at the SAME moment (before either has traded)
// can still legitimately pick the same lane; only a bid-floor "margin died" stop
// on one of them or a later rescan resolves the collision, not the ranker itself.
func (h *RunTradeRouteCoordinatorHandler) scanLanes(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	shipCapacity int,
	targetDest string,
) ([]trading.ArbitrageLane, error) {
	listings, err := h.collectSystemListings(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	for _, neighbor := range h.neighborSystems(ctx, systemSymbol, playerID) {
		neighborListings, err := h.collectSystemListings(ctx, neighbor, playerID)
		if err != nil {
			continue // fail-open: an unreadable neighbor system just yields fewer lanes
		}
		listings = append(listings, neighborListings...)
	}

	// Ranker age-cap (sp-xwa1): a lane priced from a market observation older than
	// maxListingAge can already have moved, so ranking it chases a spread that no
	// longer exists. An UNDIRECTED auto-scan drops stale rows before ranking so a
	// stale lane can't win selection and execute at moved prices. A DIRECTED --dest
	// scan keeps every row — the operator's lane is re-verified LIVE at execution
	// (staleAskAborts + the per-visit margin re-check), so staleness must never
	// SILENTLY veto it — but logs the retained stale rows so the reliance on live
	// re-verification is visible. Either way the exclusion/retention is put in the
	// MESSAGE TEXT (staleListingSummary), which `container logs` keeps even though it
	// drops the metadata map (sp-149h/sp-iqyq renderer defect).
	logger := common.LoggerFromContext(ctx)
	fresh, stale := partitionListingsByAge(listings, h.clock.Now(), maxListingAge)
	if len(stale) > 0 {
		if targetDest == "" {
			logger.Log("INFO", fmt.Sprintf(
				"Excluded %d stale market listing(s) older than %s from undirected lane ranking: %s",
				len(stale), maxListingAge, staleListingSummary(stale)),
				map[string]interface{}{
					"action":          "stale_listings_excluded",
					"count":           len(stale),
					"max_age_minutes": int(maxListingAge.Minutes()),
				})
			listings = fresh
		} else {
			logger.Log("INFO", fmt.Sprintf(
				"Retained %d stale market listing(s) for directed --dest %q (re-verified live at execution, not vetoed): %s",
				len(stale), targetDest, staleListingSummary(stale)),
				map[string]interface{}{
					"action":          "stale_listings_retained_directed",
					"count":           len(stale),
					"target_dest":     targetDest,
					"max_age_minutes": int(maxListingAge.Minutes()),
				})
			// listings unchanged: the directed path ranks all rows; live re-verify guards it.
		}
	}

	// Hold-vs-absorption weighting (sp-pnx0) and the cross-system jump-gate
	// penalty are folded into ONE scoring pass inside rankLanesWithGatePenalty,
	// not chained as two sequential re-rankings: both are "recompute-from-
	// scratch" rankers that derive their score purely from each lane's own
	// fields, ignoring input order, so composing them as funcB(funcA(lanes))
	// would let funcB silently discard funcA's reordering. Start from the
	// plain trading.RankSpreads order (not RankSpreadsForHold) since hold-fit
	// weighting is applied here via shipCapacity instead. targetDest (sp-xwa1)
	// waives the cross-system penalty for the operator-directed lane only —
	// see rankLanesWithGatePenalty's doc.
	return rankLanesWithGatePenalty(trading.RankSpreads(listings), shipCapacity, targetDest), nil
}

// collectSystemListings reads every cached market in one system into flat
// GoodListing rows, the shared building block scanLanes aggregates across the
// home system and its jump-gate neighbors before ranking.
func (h *RunTradeRouteCoordinatorHandler) collectSystemListings(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]trading.GoodListing, error) {
	waypoints, err := h.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list markets in %s: %w", systemSymbol, err)
	}

	var listings []trading.GoodListing
	for _, wp := range waypoints {
		mkt, err := h.marketRepo.GetMarketData(ctx, wp, playerID)
		if err != nil || mkt == nil {
			continue // an unreadable market simply doesn't contribute lanes
		}
		for _, g := range mkt.TradeGoods() {
			listings = append(listings, trading.GoodListing{
				Good:      g.Symbol(),
				Waypoint:  mkt.WaypointSymbol(),
				TradeType: string(g.TradeType()),
				Bid:       g.PurchasePrice(), // market BUY column = what we receive selling TO it
				Ask:       g.SellPrice(),     // market SELL column = what we pay buying FROM it
				Supply:    derefString(g.Supply()),
				Activity:  derefString(g.Activity()),
				Volume:    g.TradeVolume(),
				// Stamp each row with the market snapshot's freshness so the ranker can
				// reject stale-priced lanes (sp-xwa1). One timestamp covers all of a
				// waypoint's goods — a market scan observes the whole board at once.
				ObservedAt: mkt.LastUpdated(),
			})
		}
	}
	return listings, nil
}

// neighborSystems returns the systems one jump directly away from systemSymbol's
// own jump gate, via the already-registered GetJumpGateConnectionsQuery. Any
// failure (no gate in the system, an API error, no player context) fails open to
// an empty neighbor set rather than surfacing an error — a multi-system trade
// route degrades to a home-system-only one, it never aborts the scan.
func (h *RunTradeRouteCoordinatorHandler) neighborSystems(ctx context.Context, systemSymbol string, playerID int) []string {
	resp, err := h.mediator.Send(ctx, &shipQuery.GetJumpGateConnectionsQuery{
		SystemSymbol: systemSymbol,
		PlayerID:     &playerID,
	})
	if err != nil {
		return nil
	}
	conn, ok := resp.(*shipQuery.GetJumpGateConnectionsResponse)
	if !ok || conn == nil {
		return nil
	}
	return conn.ConnectedSystems
}

// observeGood re-reads a single good's live cached row at a waypoint so the loop
// can watch the destination bid decay as the importer fills.
func (h *RunTradeRouteCoordinatorHandler) observeGood(
	ctx context.Context,
	waypoint, good string,
	playerID int,
) (*market.TradeGood, error) {
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil {
		return nil, err
	}
	if mkt == nil {
		return nil, fmt.Errorf("no cached market at %s", waypoint)
	}
	g := mkt.FindGood(good)
	if g == nil {
		return nil, fmt.Errorf("%s no longer trades %s", waypoint, good)
	}
	return g, nil
}

func (h *RunTradeRouteCoordinatorHandler) navigate(ctx context.Context, ship *navigation.Ship, destination string, playerID int) error {
	_, err := h.mediator.Send(ctx, &navCmd.NavigateRouteCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: destination,
		PlayerID:    shared.MustNewPlayerID(playerID),
	})
	return err
}

// cargoAboardExitLog emits the one structured record every PRE-SELL circuit exit
// shares (sp-149h): once the hull has BOUGHT and is holding cargo it could not
// sell, an operator needs to see WHAT is stranded and WHERE — good/source/dest/
// held/reason — on the log line itself, not buried in a bare {"error": ...} the
// container-log renderer drops (the sp-ynuf/sp-iqyq class the source-dock and
// dest-dock legs already fixed by putting the cause in the message). AbortReason
// still carries the operator-facing prose on the response; this is its structured
// telemetry twin, keyed so a stranded-cargo exit is greppable by action rather
// than by parsing prose. reason names the specific cause (a failed leg's verbatim
// error, or the exhausted-volume detail).
func cargoAboardExitLog(logger common.ContainerLogger, level string, lane trading.ArbitrageLane, held int, reason string) {
	logger.Log(level, "Circuit ending with cargo aboard", map[string]interface{}{
		"action": "cargo_aboard_exit",
		"good":   lane.Good,
		"source": lane.SourceWaypoint,
		"dest":   lane.DestWaypoint,
		"held":   held,
		"reason": reason,
	})
}

// cargoBlockedLog emits the structured pre-flight/pre-buy cargo park (sp-xwa1): the
// hull has too little free hold to buy a tranche, so it parks rather than failing
// mid-buy or buying a useless sliver. The good/needed/free/action/reason fields go in
// the MESSAGE TEXT — `container logs` drops the metadata map (the sp-149h/sp-iqyq
// renderer defect), so an operator reading the CLI must see WHY the hull parked on the
// line itself, not in a discarded map. reason distinguishes this HULL-side stop
// ("no free hold") from a market-side one ("source volume exhausted"), which the two
// causes used to share behind one indistinguishable "volume or hold exhausted" line.
func cargoBlockedLog(logger common.ContainerLogger, good string, needed, free int, reason string) {
	logger.Log("WARNING", fmt.Sprintf(
		"Pre-flight cargo check parked hull: good=%s needed>=%d free=%d action=empty-residual-cargo-before-trading reason=%s",
		good, needed, free, reason),
		map[string]interface{}{
			"action": "cargo_blocked",
			"good":   good,
			"needed": needed,
			"free":   free,
			"reason": reason,
		})
}

// dock docks the hull at its current waypoint, surviving the nav-cache race the goods
// factory hit (sp-n7yp): right after arrival the ship's cached nav_status can still
// read IN_TRANSIT, so the domain EnsureDocked rejects the dock ("cannot dock while in
// transit"). Rather than fail — and strand the circuit at zero visits — it reconciles
// the hull against the live API (SyncShipFromAPI clears the stale IN_TRANSIT once the
// arrival has actually landed) and retries, bounded by tradeRouteDockRetryLimit so a
// genuinely-undockable hull can never spin forever.
//
// Every attempt is dispatched by SHIP SYMBOL (nil Ship), never the coordinator's
// cached hull: passing the cached ship makes LoadShip return the stale IN_TRANSIT
// snapshot and the resync a no-op (the exact subtlety sp-n7yp's dockAndConfirm
// documents) — by symbol the handler reloads the freshly-synced nav_status each try.
// A dock that keeps failing returns its cause VERBATIM so the caller aborts the
// circuit self-diagnosingly instead of swallowing it (sp-ynuf).
func (h *RunTradeRouteCoordinatorHandler) dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	logger := common.LoggerFromContext(ctx)
	pid := shared.MustNewPlayerID(playerID)
	shipSymbol := ship.ShipSymbol()

	var lastErr error
	for attempt := 0; attempt <= tradeRouteDockRetryLimit; attempt++ {
		_, err := h.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol, // nil Ship: force a fresh reload of the true persisted nav_status
			PlayerID:   pid,
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == tradeRouteDockRetryLimit {
			break
		}
		// Most likely the nav-cache race: the arrival event has not yet flipped the
		// cached IN_TRANSIT to IN_ORBIT. Reconcile against the live API to refresh
		// nav_status, then retry. Bounded, so a genuine failure still surfaces below.
		logger.Log("WARNING", fmt.Sprintf("Dock of %s failed (attempt %d/%d): %v - resyncing from API and retrying", shipSymbol, attempt+1, tradeRouteDockRetryLimit+1, err), map[string]interface{}{
			"ship_symbol": shipSymbol, "attempt": attempt + 1, "error": err.Error(),
		})
		if _, serr := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, pid); serr != nil {
			return fmt.Errorf("dock of %s failed (%v); resync from API also failed: %w", shipSymbol, err, serr)
		}
	}
	return fmt.Errorf("dock of %s still failing after %d resync retries: %w", shipSymbol, tradeRouteDockRetryLimit, lastErr)
}

func (h *RunTradeRouteCoordinatorHandler) purchase(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.PurchaseCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(playerID),
	})
	if err != nil {
		return nil, err
	}
	pr, ok := resp.(*shipCargo.PurchaseCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected purchase response type %T", resp)
	}
	return pr, nil
}

func (h *RunTradeRouteCoordinatorHandler) sell(ctx context.Context, shipSymbol, good string, units, playerID int) (*shipCargo.SellCargoResponse, error) {
	resp, err := h.mediator.Send(ctx, &shipCargo.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      units,
		PlayerID:   shared.MustNewPlayerID(playerID),
	})
	if err != nil {
		return nil, err
	}
	sr, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected sell response type %T", resp)
	}
	return sr, nil
}

// bestSpreadPerUnit returns the highest per-unit spread among ranked lanes, used to
// report how far the best standing lane fell short of the discipline floor when none
// cleared it — so a no-trade run always reports WHY, never a silent zero. Lanes are
// ranked by CAPPED spread, so the deepest per-unit spread is not necessarily lanes[0].
func bestSpreadPerUnit(lanes []trading.ArbitrageLane) int {
	best := 0
	for _, l := range lanes {
		if l.SpreadPerUnit > best {
			best = l.SpreadPerUnit
		}
	}
	return best
}

// derefString flattens an optional supply/activity pointer to its value or "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// laneCandidateLogLimit bounds how many top-ranked candidates are attached to the
// lane-selection log line (sp-q1ca): enough to show cross-system candidates were
// actually scanned and ranked even when a penalized home lane wins the selection,
// without flooding the log with the full ranked set on a system with many goods.
const laneCandidateLogLimit = 5

// laneLogCandidates summarizes up to laneCandidateLogLimit top-ranked lanes (in
// their already-penalized rank order) into loggable payloads, so the lane-selection
// log line makes cross-system scanning VERIFIABLE rather than inferred (sp-q1ca):
// an operator can see the full ranked shortlist — including any cross-system
// candidates that lost to a penalized home lane — not just the one that won.
func laneLogCandidates(lanes []trading.ArbitrageLane) []map[string]interface{} {
	limit := laneCandidateLogLimit
	if len(lanes) < limit {
		limit = len(lanes)
	}
	candidates := make([]map[string]interface{}, 0, limit)
	for _, l := range lanes[:limit] {
		candidates = append(candidates, laneLogPayload(l))
	}
	return candidates
}

// laneLogPayload flattens one lane into the structured fields the captain needs to
// verify lane selection without inferring it from nav destinations (sp-q1ca): the
// good, both endpoints (waypoint + system), the per-unit margin, and whether the
// lane crosses a system boundary (source system != destination system).
func laneLogPayload(l trading.ArbitrageLane) map[string]interface{} {
	sourceSystem := shared.ExtractSystemSymbol(l.SourceWaypoint)
	destSystem := shared.ExtractSystemSymbol(l.DestWaypoint)
	return map[string]interface{}{
		"good":          l.Good,
		"source":        l.SourceWaypoint,
		"source_system": sourceSystem,
		"dest":          l.DestWaypoint,
		"dest_system":   destSystem,
		"cross_system":  sourceSystem != destSystem,
		"spread_per_u":  l.SpreadPerUnit,
		"volume_cap":    l.VolumeCap,
		"capped_spread": l.CappedSpread,
	}
}

// laneSelectionOneLiner renders one lane into a compact
// "GOOD SRC(SRCSYS)->DST(DSTSYS) m=SPREAD <same|cross>" token for the selection log
// message text (sp-149h). m is the per-unit margin (SpreadPerUnit); the same/cross
// tag makes a gate-crossing lane greppable without parsing the two system codes.
func laneSelectionOneLiner(l trading.ArbitrageLane) string {
	srcSys := shared.ExtractSystemSymbol(l.SourceWaypoint)
	dstSys := shared.ExtractSystemSymbol(l.DestWaypoint)
	scope := "same"
	if srcSys != dstSys {
		scope = "cross"
	}
	return fmt.Sprintf("%s %s(%s)->%s(%s) m=%d %s", l.Good, l.SourceWaypoint, srcSys, l.DestWaypoint, dstSys, l.SpreadPerUnit, scope)
}

// laneSelectionCandidateLimit bounds how many ranked candidates the selection log
// MESSAGE lists (sp-149h) — the captain's ask was "chosen lane + top-3 candidates one-
// liner". Kept smaller than laneCandidateLogLimit (the fuller metadata shortlist) so
// the message line stays a scannable one-liner, not a wall of every ranked lane.
const laneSelectionCandidateLimit = 3

// laneSelectionMessage builds the lane-selection LOG MESSAGE with the chosen lane's
// identity and the top-N candidate shortlist embedded in the TEXT (sp-149h). The
// structured payload is still attached as metadata for structured consumers, but the
// CLI `container logs` view drops the metadata map — so the captain grepping that
// output must see good/source/dest/margin on the line itself, not in a discarded map
// (the same sp-iqyq renderer defect the dock-failure and cargo-aboard legs already
// route around by putting the cause in the message). The stable prefix "Selected top
// disciplined arbitrage lane" is preserved — existing greps/tests that match it keep
// working — with the payload appended after a colon.
func laneSelectionMessage(chosen trading.ArbitrageLane, ranked []trading.ArbitrageLane) string {
	limit := laneSelectionCandidateLimit
	if len(ranked) < limit {
		limit = len(ranked)
	}
	tops := make([]string, 0, limit)
	for _, l := range ranked[:limit] {
		tops = append(tops, laneSelectionOneLiner(l))
	}
	return fmt.Sprintf("Selected top disciplined arbitrage lane: %s | top%d: %s",
		laneSelectionOneLiner(chosen), limit, strings.Join(tops, "; "))
}
