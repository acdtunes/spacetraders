package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// probe_sensing_stall.go turns the parked-sensing tick and its off-gate/expansion pass into the
// three-way verdict the escalation layer consumes: PROGRESS, IDLE, or BLOCKED(reason).
//
// These are the two passes that hid a 33-system region for hours. The universe roster was
// unreadable — the crawl had died at page 318 and had NEVER once succeeded — the off-gate target
// selection failed every tick, and both reported "0 discovered", which is EXACTLY what a fully
// charted galaxy looks like. The fleet concluded it was sealed in a 57-system pocket. It was not:
// one unread jump gate led straight out of it.
//
// They get SEPARATE keys because they stall independently and for different reasons: the sensing
// tick wedges on its ports and its ledger, the expansion pass wedges on reach. Folding them into
// one verdict would let a busy sensing tick mask a permanently sealed frontier — which is precisely
// what happened.

const (
	// sensingStallCoordinator names the parked-sensing tick as a whole.
	sensingStallCoordinator = "parked_sensing"
	// expansionStallCoordinator names the off-gate/expansion pass driven from that tick.
	expansionStallCoordinator = "off_gate_expansion"
)

const (
	// stallReasonPortsUnwired: the engine surface is incomplete, so the tick holds fail-closed.
	// A permanent wedge that looks exactly like a quiet fleet — a half-wired engine plans
	// placements forever and fills none.
	stallReasonPortsUnwired health.StallReason = "ports_unwired"
	// stallReasonLedgerUnreadable: the sensing ledger could not be read at all.
	stallReasonLedgerUnreadable health.StallReason = "ledger_unreadable"
	// stallReasonEngineFailure: one or more engines failed this tick and NOTHING was
	// accomplished. Progress anywhere outranks it — a chronically noisy engine beside a working
	// fleet must not page.
	stallReasonEngineFailure health.StallReason = "engine_failure"
	// stallReasonBuyRefused: the drain tried to buy and every counter refused, for reasons that
	// are NOT the probe cap and NOT the treasury floor. A money guard doing its job is a correct
	// refusal, never a stall; this is the other kind.
	stallReasonBuyRefused health.StallReason = "buy_refused"
	// stallReasonExpansionError: the expansion pass could not complete.
	stallReasonExpansionError health.StallReason = "expansion_error"
	// stallReasonOffGateNoTarget: THE MEASURED PRODUCTION FAILURE. The gate-reachable frontier is
	// exhausted — there IS charting work and not one target is within gate reach — and no warp
	// target could be selected either. The fleet is sealed in whatever pocket it currently holds,
	// and every layer reports it as "0 discovered".
	stallReasonOffGateNoTarget health.StallReason = "off_gate_no_target"
	// stallReasonExpansionSkippedPrefix labels a pass held by its own gate. The sentinel is
	// appended (expansion_budget), so the reason stays stable tick to tick and can escalate.
	//
	// EVERY REMAINING SKIP IS THE ENGINE BEING HELD BACK FROM WORK IT WANTED TO DO, which is
	// what makes blocking the right verdict for all of them. There used to be an exception —
	// "disabled", the operator's own switch — and it is gone because the switch no longer skips
	// the tick: a spend-paused tick still marks the frontier and reads gates, reports that work
	// in Discovered and GatesRead, and is graded on it like any other. Reading it as skipped
	// would file a tick that discovered twenty systems as idle.
	stallReasonExpansionSkippedPrefix = "expansion_"
)

// sensingTickTally is everything one parked-sensing tick accomplished, as the reconcile already
// holds it. Passing the tallies rather than re-reading anything keeps the verdict a pure
// projection that can never influence the tick it describes.
type sensingTickTally struct {
	cutover    int
	screened   int
	adopted    int
	dispatched int
	rotation   int
	reap       parkedsensing.ReapReport
	buy        parkedsensing.BuyReport
	place      parkedsensing.PlacementReport
	expand     parkedsensing.ExpandReport
	failures   int
}

// anyEffect reports whether the tick moved ANYTHING: a system screened, a hull adopted, flown,
// bought or re-tasked, a claim reaped, a placement advanced, a frontier system discovered, a warp
// dispatched. The rotation size is deliberately NOT an effect — a steady rotation is what an idle
// tick looks like.
func (t sensingTickTally) anyEffect() bool {
	return t.cutover > 0 || t.screened > 0 || t.adopted > 0 || t.dispatched > 0 ||
		t.reap.Reaped > 0 ||
		t.buy.Bought > 0 || t.buy.Reused > 0 || t.buy.Footholds > 0 || t.buy.Queued > 0 ||
		t.place.Actions > 0 ||
		t.expand.Actions > 0 || t.expand.Discovered > 0 || t.expand.OffGateWarped > 0
}

// buyWedged reports the drain trying and failing for a reason that is not a money guard. CapHeld
// (at the probe cap) and FloorHeld (below the buy floor) are CORRECT refusals — the engine has
// nothing it should do — so they read as idle, not as a stall. RULINGS #4 is untouched either
// way: this reads the drain's report, it does not gate the drain.
func (t sensingTickTally) buyWedged() bool {
	if t.buy.Attempts <= 0 || t.buy.CapHeld || t.buy.FloorHeld {
		return false
	}
	return t.buy.Bought == 0 && t.buy.Reused == 0 && t.buy.Footholds == 0
}

// sensingTickVerdict maps one tick's tallies to its verdict.
//
// PROGRESS OUTRANKS FAILURE, deliberately. A tick that screened four systems and had one engine
// error still moved the fleet forward; treating it as blocked would page on a chronically noisy
// port while everything worked, and an alarm that fires while the fleet is healthy is the noise
// that made the original signals unreadable.
func sensingTickVerdict(t sensingTickTally) health.TickOutcome {
	if t.anyEffect() {
		return health.TickProgress()
	}
	if t.failures > 0 {
		return health.TickBlocked(stallReasonEngineFailure, fmt.Sprintf("%d engine failure(s) and nothing accomplished this tick", t.failures))
	}
	if t.buyWedged() {
		return health.TickBlocked(stallReasonBuyRefused, fmt.Sprintf("the drain made %d attempt(s) and bought nothing, with neither the probe cap nor the buy floor holding", t.buy.Attempts))
	}
	return health.TickIdle()
}

// expansionStallVerdict maps one off-gate/expansion pass to its verdict.
//
// The off-gate no-target case is checked AFTER progress for the same reason failures are: a
// frontier that is still charting is not sealed, whatever the warp selector did. It is checked
// BEFORE idle because "demand raised, no target found" is the exact shape that reads as a fully
// charted galaxy while a jump gate sits unread.
func expansionStallVerdict(rep parkedsensing.ExpandReport, err error) health.TickOutcome {
	if err != nil {
		return health.TickBlocked(stallReasonExpansionError, err.Error())
	}
	if rep.Skipped != "" {
		return health.TickBlocked(health.StallReason(stallReasonExpansionSkippedPrefix+rep.Skipped),
			fmt.Sprintf("the expansion pass was held by its %s gate", rep.Skipped))
	}
	if rep.Actions > 0 || rep.Discovered > 0 || rep.OffGateWarped > 0 || rep.SeedsRequested > 0 || rep.SeedsClaimed > 0 || rep.MarketsFound > 0 {
		return health.TickProgress()
	}
	if rep.OffGateDemanded && rep.OffGateTarget == "" {
		return health.TickBlocked(stallReasonOffGateNoTarget,
			"the gate-reachable frontier is exhausted and NO warp target could be selected — the fleet is sealed in the systems it already holds, and every layer reports this as '0 discovered'")
	}
	return health.TickIdle()
}

// observeStall reports one pass's verdict to the escalator. Nil-safe: an unwired observer simply
// reports nothing, because observability is never a precondition for running a tick.
func (h *RunProbeSensingCoordinatorHandler) observeStall(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, coordinator string, outcome health.TickOutcome) {
	if h.stall == nil {
		return
	}
	h.stall.Observe(ctx, health.StallKey{
		Coordinator: coordinator,
		ContainerID: cmd.ContainerID,
		PlayerID:    cmd.PlayerID.Value(),
	}, outcome)
}
