package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
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
	// eventSubscriber lets travel() wait out a hull that is already IN_TRANSIT
	// before any movement (sp-8l3o): a run re-adopted mid-hop must ride the
	// arrival out as a WAIT state, not attempt the jump/navigate now (which the
	// API rejects 4214 'in-transit' and burns the container's restart budget on).
	// Optional; nil skips the pre-movement wait so every existing caller/test is
	// byte-for-byte unchanged (fail-open, matching gateGraph's optional-port
	// contract). The daemon injects the shared ShipEventBus via SetEventSubscriber.
	eventSubscriber navigation.ShipEventSubscriber
	// absorptionLedger is the cross-engine market-absorption ledger (sp-78ai L4).
	// scanLanes consults it READ-ONLY (trade-analyst Q1: "circuits write nothing") to
	// exclude a lane whose sell side is shadowed or whose reserved depth can't absorb
	// a circuit tranche. Optional; nil leaves the consult fully inert (pre-L4
	// behavior), the same optional-port contract gateGraph/eventSubscriber use. The
	// daemon injects the shared ledger instance via SetAbsorptionLedger.
	absorptionLedger absorption.Ledger
	// absorptionConsultDisabled is the operator kill-switch (config
	// absorption.trade_route_consult_disabled): true skips the consult read entirely
	// and restores pre-L4 ranking byte-identically, even with a ledger wired.
	absorptionConsultDisabled bool
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
	// Connections returns fromSystem's directly-gated neighbor edges from the
	// persisted era-scoped adjacency (fetch-through on a cache miss/stale). Unlike
	// the live GetJumpGate scan, this durable read is origin-INDEPENDENT: it answers
	// even when the origin's own gate is uncharted or has no ship present (the live
	// API 4001 that returned zero reposition candidates from X1-DP51, sp-1ki5). Each
	// edge carries its build state so an under-construction neighbor is rejected, not
	// silently pre-flighted into a hop-time crash.
	Connections(ctx context.Context, fromSystem string, playerID int) ([]system.GateEdge, error)
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

// SetEventSubscriber wires the ship-arrival event bus so travel() can wait out a
// hull that is already IN_TRANSIT before attempting any movement (sp-8l3o). The
// daemon injects the shared ShipEventBus (the same publisher the RouteExecutor
// subscribes to). Left unset (nil), the pre-movement in-transit wait is skipped
// entirely — every existing caller/test behaves exactly as before this lever
// existed. Mirrors the SetGateGraph optional-injection idiom.
func (h *RunTradeRouteCoordinatorHandler) SetEventSubscriber(subscriber navigation.ShipEventSubscriber) {
	h.eventSubscriber = subscriber
}

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L4), the same
// optional-port idiom the other coordinator dependencies use. A nil ledger leaves the
// consult inert (pre-L4 behavior, byte-for-byte). consultDisabled is the operator
// kill-switch — unlike idle-arb's L2 SetAbsorptionLedger, there is no recording to
// keep alive when disabled, since trade-route circuits never write to the ledger
// (trade-analyst Q1: "circuits write nothing").
func (h *RunTradeRouteCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger, consultDisabled bool) {
	h.absorptionLedger = ledger
	h.absorptionConsultDisabled = consultDisabled
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

		// Publish the committed circuit lane to the read-only flow feed (fire-and-forget;
		// a missed publish never touches the trade path — RULINGS #4).
		flowfeed.Publish(buildTradeRouteFlow(cmd, lane, laneCircuitRatePerHour(lane, ship.CargoCapacity(), cmd.TargetDest), shipCargoItems(ship), time.Time{}, time.Now().UTC()))

		// sp-q1ca: this line used to print no structured payload — the captain could
		// not tell which lane a daemon picked, or whether cross-system lanes were even
		// scanned, without inferring it from nav destinations. laneLogPayload carries
		// the SELECTED lane's full identity (both endpoints' waypoint+system, margin,
		// cross-system flag); laneLogCandidates attaches the top-ranked shortlist so a
		// surcharged-but-present cross-system lane (see rankLanesByCircuitRate) is
		// verifiable in the log even when a home lane wins the selection.
		selectionPayload := laneLogPayload(lane)
		selectionPayload["ship_symbol"] = cmd.ShipSymbol
		selectionPayload["circuit"] = response.Circuits
		selectionPayload["candidates"] = laneLogCandidates(lanes)
		// sp-149h: put the payload in the MESSAGE TEXT, not just the metadata map the
		// `container logs` renderer drops — the captain greps the CLI output to verify
		// which lane (and whether a cross-system one) was picked and at what margin.
		logger.Log("INFO", laneSelectionMessage(lane, lanes, ship.CargoCapacity(), cmd.TargetDest), selectionPayload)

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
