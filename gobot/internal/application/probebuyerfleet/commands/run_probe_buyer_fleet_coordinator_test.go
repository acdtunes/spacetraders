package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- port doubles ----------------------------------------------------------
//
// The coordinator is exercised through its driving port (reconcileOnce) with doubles at its four
// driven-port boundaries: the guarded buy engine, the positioner, the fleet roster/tagger, and the
// yard finder. Observable outcomes only — HOW MANY buys are driven and with what supply/target,
// WHICH hull is recruited, WHEN the positioner is invoked — never the coordinator's internals. The
// real guard stack (25%/floor/cap/ceiling) has its own probebuy tests; the real dedicated-hull buy
// path has its own expansion adapter tests. Here we pin the ORCHESTRATION.

type buyCall struct {
	demand int
	supply int
	dryRun bool
	target probebuy.ProbeTarget
}

type fakeBuyer struct {
	bought bool // what MaybeBuy reports for Bought (true = guards passed + bought)
	calls  []buyCall
}

func (f *fakeBuyer) MaybeBuy(_ context.Context, _ shared.PlayerID, demand, supply int, dryRun bool, target probebuy.ProbeTarget) probebuy.Outcome {
	f.calls = append(f.calls, buyCall{demand: demand, supply: supply, dryRun: dryRun, target: target})
	return probebuy.Outcome{Bought: f.bought, Reason: "fake"}
}

type fakePositioner struct {
	calls      int
	dispatched bool
	err        error
}

func (f *fakePositioner) PositionProbeBuyer(_ context.Context, _ shared.PlayerID) (bool, error) {
	f.calls++
	return f.dispatched, f.err
}

type assignCall struct{ symbol, fleet string }

type fakeFleetRepo struct {
	ships    []*navigation.Ship
	err      error
	assigned []assignCall
	reads    int // FindAllByPlayer calls — the pre-EXPANSION inertness pin (zero work, not just zero buys)
}

func (f *fakeFleetRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	f.reads++
	return f.ships, f.err
}

func (f *fakeFleetRepo) AssignFleet(_ context.Context, symbol, fleet string, _ shared.PlayerID) error {
	f.assigned = append(f.assigned, assignCall{symbol: symbol, fleet: fleet})
	for _, s := range f.ships {
		if s.ShipSymbol() == symbol {
			s.SetDedicatedFleet(fleet) // the tag takes effect, so a re-derive counts it (restart-safe)
		}
	}
	return nil
}

type fakeYardFinder struct {
	bySystem map[string][]shipyardQueries.YardCandidate
	queries  [][]string
}

func (f *fakeYardFinder) NearestYardsSelling(_ context.Context, _ int, _ []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error) {
	f.queries = append(f.queries, fromSystems)
	if len(fromSystems) == 0 {
		return nil, nil
	}
	return f.bySystem[fromSystems[0]], nil
}

// fakePhaseReader doubles the bootstrap-EXPANSION phase port (sp-f3mcc): inExpansion is what the
// derived-phase read reports; err models an unreadable phase (the fail-closed input).
type fakePhaseReader struct {
	inExpansion bool
	err         error
	calls       int
}

func (f *fakePhaseReader) InExpansion(_ context.Context, _ shared.PlayerID) (bool, error) {
	f.calls++
	return f.inExpansion, f.err
}

// captureLogger records log lines so the phase-gate tests can pin the honest no-work reason
// (a deferral is surfaced, never a silent stall).
type captureLogger struct {
	lines []capturedLine
}

type capturedLine struct {
	level  string
	msg    string
	action string
}

func (l *captureLogger) Log(level, message string, metadata map[string]interface{}) {
	action := ""
	if metadata != nil {
		if a, ok := metadata["action"].(string); ok {
			action = a
		}
	}
	l.lines = append(l.lines, capturedLine{level: level, msg: message, action: action})
}

func (l *captureLogger) find(action string) (capturedLine, bool) {
	for _, ln := range l.lines {
		if ln.action == action {
			return ln, true
		}
	}
	return capturedLine{}, false
}

// ---- ship builders ---------------------------------------------------------

func newSat(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	if fleet != "" {
		ship.SetDedicatedFleet(fleet)
	}
	return ship
}

func newFrigate(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30, "FRAME_FRIGATE", "COMMAND", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func pid() shared.PlayerID { return shared.MustNewPlayerID(1) }

// newHandler wires a handler whose bootstrap phase reads EXPANSION, so the orchestration tests
// exercise the armed (buying) coordinator — they double as the "buys proceed under EXPANSION,
// composing with the existing guard stack" pins (sp-f3mcc). The phase-gate tests below wire
// pre-EXPANSION/unreadable readers instead.
func newHandler(repo fleetRepository, buyer guardedBuyer, pos buyerPositioner, finder yardReachability) *RunProbeBuyerFleetCoordinatorHandler {
	return NewRunProbeBuyerFleetCoordinatorHandler(repo, buyer, pos, finder, &fakePhaseReader{inExpansion: true}, nil)
}

// ---- tests -----------------------------------------------------------------

// CORE (sp-f082y): K probe-buyer hulls already stationed, NO idle undedicated hull, fleet below the
// cap → the coordinator drives exactly K in-place buys this tick and the supply it hands each buy
// INCREMENTS (159→160→…), proving the fleet grows by K. Every buy carries the container as the
// single-writer claim owner and the immutable price ceiling. No positioning when K buys land.
func TestReconcile_KStationedBuyers_DoKInPlaceBuysPerTick(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet), // stationed dedicated buyers
		newSat(t, "PB-2", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "FREE-1", "X1-AA-F1", ""), // one undedicated satellite (makes satCount=3)
	}}
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	h := newHandler(repo, buyer, pos, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 2, "exactly K in-place buys this tick")
	require.Equal(t, 3, buyer.calls[0].supply, "first buy sees the live satellite count")
	require.Equal(t, 4, buyer.calls[1].supply, "the second buy sees the incremented supply — the fleet is growing")
	require.Equal(t, 5, buyer.calls[0].demand, "demand is the fleet cap (the binding growth target)")
	require.Equal(t, "pb-cid", buyer.calls[0].target.ClaimOwnerContainerID, "the buy claim is owned by this container (RULINGS #3)")
	require.Equal(t, probeBuyerMaxProbePrice, buyer.calls[0].target.MaxProbePriceCredits, "every buy carries the immutable price ceiling (RULINGS #4)")
	require.False(t, buyer.calls[0].dryRun, "armed buy")
	require.Zero(t, pos.calls, "no positioning needed when all K buys land")
	require.Empty(t, repo.assigned, "no recruitment when K buyers already exist")
}

// CAP (RULINGS #4): at/over the cap the coordinator buys nothing AND recruits nothing — the fleet is
// full. The mutation guard: drop the cap gate and the buyer/recruiter fire.
func TestReconcile_AtCap_NoBuyNoRecruit(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "S-2", "X1-AA-F2", ""),
		newSat(t, "S-3", "X1-AA-F3", ""),
		newSat(t, "S-4", "X1-AA-F4", ""),
		newSat(t, "S-5", "X1-AA-F5", ""),
	}}
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	finder := &fakeYardFinder{bySystem: map[string][]shipyardQueries.YardCandidate{"X1-AA": {yardCand("X1-AA-YD", "X1-AA", 0, 20_000)}}}
	h := newHandler(repo, buyer, pos, finder)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Empty(t, buyer.calls, "no buy at the cap")
	require.Empty(t, repo.assigned, "no recruit at the cap")
	require.Zero(t, pos.calls, "no positioning at the cap")
}

// NEAR-CAP: the coordinator's OWN loop gate bounds a tick to (cap − satCount) buys even when K is
// larger, and does not over-position once the cap is reached.
func TestReconcile_NearCap_BuysOnlyUpToCap(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "PB-2", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "S-3", "X1-AA-F3", ""),
		newSat(t, "S-4", "X1-AA-F4", ""),
	}}
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	h := newHandler(repo, buyer, pos, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 1, "satCount 4, cap 5 → only ONE buy fits under the cap, though K=2")
	require.Equal(t, 4, buyer.calls[0].supply)
	require.Zero(t, pos.calls, "reaching the cap is not a shortfall — do not position")
}

// RECRUITMENT (sp-f082y): fewer than K buyers and below cap → recruit ONE, tagging the nearest
// REACHABLE idle undedicated satellite. A hull whose system reaches no probe-yard is passed over; a
// contract-dedicated hull and the command frigate are never poached (RULINGS #7).
func TestReconcile_RecruitsNearestReachableUndedicatedSatellite(t *testing.T) {
	far := newSat(t, "FARSCOUT-7", "X1-FAR-F1", "")       // undedicated but its system reaches no yard
	near := newSat(t, "NEARSCOUT-3", "X1-AA-F1", "")      // undedicated AND reachable → the recruit
	hauler := newSat(t, "HAUL-9", "X1-AA-F2", "contract") // dedicated elsewhere → never poached
	frigate := newFrigate(t, "CMD-1", "X1-AA-F3")         // the command frigate → never poached
	repo := &fakeFleetRepo{ships: []*navigation.Ship{far, hauler, frigate, near}}
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	finder := &fakeYardFinder{bySystem: map[string][]shipyardQueries.YardCandidate{
		"X1-AA": {yardCand("X1-AA-YD", "X1-AA", 0, 20_000)}, // only NEAR-1's system reaches a yard
	}}
	h := newHandler(repo, buyer, pos, finder)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 200}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, repo.assigned, 1, "exactly one recruit per tick")
	require.Equal(t, assignCall{symbol: "NEARSCOUT-3", fleet: ProbeBuyerFleet}, repo.assigned[0], "the nearest REACHABLE undedicated satellite is tagged")
}

// ISOLATION (RULINGS #7): with only a contract hull and the frigate available, recruitment tags
// NEITHER — a dedicated hull and the command frigate are off-limits, and a probe whose symbol marks
// it a command hull ("*-1") is off-limits too.
func TestReconcile_NeverRecruitsDedicatedOrCommandHull(t *testing.T) {
	hauler := newSat(t, "HAUL-9", "X1-AA-F1", "long-haul")    // dedicated elsewhere
	frigate := newFrigate(t, "CMD-1", "X1-AA-F2")             // command by role
	probeNamedCommand := newSat(t, "AGENT-1", "X1-AA-F3", "") // undedicated satellite, but "-1" = command hull
	repo := &fakeFleetRepo{ships: []*navigation.Ship{hauler, frigate, probeNamedCommand}}
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	finder := &fakeYardFinder{bySystem: map[string][]shipyardQueries.YardCandidate{
		"X1-AA": {yardCand("X1-AA-YD", "X1-AA", 0, 20_000)},
	}}
	h := newHandler(repo, buyer, pos, finder)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 200}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Empty(t, repo.assigned, "no eligible recruit: dedicated hulls + the command frigate are never poached")
}

// ROTATION / money-guard response: when a buy is refused (a guard blocked it — e.g. the yard's price
// climbed over the ceiling), the coordinator drives no buy and STATIONS/rotates a buyer so the next
// tick can price a fresh yard. It never forces a buy past the guard (RULINGS #4).
func TestReconcile_BuyBlocked_StationsBuyerForNextTick(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "PB-2", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "S-3", "X1-AA-F3", ""),
	}}
	buyer := &fakeBuyer{bought: false} // every guard-gated buy is refused this tick
	pos := &fakePositioner{dispatched: true}
	h := newHandler(repo, buyer, pos, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 1, "one buy attempt, refused by the guard, then stop attempting this tick")
	require.Equal(t, 1, pos.calls, "a refused buy triggers a station/rotate so a fresh yard prices next tick")
}

// RECOVERY-SAFE (RULINGS #2): the coordinator holds NO in-memory state — a FRESH handler (simulating
// a daemon restart) given only the persisted fleet re-derives its buyers from the dedicated tag and
// behaves identically: it drives K buys and recruits nobody (the K buyers are already tagged).
func TestReconcile_RestartReDerivesBuyersFromPersistedTag(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "PB-2", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "S-3", "X1-AA-F3", ""),
	}}
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	// A brand-new handler instance — no state carried over from any prior run.
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	fresh := newHandler(repo, buyer, pos, nil)

	require.NoError(t, fresh.reconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 2, "a restarted coordinator re-derives its 2 buyers from the persisted tag and buys")
	require.Empty(t, repo.assigned, "no re-recruit: the persisted dedicated tag already satisfies K (no in-memory count)")
}

func yardCand(waypoint, system string, hops, price int) shipyardQueries.YardCandidate {
	return shipyardQueries.YardCandidate{
		WaypointSymbol: waypoint,
		SystemSymbol:   system,
		ShipType:       probeShipType,
		Hops:           hops,
		PurchasePrice:  price,
	}
}

// ---- sp-f3mcc: the Admiral EXPANSION phase gate ----------------------------
//
// "WE SHOULD NOT BE BUYING PROBES IN THE GATE PHASE... not in the gate, not in data, not in
// income!" (Admiral, 2026-07-24). Outside EXPANSION (the bootstrap-derived gate-built phase,
// sp-feiy7) the coordinator is FULLY INERT — zero purchases, zero recruitment, zero positioning,
// not even a fleet read — and it surfaces the honest no-work reason. DATA/INCOME/GATE all read
// pre-EXPANSION at this boundary (the derived phase is EXPANSION iff the home jump gate is built),
// so one pre-EXPANSION pin covers all three. Fail-closed: an unreadable phase never defaults a
// spender open (RULINGS #4).

// expansionGateFleet is a fleet shaped so EVERY action would fire if the gate were open: stationed
// buyers ready to buy, an idle undedicated satellite ready to recruit, all far below the cap.
func expansionGateFleet(t *testing.T) *fakeFleetRepo {
	t.Helper()
	return &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "FREE-1", "X1-AA-F1", ""),
	}}
}

// Pre-EXPANSION (the world still in DATA/INCOME/GATE — the home jump gate not yet built): the tick
// performs NOTHING — no buy reaches even the guard stack, no recruit, no positioning, no fleet
// read — and the deferral is logged with the honest reason. This is the staging incident pin: the
// coordinator that bought ~23 probes during GATE must now sit inert until EXPANSION.
func TestReconcile_PreExpansion_FullyInert_AndLogsHonestReason(t *testing.T) {
	repo := expansionGateFleet(t)
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	phase := &fakePhaseReader{inExpansion: false}
	h := NewRunProbeBuyerFleetCoordinatorHandler(repo, buyer, pos, nil, phase, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 200}

	log := &captureLogger{}
	require.NoError(t, h.reconcileOnce(common.WithLogger(context.Background(), log), cmd))

	require.Empty(t, buyer.calls, "pre-EXPANSION: no buy may even reach the guard stack")
	require.Empty(t, repo.assigned, "pre-EXPANSION: no recruitment (no idle satellite is pulled off manning)")
	require.Zero(t, pos.calls, "pre-EXPANSION: no stationing/positioning (zero API churn)")
	require.Zero(t, repo.reads, "pre-EXPANSION: fully inert — not even a fleet read")
	line, ok := log.find("probe_buyer_phase_deferred")
	require.True(t, ok, "the deferral must surface an honest no-work reason, never a silent stall")
	require.Contains(t, line.msg, "EXPANSION", "the reason names the phase gate")
}

// Unreadable phase (reader error): FAIL-CLOSED — a spender guard never defaults open. Zero actions,
// and the fail-closed reason is surfaced.
func TestReconcile_PhaseUnreadable_FailsClosed_NoActions(t *testing.T) {
	repo := expansionGateFleet(t)
	buyer := &fakeBuyer{bought: true}
	pos := &fakePositioner{}
	phase := &fakePhaseReader{inExpansion: true, err: contextDeadlineErr{}} // reader errors trump its bool
	h := NewRunProbeBuyerFleetCoordinatorHandler(repo, buyer, pos, nil, phase, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 200}

	log := &captureLogger{}
	require.NoError(t, h.reconcileOnce(common.WithLogger(context.Background(), log), cmd))

	require.Empty(t, buyer.calls, "unreadable phase: fail-closed, no buys")
	require.Empty(t, repo.assigned, "unreadable phase: no recruitment")
	require.Zero(t, pos.calls, "unreadable phase: no positioning")
	_, ok := log.find("probe_buyer_phase_unreadable")
	require.True(t, ok, "the fail-closed hold must be surfaced")
}

// A NIL phase reader (mis-wire) is the same fail-closed hold: the spender cannot run without a
// phase source. Surfaced loudly so a wiring regression is visible, never a silent default-open.
func TestReconcile_NilPhaseReader_FailsClosed(t *testing.T) {
	repo := expansionGateFleet(t)
	buyer := &fakeBuyer{bought: true}
	h := NewRunProbeBuyerFleetCoordinatorHandler(repo, buyer, &fakePositioner{}, nil, nil, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 200}

	log := &captureLogger{}
	require.NoError(t, h.reconcileOnce(common.WithLogger(context.Background(), log), cmd))

	require.Empty(t, buyer.calls, "no phase reader wired: fail-closed, no buys")
	_, ok := log.find("probe_buyer_phase_unreadable")
	require.True(t, ok, "the nil-reader hold must be surfaced")
}

// EXPANSION: the gate opens and buying proceeds EXACTLY as before — through the unchanged reused
// guard stack (25% treasury / working-capital floor / fleet cap / price ceiling all still consulted
// inside MaybeBuy; a guard refusal still refuses). The phase gate composes with — never replaces —
// the money guards. (The full orchestration behaviors are pinned by the tests above, which all run
// with an EXPANSION-true reader via newHandler.)
func TestReconcile_Expansion_BuysProceedBehindExistingGuards(t *testing.T) {
	repo := &fakeFleetRepo{ships: []*navigation.Ship{
		newSat(t, "PB-1", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "PB-2", "X1-AA-YD", ProbeBuyerFleet),
		newSat(t, "FREE-1", "X1-AA-F1", ""),
	}}
	buyer := &fakeBuyer{bought: true}
	phase := &fakePhaseReader{inExpansion: true}
	h := NewRunProbeBuyerFleetCoordinatorHandler(repo, buyer, &fakePositioner{}, nil, phase, nil)
	cmd := &RunProbeBuyerFleetCoordinatorCommand{PlayerID: pid(), ContainerID: "pb-cid", ProbeBuyerCount: 2, MaxProbeFleet: 5}

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Equal(t, 1, phase.calls, "the phase is read once per tick")
	require.Len(t, buyer.calls, 2, "EXPANSION: the K in-place buys proceed through the reused guard stack")
	require.Equal(t, probeBuyerMaxProbePrice, buyer.calls[0].target.MaxProbePriceCredits, "the money-guard stack is untouched (RULINGS #4)")
}

// contextDeadlineErr is a tiny named error for the unreadable-phase double.
type contextDeadlineErr struct{}

func (contextDeadlineErr) Error() string { return "bootstrap phase read timed out" }
