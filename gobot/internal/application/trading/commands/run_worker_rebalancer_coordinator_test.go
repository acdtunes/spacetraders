package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ---- fakes (rebalancer-scoped; the package's shared tradeCaptureLogger / clockAt /
// baseTime are reused) --------------------------------------------------------

type rebalancerClaimRecord struct {
	ship      string
	container string
	operation string
}

// fakeRebalancerShipRepo is a ShipRepository over a fixed roster. Idle/assignment state is
// derived from the entities themselves, so a ForceRelease (reclaim) makes a hull idle for
// the very next FindIdleLightHaulers call, exactly like the DB round-trip.
type fakeRebalancerShipRepo struct {
	navigation.ShipRepository
	ships    []*navigation.Ship
	clock    shared.Clock
	claims   []rebalancerClaimRecord
	releases []string
}

func (r *fakeRebalancerShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *fakeRebalancerShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	for _, s := range r.ships {
		if s.ShipSymbol() == symbol {
			return s, nil
		}
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

func (r *fakeRebalancerShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var matched []*navigation.Ship
	for _, s := range r.ships {
		if s.ContainerID() == containerID {
			matched = append(matched, s)
		}
	}
	return matched, nil
}

func (r *fakeRebalancerShipRepo) ClaimShip(_ context.Context, shipSymbol, containerID string, _ shared.PlayerID, operation string) error {
	for _, s := range r.ships {
		if s.ShipSymbol() != shipSymbol {
			continue
		}
		if fleet := s.DedicatedFleet(); fleet != "" && fleet != operation {
			return fmt.Errorf("ship %s dedicated to fleet %q", shipSymbol, fleet)
		}
		if err := s.AssignToContainer(containerID, r.clock); err != nil {
			return err
		}
		r.claims = append(r.claims, rebalancerClaimRecord{ship: shipSymbol, container: containerID, operation: operation})
		return nil
	}
	return fmt.Errorf("ship %s not found", shipSymbol)
}

func (r *fakeRebalancerShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if ship.IsIdle() {
		r.releases = append(r.releases, ship.ShipSymbol())
	}
	return nil
}

// SaveWithRetry mirrors the real repository's non-conflict path (find → mutate →
// save) so the migrated ferry reclaim / release-hull sites (sp-wa7c) exercise their
// production closures while still routing through Save's released-hull tracking.
func (r *fakeRebalancerShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	sh, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(sh)
	if err != nil {
		return sh, false, err
	}
	if !changed {
		return sh, false, nil
	}
	if err := r.Save(ctx, sh); err != nil {
		return sh, false, err
	}
	return sh, true, nil
}

// erroringShipRepo fails FindAllByPlayer — the fail-closed ship-read guard.
type erroringShipRepo struct{ navigation.ShipRepository }

func (erroringShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, fmt.Errorf("db unavailable")
}

// fakeRebalancerContainerQuery returns configured factory + ferry container rows. It
// ignores `since` (returns the configured ferries verbatim), mirroring the real adapter's
// contract that RUNNING ferries are always included regardless of age.
type fakeRebalancerContainerQuery struct {
	factories  []ActiveFactoryContainer
	ferries    []FerryContainer
	factoryErr error
	ferriesErr error
}

func (q *fakeRebalancerContainerQuery) ActiveFactoryContainers(_ context.Context, _ int) ([]ActiveFactoryContainer, error) {
	return q.factories, q.factoryErr
}

func (q *fakeRebalancerContainerQuery) RecentFerries(_ context.Context, _ int, _ time.Time) ([]FerryContainer, error) {
	return q.ferries, q.ferriesErr
}

// fakeRebalancerMarketProvider returns one marketplace waypoint per system; systems in
// emptySystems return none (fail-closed destination).
type fakeRebalancerMarketProvider struct {
	emptySystems map[string]bool
}

func (m *fakeRebalancerMarketProvider) ListBySystemWithTrait(_ context.Context, systemSymbol, _ string) ([]*shared.Waypoint, error) {
	if m.emptySystems[systemSymbol] {
		return nil, nil
	}
	wp, err := shared.NewWaypoint(systemSymbol+"-M1", 0, 0)
	if err != nil {
		return nil, err
	}
	return []*shared.Waypoint{wp}, nil
}

// fakeHopGraph resolves jump-hop distances from a fixed "FROM->TO" → hop-count table. A
// missing entry is UNROUTABLE (Path errors) — the coordinator skips that source
// (fail-closed). Named distinctly from the package's single-path fakeGateGraph, and
// implements the reused GateGraph (Path + Routable).
type fakeHopGraph struct {
	hops map[string]int
}

func (g *fakeHopGraph) Path(_ context.Context, from, to string, _ int) ([]string, error) {
	n, ok := g.hops[from+"->"+to]
	if !ok {
		return nil, fmt.Errorf("no jump-gate route from %s to %s", from, to)
	}
	path := make([]string, n+1)
	for i := range path {
		path[i] = fmt.Sprintf("%s#%d", from, i)
	}
	path[0], path[n] = from, to
	return path, nil
}

// RepositionPath mirrors Path — the worker rebalancer ferries via strict travel, so this
// exists only to satisfy the GateGraph interface (sp-8k9m); the bound is ignored here.
func (g *fakeHopGraph) RepositionPath(ctx context.Context, from, to string, _ int) ([]string, error) {
	return g.Path(ctx, from, to, 0)
}

func (g *fakeHopGraph) Routable(_ context.Context, from, to string, _ int) (bool, error) {
	_, ok := g.hops[from+"->"+to]
	return ok, nil
}

// Connections is inert here (the rebalancer exercises multi-hop Path/Routable, not the
// reposition neighbor scan) — it exists only to satisfy the GateGraph interface (sp-1ki5).
func (g *fakeHopGraph) Connections(_ context.Context, _ string, _ int) ([]system.GateEdge, error) {
	return nil, nil
}

// fakeRebalancerDaemonClient records the ferry worker lifecycle calls.
type fakeRebalancerDaemonClient struct {
	daemon.DaemonClient
	ferried    []string              // container IDs persisted (worker_ferry workers)
	ferryCmds  []*WorkerFerryCommand // the captured *WorkerFerryCommand per persist, same order
	started    []string
	stopped    []string
	persistErr error
	startErr   error
}

func (c *fakeRebalancerDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, containerID string, _ uint, command interface{}) error {
	if c.persistErr != nil {
		return c.persistErr
	}
	if kind != daemon.ContainerKindWorkerFerry {
		return fmt.Errorf("unexpected kind %q", kind)
	}
	c.ferried = append(c.ferried, containerID)
	if fc, ok := command.(*WorkerFerryCommand); ok {
		c.ferryCmds = append(c.ferryCmds, fc)
	}
	return nil
}

func (c *fakeRebalancerDaemonClient) StartContainer(_ context.Context, _ daemon.ContainerKind, containerID string) error {
	if c.startErr != nil {
		return c.startErr
	}
	c.started = append(c.started, containerID)
	return nil
}

func (c *fakeRebalancerDaemonClient) StopContainer(_ context.Context, containerID string) error {
	c.stopped = append(c.stopped, containerID)
	return nil
}

// ---- ship builders ---------------------------------------------------------

// rebalancerLight builds an idle, undedicated light-hauler (role HAULER, 80 cargo) in
// orbit at waypoint — the ferry source/candidate shape.
func rebalancerLight(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(80, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 80, cargo, 30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

// ---- test wiring -----------------------------------------------------------

func newTestRebalancerHandler(
	shipRepo *fakeRebalancerShipRepo,
	daemonClient *fakeRebalancerDaemonClient,
	cq *fakeRebalancerContainerQuery,
	mp *fakeRebalancerMarketProvider,
	graph *fakeHopGraph,
	clock shared.Clock,
) *RunWorkerRebalancerCoordinatorHandler {
	h := &RunWorkerRebalancerCoordinatorHandler{
		shipRepo:       shipRepo,
		daemonClient:   daemonClient,
		containerQuery: cq,
		marketProvider: mp,
		clock:          clock,
	}
	if graph != nil {
		h.gateGraph = graph
	}
	return h
}

func rebalancerTestCmd() *RunWorkerRebalancerCoordinatorCommand {
	return &RunWorkerRebalancerCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "worker_rebalancer_coordinator-1",
		Enabled:     true,
	}
}

// ferryDefaultMarket returns a market provider that serves every system.
func ferryDefaultMarket() *fakeRebalancerMarketProvider {
	return &fakeRebalancerMarketProvider{emptySystems: map[string]bool{}}
}

// ---- tests: vacancy detection ----------------------------------------------

// The canonical acceptance case: DP51 has 1 factory running 20m and zero in-system
// lights, and a fleet source holds 2 idle undedicated lights routable 1 hop away → the
// coordinator ferries exactly one light to DP51 through the guarded claim path.
func TestRebalancer_Vacancy_AllConditionsMet_Ferries(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	ferried, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried, "exactly one ferry is dispatched for the single vacancy")
	require.Len(t, daemonClient.ferried, 1, "one worker_ferry container is persisted")
	require.Len(t, daemonClient.started, 1, "the ferry is started")
	require.Len(t, shipRepo.claims, 1, "the chosen light is claimed for the ferry")
	require.Equal(t, "SRC-A", shipRepo.claims[0].ship, "the lowest-symbol light of the source is chosen (deterministic)")
	require.Equal(t, workerFerryOperation, shipRepo.claims[0].operation, "claimed under the worker_ferry op (poach-guarded, RULINGS #7)")
	require.Equal(t, daemonClient.ferried[0], shipRepo.claims[0].container, "the claim binds the light to the ferry container")
	require.Len(t, daemonClient.ferryCmds, 1)
	require.Equal(t, "X1-DP51-M1", daemonClient.ferryCmds[0].DestinationWaypoint, "the destination is a marketplace in the vacancy system")
	require.Equal(t, "worker_rebalancer_coordinator-1", daemonClient.ferryCmds[0].CoordinatorID, "the ferry carries the coordinator id for restart recovery")
	require.True(t, logger.loggedContaining("Ferrying SRC-A", "X1-DP51", "1 hop"), "the dispatch logs the honest ferry decision with hop count")
}

// Condition 2 (persisted duration): a factory that started <15m ago is NOT yet a vacancy —
// a just-launched/restarted factory mid-first-cycle is exempt (RULINGS #2).
func TestRebalancer_Vacancy_FactoryTooYoung_NotYet(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-5 * time.Minute)}, // < 15m default
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "a factory younger than vacancy_min is not yet a vacancy")
	require.Empty(t, shipRepo.claims, "no light is claimed")
}

// Condition 3 (no self-heal): a system with an idle in-system light mans itself — never a
// vacancy, so no cross-system ferry.
func TestRebalancer_Vacancy_IdleInSystemLight_SelfHeals(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "LOCAL-X", "X1-DP51-A1"), // idle, in-system → self-heals (non "-1" symbol: not the command hull)
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "a system with an idle in-system light is not a vacancy")
	require.Empty(t, shipRepo.claims)
}

// Condition 4 (demand > supply, the anti-hub guard): a well-supplied system (in-system
// lights >= factory count, e.g. KA42) is adequately manned and NOT a vacancy — even with
// no light idle right now.
func TestRebalancer_Vacancy_WellSupplied_NotVacancy(t *testing.T) {
	clock := clockAt(0)
	// KA42: two undedicated lights physically in-system (busy, assigned elsewhere), one
	// factory → supply(2) >= factories(1). Not a vacancy.
	busyA := rebalancerLight(t, "KA-X", "X1-KA42-A1")
	require.NoError(t, busyA.AssignToContainer("some-other-op-KA-X", clock))
	busyB := rebalancerLight(t, "KA-Y", "X1-KA42-A2")
	require.NoError(t, busyB.AssignToContainer("some-other-op-KA-Y", clock))
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		busyA, busyB,
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-KA42", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-KA42": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "a system supplied with lights >= its factory count is not a vacancy (anti-hub guard)")
	require.Empty(t, shipRepo.claims)
}

// Condition 4 boundary (GQ92: 1 busy in-system light, 3 factory chains): supply(1) <
// factories(3) and no idle in-system light → a vacancy despite holding one light.
func TestRebalancer_Vacancy_UndersuppliedMultiChain_IsVacancy(t *testing.T) {
	clock := clockAt(0)
	busy := rebalancerLight(t, "GQ-X", "X1-GQ92-A1")
	require.NoError(t, busy.AssignToContainer("some-other-op-GQ-X", clock)) // in-system but busy
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		busy,
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-GQ92", StartedAt: baseTime.Add(-20 * time.Minute)},
		{SystemSymbol: "X1-GQ92", StartedAt: baseTime.Add(-18 * time.Minute)},
		{SystemSymbol: "X1-GQ92", StartedAt: baseTime.Add(-16 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-GQ92": 2}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried, "an under-supplied multi-chain system (1 light < 3 factories, none idle) is a vacancy")
	require.Equal(t, "X1-GQ92", shared.ExtractSystemSymbol(daemonClient.ferryCmds[0].DestinationWaypoint))
}

// Condition 1: no active factory container → no vacancy anywhere → no ferry.
func TestRebalancer_Vacancy_NoActiveFactory_NoVacancy(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{} // no factories
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried)
	require.Empty(t, shipRepo.claims)
}

// ---- tests: source eligibility ---------------------------------------------

// A source below source_min_idle (only 1 idle light) cannot donate — the vacancy parks
// fail-closed rather than stripping a lone source.
func TestRebalancer_Source_BelowMinIdle_ParksFailClosed(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"), // only ONE idle light in the source
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	ferried, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "a source with only 1 idle light is below source_min_idle and never stripped")
	require.Empty(t, shipRepo.claims)
	require.True(t, logger.loggedContaining("X1-DP51", "no eligible"), "the fail-closed park reason is logged")
}

// Dedicated and captain-reserved hulls are never counted as source candidates (RULINGS #7):
// a source with 2 idle undedicated lights + 1 dedicated + 1 captain-reserved ferries one of
// the two idle undedicated lights, never the pinned/reserved ones.
func TestRebalancer_Source_ExcludesDedicatedAndReserved(t *testing.T) {
	clock := clockAt(0)
	dedicated := rebalancerLight(t, "SRC-DED", "X1-SRC-A3")
	dedicated.SetDedicatedFleet("trade") // pinned to another fleet — never poach
	reserved := rebalancerLight(t, "SRC-RES", "X1-SRC-A4")
	require.NoError(t, reserved.ReserveByCaptain("captain manual use", clock)) // reserved — never poach
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
		dedicated, reserved,
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried)
	require.Len(t, shipRepo.claims, 1)
	require.Equal(t, "SRC-A", shipRepo.claims[0].ship, "the deterministic sorted pick is SRC-A (the first idle undedicated unreserved light), never the dedicated/reserved hulls")
}

// Never strip a source below one idle: with two vacancies but a single 2-idle source, the
// first vacancy takes one light (leaving one), and the source is then ineligible for the
// second vacancy — exactly one ferry.
func TestRebalancer_Source_NeverStripsBelowOne(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
		{SystemSymbol: "X1-EQ77", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1, "X1-SRC->X1-EQ77": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried, "only one ferry — the lone 2-idle source is not stripped below one for the second vacancy")
	require.Len(t, shipRepo.claims, 1)
}

// ---- tests: nearest-source-by-hops -----------------------------------------

// The FEWEST-hops source is chosen, not the lowest-symbol one, when both are eligible.
func TestRebalancer_Source_NearestByHops(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		// X1-AFAR sorts first but is 3 hops; X1-NEAR sorts later but is 1 hop.
		// (letter suffixes so none is mistaken for the "*-1" command hull)
		rebalancerLight(t, "AFAR-P", "X1-AFAR-A1"),
		rebalancerLight(t, "AFAR-Q", "X1-AFAR-A2"),
		rebalancerLight(t, "NEAR-P", "X1-NEAR-A1"),
		rebalancerLight(t, "NEAR-Q", "X1-NEAR-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{
		"X1-AFAR->X1-DP51": 3,
		"X1-NEAR->X1-DP51": 1,
	}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried)
	require.Equal(t, "NEAR-P", shipRepo.claims[0].ship, "the fewest-hops source is chosen, not the first-sorted")
}

// A source whose gate-graph Path errors is skipped (fail-closed); a farther but routable
// source still serves the vacancy.
func TestRebalancer_Source_UnroutableSkipped(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "ISO-P", "X1-ISO-A1"), // unroutable to DP51
		rebalancerLight(t, "ISO-Q", "X1-ISO-A2"),
		rebalancerLight(t, "OK-P", "X1-OK-A1"), // routable
		rebalancerLight(t, "OK-Q", "X1-OK-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-OK->X1-DP51": 4}} // ISO absent → unroutable
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried)
	require.Equal(t, "OK-P", shipRepo.claims[0].ship, "the unroutable source is skipped; the routable one serves the vacancy")
}

// No gate graph wired at all → ferrying is disabled (fail-closed park), never a blind move.
func TestRebalancer_NoGateGraph_ParksFailClosed(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), nil, clock) // no gate graph

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "no gate graph ⇒ no ferry (fail-closed)")
	require.Empty(t, shipRepo.claims)
}

// ---- tests: caps (DB-derived, restart-safe) --------------------------------

// At the concurrency cap (2 RUNNING ferries), no new ferry launches even with a live
// vacancy and an eligible source.
func TestRebalancer_ConcurrencyCap_AtCap_NoFerry(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{
		factories: []ActiveFactoryContainer{{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)}},
		ferries: []FerryContainer{
			{ID: "worker_ferry-x-1", DestinationWaypoint: "X1-OTHER-M1", Status: "RUNNING", StartedAt: baseTime.Add(-30 * time.Second)},
			{ID: "worker_ferry-y-2", DestinationWaypoint: "X1-OTHER2-M1", Status: "RUNNING", StartedAt: baseTime.Add(-30 * time.Second)},
		},
	}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	ferried, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "at the concurrency cap, no new ferry is dispatched")
	require.Empty(t, shipRepo.claims)
	require.True(t, logger.loggedContaining("max concurrent ferries"))
}

// The per-vacancy cooldown suppresses a NEW ferry to a system a ferry targeted recently —
// and a FRESH handler reading the same ferry rows makes the identical suppress decision
// (restart-safe: the clock is the container row's StartedAt, not in-memory state).
func TestRebalancer_Cooldown_SuppressesRecentTarget_RestartSafe(t *testing.T) {
	clock := clockAt(0)
	factoryRows := []ActiveFactoryContainer{{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)}}
	ferryRows := []FerryContainer{ // a ferry to DP51 started 100s ago (< 600s cooldown), already completed
		{ID: "worker_ferry-prev-1", DestinationWaypoint: "X1-DP51-M1", Status: "COMPLETED", StartedAt: baseTime.Add(-100 * time.Second)},
	}
	newCtx := func() (*fakeRebalancerShipRepo, *fakeRebalancerDaemonClient, *tradeCaptureLogger, context.Context, *RunWorkerRebalancerCoordinatorHandler) {
		shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
			rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
			rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
		}}
		cq := &fakeRebalancerContainerQuery{factories: factoryRows, ferries: ferryRows}
		daemonClient := &fakeRebalancerDaemonClient{}
		graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
		logger := &tradeCaptureLogger{}
		h := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
		return shipRepo, daemonClient, logger, common.WithLogger(context.Background(), logger), h
	}

	// Handler A.
	shipRepoA, _, loggerA, ctxA, handlerA := newCtx()
	ferriedA, _, errA := handlerA.reconcileOnce(ctxA, rebalancerTestCmd())
	require.NoError(t, errA)
	require.Zero(t, ferriedA, "a recent ferry to DP51 suppresses a new one (cooldown)")
	require.Empty(t, shipRepoA.claims)
	require.True(t, loggerA.loggedContaining("X1-DP51", "cooling down"))

	// Handler B — a brand-new struct (post-restart), same container rows → same decision.
	shipRepoB, _, _, ctxB, handlerB := newCtx()
	ferriedB, _, errB := handlerB.reconcileOnce(ctxB, rebalancerTestCmd())
	require.NoError(t, errB)
	require.Zero(t, ferriedB, "a fresh handler derives the SAME cooldown suppression from the DB rows (restart-safe)")
	require.Empty(t, shipRepoB.claims)
}

// A ferry to the vacancy system that started OUTSIDE the cooldown window does NOT suppress
// a new ferry — the cooldown is a genuine time window, not a permanent block.
func TestRebalancer_Cooldown_ExpiredAllowsFerry(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{
		factories: []ActiveFactoryContainer{{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)}},
		ferries:   []FerryContainer{{ID: "worker_ferry-old-1", DestinationWaypoint: "X1-DP51-M1", Status: "COMPLETED", StartedAt: baseTime.Add(-700 * time.Second)}}, // > 600s
	}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Equal(t, 1, ferried, "a ferry older than the cooldown window no longer suppresses")
}

// ---- tests: dispatch rollback (spawnFerry StartContainer failure) ----------

// spawnFerry rolls back cleanly when StartContainer fails mid-spawn (sp-sqh7): the
// persist → claim → start sequence has already persisted the container and claimed the hull,
// so a start failure must undo BOTH — releaseHull (ForceRelease) returns the claimed hull to
// the idle pool, and StopContainer tears down the persisted-but-unstarted container. Without
// the rollback the hull is stranded claimed to a dead container (the auditor's gap). The
// fixture's startErr injects the failure; the tick logs-and-skips (no tick error, zero
// ferried) rather than aborting the whole reconcile.
func TestRebalancer_SpawnFerry_StartFailure_ReleasesHullAndStopsContainer(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{startErr: fmt.Errorf("daemon refused StartContainer")}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	ferried, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err, "a per-ferry StartContainer failure is logged-and-skipped, not a tick error")
	require.Zero(t, ferried, "the ferry that failed to start is not counted as dispatched")

	// The hull WAS claimed (persist → claim succeeded) before StartContainer failed, so the
	// rollback release below is load-bearing, not vacuous.
	require.Len(t, shipRepo.claims, 1, "the source hull was claimed before the start attempt")
	require.Equal(t, "SRC-A", shipRepo.claims[0].ship, "the deterministic sorted pick, SRC-A, is the claimed hull")

	// Rollback action 1 — releaseHull: the claimed hull is force-released back to the idle pool.
	require.Contains(t, shipRepo.releases, "SRC-A", "StartContainer failure releases the claimed hull (idle again for the next tick)")
	require.True(t, shipRepo.ships[0].IsIdle(), "SRC-A is idle after the rollback, not stranded claimed to a dead container")

	// Rollback action 2 — StopContainer: the persisted-but-unstarted container is torn down,
	// and it is exactly the container that was persisted (not some stray ID).
	require.Len(t, daemonClient.ferried, 1, "the ferry container was persisted before the start attempt")
	require.Equal(t, daemonClient.ferried, daemonClient.stopped, "the persisted-but-unstarted container is exactly the one StopContainer tears down")
	require.Empty(t, daemonClient.started, "no ferry is recorded as started when StartContainer fails")
}

// ---- tests: reclaim (arrival + interruption, one path) ---------------------

// Arrival: a hull still claimed to a COMPLETED worker_ferry container is reclaimed
// (ForceRelease) so it is idle in the destination system — the acceptance criterion (the
// factory then claims it in-system).
func TestRebalancer_Reclaim_Arrival_ReleasesIdle(t *testing.T) {
	clock := clockAt(0)
	arrived := rebalancerLight(t, "LIGHT-9", "X1-DP51-M1") // landed in the vacancy system
	require.NoError(t, arrived.AssignToContainer("worker_ferry-LIGHT-9-abc123", clock))
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{arrived}}
	cq := &fakeRebalancerContainerQuery{ferries: []FerryContainer{
		{ID: "worker_ferry-LIGHT-9-abc123", DestinationWaypoint: "X1-DP51-M1", Status: "COMPLETED", StartedAt: baseTime.Add(-2 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), &fakeHopGraph{hops: map[string]int{}}, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	_, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Contains(t, shipRepo.releases, "LIGHT-9", "the arrived ferry hull is released idle in-system for the factory to man")
	require.True(t, arrived.IsIdle(), "the hull is idle after reclaim")
	require.True(t, logger.loggedContaining("Reclaimed ferried hull LIGHT-9", "arrival"))
}

// Restart-mid-ferry: a worker_ferry container that is FAILED/worker_interrupted with its
// hull still claimed (and old enough to have fallen out of the recent-ferry window) is
// reclaimed from ship state alone and re-evaluated — never stranded (RULINGS #2). Mirrors
// the scout_reposition recovery test shape.
func TestRebalancer_Restart_InterruptedFerry_ReclaimedAndReEvaluated(t *testing.T) {
	clock := clockAt(0)
	stranded := rebalancerLight(t, "LIGHT-3", "X1-MID-A1") // interrupted mid-flight, wherever it sits
	require.NoError(t, stranded.AssignToContainer("worker_ferry-LIGHT-3-def456", clock))
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{stranded}}
	// The ferry row is NOT RUNNING and NOT in the recent window (empty ferries) — the
	// coordinator must still reclaim it purely from the hull's worker_ferry-prefixed claim.
	cq := &fakeRebalancerContainerQuery{}
	daemonClient := &fakeRebalancerDaemonClient{}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), &fakeHopGraph{hops: map[string]int{}}, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	_, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Contains(t, shipRepo.releases, "LIGHT-3", "an interrupted ferry's hull is reclaimed from ship state alone (restart-safe)")
	require.True(t, stranded.IsIdle(), "the hull is freed for re-evaluation next tick")
	require.True(t, logger.loggedContaining("Reclaimed ferried hull LIGHT-3", "interrupted"))
}

// A RUNNING ferry's hull is NEVER reclaimed mid-flight (un-poachable, RULINGS #7).
func TestRebalancer_Reclaim_RunningFerry_LeftUntouched(t *testing.T) {
	clock := clockAt(0)
	inflight := rebalancerLight(t, "LIGHT-5", "X1-MID-A1")
	require.NoError(t, inflight.AssignToContainer("worker_ferry-LIGHT-5-ghi789", clock))
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{inflight}}
	cq := &fakeRebalancerContainerQuery{ferries: []FerryContainer{
		{ID: "worker_ferry-LIGHT-5-ghi789", DestinationWaypoint: "X1-DP51-M1", Status: "RUNNING", StartedAt: baseTime.Add(-1 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), &fakeHopGraph{hops: map[string]int{}}, clock)

	_, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.NoError(t, err)
	require.Empty(t, shipRepo.releases, "a RUNNING ferry's hull is never reclaimed mid-flight")
	require.False(t, inflight.IsIdle(), "the in-flight hull stays claimed")
}

// ---- tests: dry-run + max_lights + off-switch + fail-closed -----------------

// Dry-run decides and logs the ferry it WOULD dispatch but persists/claims/starts nothing.
func TestRebalancer_DryRun_DecidesButFerriesNothing(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	cmd := rebalancerTestCmd()
	cmd.DryRun = true
	ferried, _, err := handler.reconcileOnce(ctx, cmd)

	require.NoError(t, err)
	require.Zero(t, ferried, "dry-run ferries nothing")
	require.Empty(t, daemonClient.ferried, "no ferry container is persisted in dry-run")
	require.Empty(t, shipRepo.claims, "no hull is claimed in dry-run")
	require.True(t, logger.loggedContaining("[dry-run]", "Would ferry SRC-A", "X1-DP51"), "the decision is logged")
}

// max_lights_per_system: a vacancy whose in-system lights plus in-flight inbound ferries
// already meet the cap is not ferried to — even though the in-flight ferry started outside
// the cooldown window (proving a RUNNING-but-old ferry is still counted).
func TestRebalancer_MaxLights_AtCap_NotFerried(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{
		factories: []ActiveFactoryContainer{{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)}},
		// one RUNNING ferry inbound to DP51, started 700s ago (> cooldown, so cooldown does
		// NOT fire) — it counts toward max_lights as an in-flight inbound light.
		ferries: []FerryContainer{{ID: "worker_ferry-inb-1", DestinationWaypoint: "X1-DP51-M1", Status: "RUNNING", StartedAt: baseTime.Add(-700 * time.Second)}},
	}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	cmd := rebalancerTestCmd()
	cmd.MaxLightsPerSystem = 1
	ferried, _, err := handler.reconcileOnce(ctx, cmd)

	require.NoError(t, err)
	require.Zero(t, ferried, "a vacancy already at max_lights (0 in-system + 1 in-flight >= 1) is not ferried to")
	require.Empty(t, shipRepo.claims)
	require.True(t, logger.loggedContaining("X1-DP51", "max_lights_per_system"))
}

// The config off-switch makes a reconcile pass inert.
func TestRebalancer_Disabled_Inert(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	cmd := rebalancerTestCmd()
	cmd.Enabled = false
	ferried, _, err := handler.reconcileOnce(context.Background(), cmd)

	require.NoError(t, err)
	require.Zero(t, ferried, "a disabled coordinator is inert")
	require.Empty(t, shipRepo.claims)
}

// Fail-closed: a ship-list read error aborts the tick with no ferry.
func TestRebalancer_FailClosed_ShipReadError(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &erroringShipRepo{}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := &RunWorkerRebalancerCoordinatorHandler{
		shipRepo:       shipRepo,
		daemonClient:   daemonClient,
		containerQuery: cq,
		marketProvider: ferryDefaultMarket(),
		clock:          clock,
		gateGraph:      graph,
	}

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.Error(t, err, "an unreadable ship list fails the tick closed")
	require.Zero(t, ferried)
	require.Empty(t, daemonClient.ferried)
}

// Fail-closed: a factory-container read error aborts the tick with no ferry.
func TestRebalancer_FailClosed_FactoryReadError(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factoryErr: fmt.Errorf("container table locked")}
	daemonClient := &fakeRebalancerDaemonClient{}
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, ferryDefaultMarket(), graph, clock)

	ferried, _, err := handler.reconcileOnce(context.Background(), rebalancerTestCmd())

	require.Error(t, err, "an unreadable factory-container list fails the tick closed")
	require.Zero(t, ferried)
	require.Empty(t, shipRepo.claims)
}

// A vacancy whose system has no known marketplace waypoint parks fail-closed (no
// destination to ferry to).
func TestRebalancer_NoDestination_ParksFailClosed(t *testing.T) {
	clock := clockAt(0)
	shipRepo := &fakeRebalancerShipRepo{clock: clock, ships: []*navigation.Ship{
		rebalancerLight(t, "SRC-A", "X1-SRC-A1"),
		rebalancerLight(t, "SRC-B", "X1-SRC-A2"),
	}}
	cq := &fakeRebalancerContainerQuery{factories: []ActiveFactoryContainer{
		{SystemSymbol: "X1-DP51", StartedAt: baseTime.Add(-20 * time.Minute)},
	}}
	daemonClient := &fakeRebalancerDaemonClient{}
	mp := &fakeRebalancerMarketProvider{emptySystems: map[string]bool{"X1-DP51": true}} // no market known
	graph := &fakeHopGraph{hops: map[string]int{"X1-SRC->X1-DP51": 1}}
	handler := newTestRebalancerHandler(shipRepo, daemonClient, cq, mp, graph, clock)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	ferried, _, err := handler.reconcileOnce(ctx, rebalancerTestCmd())

	require.NoError(t, err)
	require.Zero(t, ferried, "no known marketplace ⇒ no ferry destination ⇒ fail-closed park")
	require.Empty(t, shipRepo.claims)
	require.True(t, logger.loggedContaining("X1-DP51", "no known marketplace"))
}

// ---- tests: Handle() startup log + absurd-knob sanity guard (sp-nivi) ------
//
// The bug: Handle's one-time startup log passed cmd.vacancyMin() — a time.Duration — to a
// %d verb meant for whole minutes. Go's fmt happily prints ANY integer-kinded value
// (Duration's underlying type is int64) through %d, so go vet's printf checker never flags
// it — it's valid Go, just the wrong value. A configured 15-minute vacancy_min logged as
// "vacancy_min 900000000000m" (raw nanoseconds) instead of "vacancy_min 15m".
//
// These tests exercise Handle() directly (every test above this point calls reconcileOnce)
// because the buggy line lives in Handle's one-time startup log, before the reconcile loop.
// A pre-cancelled context lets Handle log its startup line and then return promptly via the
// ctx.Done() branch, mirroring the trade-fleet coordinator's
// TestTradeHandle_ContextCancelledReturns.

// startedAndCancelledRebalancerHandle runs Handle to completion (immediately, via a
// pre-cancelled context) and returns the captured log. reconcileOnce is never reached, so
// the fakes need no data wired.
func startedAndCancelledRebalancerHandle(t *testing.T, cmd *RunWorkerRebalancerCoordinatorCommand) *tradeCaptureLogger {
	t.Helper()
	handler := newTestRebalancerHandler(&fakeRebalancerShipRepo{}, &fakeRebalancerDaemonClient{}, &fakeRebalancerContainerQuery{}, ferryDefaultMarket(), &fakeHopGraph{}, clockAt(0))
	logger := &tradeCaptureLogger{}
	ctx, cancel := context.WithCancel(common.WithLogger(context.Background(), logger))
	cancel() // already cancelled: Handle logs its startup line, then returns on ctx.Done()

	_, err := handler.Handle(ctx, cmd)
	require.ErrorIs(t, err, context.Canceled)
	return logger
}

// The round-trip a captain actually sees: vacancy_min_minutes: 15 in config.yaml must log
// as "15m", never as the underlying Duration's raw nanoseconds — and a sane value must
// never trip the absurd-knob guard.
func TestRebalancerHandle_StartupLog_VacancyMinLogsWholeMinutes(t *testing.T) {
	cmd := rebalancerTestCmd()
	cmd.VacancyMinMinutes = 15

	logger := startedAndCancelledRebalancerHandle(t, cmd)

	require.True(t, logger.loggedContaining("vacancy_min 15m"), "vacancy_min must log as whole minutes, not a raw Duration")
	require.False(t, logger.loggedContaining("900000000000"), "no raw-nanoseconds value should ever reach the log")
	require.False(t, logger.loggedContaining("clamped"), "a sane configured value must never trip the absurd-knob guard")
}

// The incident value itself: 900,000,000,000 is exactly 15 minutes in nanoseconds — the
// number a ns-as-minutes miswire produces. Configured directly as VacancyMinMinutes, it
// must never reach a Duration conversion un-clamped: multiplying it straight into a
// time.Duration would overflow int64 nanoseconds (900_000_000_000 * 60e9 ≈ 5.4e22, far past
// int64's ~9.22e18 ns ceiling) and silently wrap to a garbage/negative duration BEFORE any
// post-hoc Duration check could catch it. The resolver clamps the RAW int first, so this
// can never happen, and the startup log shows the clamped ceiling plus a loud WARN.
func TestRebalancerHandle_StartupLog_ClampsAbsurdVacancyMin(t *testing.T) {
	cmd := rebalancerTestCmd()
	cmd.VacancyMinMinutes = 900_000_000_000

	logger := startedAndCancelledRebalancerHandle(t, cmd)

	require.True(t, logger.loggedContaining(fmt.Sprintf("vacancy_min %dm", maxSaneVacancyMinMinutes)), "an absurd vacancy_min must log as the clamped ceiling, not the raw value")
	require.False(t, logger.loggedContaining("900000000000m"), "the raw absurd value must never reach the startup log")
	require.True(t, logger.loggedContaining("vacancy_min_minutes", "900000000000", "clamped"), "a loud WARN must name the offending knob and its configured value")
}

// Same guard, the cooldown knob (FerryCooldownSecs).
func TestRebalancerHandle_StartupLog_ClampsAbsurdCooldown(t *testing.T) {
	cmd := rebalancerTestCmd()
	cmd.FerryCooldownSecs = 900_000_000_000

	logger := startedAndCancelledRebalancerHandle(t, cmd)

	require.True(t, logger.loggedContaining(fmt.Sprintf("cooldown %ds", maxSaneFerryCooldownSecs)), "an absurd cooldown must log as the clamped ceiling, not the raw value")
	require.True(t, logger.loggedContaining("ferry_cooldown_seconds", "900000000000", "clamped"), "a loud WARN must name the offending knob and its configured value")
}

// Same guard, the tick knob (TickIntervalSecs).
func TestRebalancerHandle_StartupLog_ClampsAbsurdTick(t *testing.T) {
	cmd := rebalancerTestCmd()
	cmd.TickIntervalSecs = 900_000_000_000

	logger := startedAndCancelledRebalancerHandle(t, cmd)

	require.True(t, logger.loggedContaining("tick 24h0m0s"), "an absurd tick must log as the clamped 24h ceiling, not the raw value")
	require.True(t, logger.loggedContaining("tick_seconds", "900000000000", "clamped"), "a loud WARN must name the offending knob and its configured value")
}
