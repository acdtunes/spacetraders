package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes -------------------------------------------------------------------

// liquidationE2EShipRepo backs the whole loop: FilterUnrelatedCargo's FindAllByPlayer,
// spawnLiquidationWorker's FindBySymbol/ClaimShip, and the liquidation handler's
// SyncShipFromAPI reconcile. The `ship` field is swappable so a test can flip the hull
// from laden to empty, modelling the server state after the worker's sell.
//
// serverShip/syncErr model the SERVER's answer diverging from the persisted `ship`
// the parking decision was made on — the hull emptying between decision and dispatch.
type liquidationE2EShipRepo struct {
	navigation.ShipRepository
	ship       *navigation.Ship
	serverShip *navigation.Ship
	syncErr    error
	findErr    error
	syncCalls  int
	findCalls  int
	claims     []contractShipClaim
}

func (r *liquidationE2EShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return []*navigation.Ship{r.ship}, nil
}
func (r *liquidationE2EShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.findCalls++
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.ship, nil
}
func (r *liquidationE2EShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.syncCalls++
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	if r.serverShip != nil {
		return r.serverShip, nil
	}
	return r.ship, nil
}
func (r *liquidationE2EShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	r.claims = append(r.claims, contractShipClaim{symbol: symbol, containerID: containerID, operation: operation})
	return nil
}
func (r *liquidationE2EShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

// SaveWithRetry mirrors the real repository's non-conflict path (find → mutate →
// save) so a migrated fleet-coordinator persist exercises its production
// closure against this fake without hitting the embedded nil interface.
func (r *liquidationE2EShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
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

// liquidationE2EMarket answers the best in-system bid for the strand good.
type liquidationE2EMarket struct {
	market.MarketRepository
	byGood map[string]*market.BestMarketBuyingResult
}

func (m *liquidationE2EMarket) FindBestMarketBuying(_ context.Context, good, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	return m.byGood[good], nil
}

// liquidationE2EMediator records the sell the liquidation worker issues and reports it sold.
type liquidationE2EMediator struct {
	sent      []common.Request
	sellPrice int
}

func (m *liquidationE2EMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sent = append(m.sent, request)
	switch cmd := request.(type) {
	case *shipCargo.SellCargoCommand:
		return &shipCargo.SellCargoResponse{UnitsSold: cmd.Units, TotalRevenue: cmd.Units * m.sellPrice}, nil
	default:
		return nil, nil // navigate/dock succeed silently
	}
}
func (m *liquidationE2EMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *liquidationE2EMediator) RegisterMiddleware(_ common.Middleware)                 {}

func (m *liquidationE2EMediator) soldUnits() int {
	total := 0
	for _, r := range m.sent {
		if s, ok := r.(*shipCargo.SellCargoCommand); ok {
			total += s.Units
		}
	}
	return total
}

// liquidationCapturingDaemonClient captures the persisted command so a test can drive the
// real liquidation worker with exactly the command the coordinator built.
type liquidationCapturingDaemonClient struct {
	daemon.DaemonClient
	persistedKinds []daemon.ContainerKind
	persistedCmds  []interface{}
	startedKinds   []daemon.ContainerKind
	started        []string
}

func (d *liquidationCapturingDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, _ string, _ uint, command interface{}) error {
	d.persistedKinds = append(d.persistedKinds, kind)
	d.persistedCmds = append(d.persistedCmds, command)
	return nil
}
func (d *liquidationCapturingDaemonClient) StartContainer(_ context.Context, kind daemon.ContainerKind, id string) error {
	d.startedKinds = append(d.startedKinds, kind)
	d.started = append(d.started, id)
	return nil
}
func (d *liquidationCapturingDaemonClient) StopContainer(_ context.Context, _ string) error {
	return nil
}

// ladenHull builds a docked hauler at waypointSymbol holding `units` of `good`.
func ladenHull(t *testing.T, symbol, good, waypointSymbol string, units int) *navigation.Ship {
	t.Helper()
	inv := []*shared.CargoItem{}
	total := 0
	if units > 0 {
		ci, err := shared.NewCargoItem(good, good, "", units)
		require.NoError(t, err)
		inv = append(inv, ci)
		total = units
	}
	cargo, err := shared.NewCargo(200, total, inv)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), wp, fuel, 400, 200, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	return ship
}

func liquidationCommand(disabled bool, minJettison int) *RunFleetCoordinatorCommand {
	return &RunFleetCoordinatorCommand{
		PlayerID:                    shared.MustNewPlayerID(1),
		ContainerID:                 "contract-fleet-coordinator-1",
		AutoLiquidationDisabled:     disabled,
		LiquidationMinJettisonValue: minJettison,
	}
}

func newLiquidationDispatchHandler(repo navigation.ShipRepository, daemonClient daemon.DaemonClient, clock shared.Clock) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, nil, repo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		clock:                  clock,
	}
}

// --- the acceptance ----------------------------------------------------------

// ACCEPTANCE (verbatim): a parked-with-cargo contract hull self-clears (sell or
// floor-jettison) and re-enters candidacy without captain hands. This pins the whole loop
// at the command level: FilterUnrelatedCargo parks a laden hull out of candidacy (the
// jam) -> the coordinator dispatches a cargo_liquidation worker on it -> the REAL
// liquidation worker sells the strand at the best in-system bid -> the now-empty hull is
// claimable by FilterUnrelatedCargo again. No captain hands anywhere in the chain.
func TestFleetCoordinator_ParkedHullSelfClearsAndReentersCandidacy(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	// A dual-duty hull holding a leftover SILICON_CRYSTALS lot, unrelated to an active
	// IRON_ORE contract — exactly the strand the incident described.
	hull := ladenHull(t, "TORWIND-7", "SILICON_CRYSTALS", "X1-KA42-A1", 66)
	repo := &liquidationE2EShipRepo{ship: hull}

	// PRECONDITION — the jam: the laden hull is parked, NOT claimable for the IRON_ORE
	// contract, so with it as the only candidate the pool has zero eligible workers.
	claimable, parked, err := appContract.FilterUnrelatedCargo(context.Background(), playerID, repo, []string{"TORWIND-7"}, "IRON_ORE")
	require.NoError(t, err)
	require.Empty(t, claimable, "precondition: the laden hull is not claimable — the pool is jammed")
	require.Equal(t, []string{"TORWIND-7"}, parked, "precondition: the hull is parked for holding unrelated cargo")

	// STEP 1 — the coordinator self-clears it: dispatch auto-liquidation on the parked hull.
	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, shared.NewRealClock())
	handler.reconcileParkedHulls(context.Background(), liquidationCommand(false, 0), parked, "IRON_ORE", map[string]time.Time{})

	require.Equal(t, []daemon.ContainerKind{daemon.ContainerKindCargoLiquidation}, daemonClient.persistedKinds, "a cargo_liquidation worker is persisted for the parked hull")
	require.Equal(t, []daemon.ContainerKind{daemon.ContainerKindCargoLiquidation}, daemonClient.startedKinds, "and started")
	require.Len(t, repo.claims, 1)
	require.Equal(t, "TORWIND-7", repo.claims[0].symbol)
	require.Equal(t, "contract", repo.claims[0].operation, "claimed under the contract fleet identity (foreign-pinned hulls are rejected, not poached)")
	dispatched, ok := daemonClient.persistedCmds[0].(*liquidation.LiquidateCargoCommand)
	require.True(t, ok, "the persisted command is a LiquidateCargoCommand")
	require.Equal(t, "TORWIND-7", dispatched.ShipSymbol)
	require.Equal(t, "contract-fleet-coordinator-1", dispatched.CoordinatorID, "carries the coordinator id so restart recovery skips it")

	// STEP 2 — the worker runs (what the started container does): sell the strand at the
	// best in-system bid. Driving the REAL handler with the coordinator's command proves
	// the dispatch is wired to a worker that actually clears the hold.
	mkt := &liquidationE2EMarket{byGood: map[string]*market.BestMarketBuyingResult{
		"SILICON_CRYSTALS": {WaypointSymbol: "X1-KA42-B7", Bid: 2200},
	}}
	med := &liquidationE2EMediator{sellPrice: 2200}
	worker := liquidation.NewLiquidateCargoHandler(repo, mkt, med)
	resp, err := worker.Handle(context.Background(), dispatched)
	require.NoError(t, err)
	require.Equal(t, 66, resp.(*liquidation.LiquidateCargoResponse).UnitsSold, "the worker sold the whole leftover lot — value recovered, not dumped")
	require.Equal(t, 66, med.soldUnits())

	// The sell emptied the hull on the server; the repo now reflects that truth.
	repo.ship = ladenHull(t, "TORWIND-7", "SILICON_CRYSTALS", "X1-KA42-B7", 0)

	// POSTCONDITION — re-entry: FilterUnrelatedCargo now returns the hull claimable. The
	// jam is cleared with zero captain hands.
	claimable, parked, err = appContract.FilterUnrelatedCargo(context.Background(), playerID, repo, []string{"TORWIND-7"}, "IRON_ORE")
	require.NoError(t, err)
	require.Equal(t, []string{"TORWIND-7"}, claimable, "the self-cleared hull re-enters candidacy")
	require.Empty(t, parked, "nothing remains parked")
}

// The escape hatch: with auto-liquidation disabled, a parked hull is left alone (no
// worker spawned) — the previous behavior, preserved for a captain who opts out.
func TestFleetCoordinator_AutoLiquidationDisabled_NoDispatch(t *testing.T) {
	hull := ladenHull(t, "TORWIND-3", "PLASTICS", "X1-KA42-A1", 67)
	repo := &liquidationE2EShipRepo{ship: hull}
	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, shared.NewRealClock())

	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), liquidationCommand(true, 0), []string{"TORWIND-3"}, "IRON_ORE", map[string]time.Time{})

	require.Empty(t, daemonClient.persistedKinds, "no liquidation worker is spawned when the feature is disabled")
	require.Empty(t, repo.claims)
	require.Empty(t, readmitted, "a confirmed strand is not re-admitted just because liquidation is off")
	require.Equal(t, []string{"TORWIND-3"}, stillParked)
}

// The escape hatch governs SPAWNING, not pool correctness: with auto-liquidation
// disabled, a hull whose hold cleared between the parking decision and dispatch must
// still be re-admitted to candidacy. Opting out of liquidation is never a licence to
// select over a pool already known to be wrong.
func TestFleetCoordinator_AutoLiquidationDisabled_ClearedHoldStillReadmitted(t *testing.T) {
	repo := &liquidationE2EShipRepo{
		ship:       ladenHull(t, "TORWIND-3", "PLASTICS", "X1-KA42-A1", 67),
		serverShip: ladenHull(t, "TORWIND-3", "PLASTICS", "X1-KA42-A1", 0),
	}
	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, shared.NewRealClock())

	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), liquidationCommand(true, 0), []string{"TORWIND-3"}, "IRON_ORE", map[string]time.Time{})

	require.Equal(t, []string{"TORWIND-3"}, readmitted, "a cleared hold re-enters candidacy even with liquidation disabled")
	require.Empty(t, stillParked)
	require.Empty(t, daemonClient.persistedKinds, "and still no liquidation worker is spawned")
	require.Empty(t, repo.claims)
}

// The per-hull cooldown bounds a spawn-storm: a hull dispatched this pass is not
// re-dispatched on the immediately following pass, but is retried once the cooldown
// elapses (a deferral, never a permanent skip).
func TestFleetCoordinator_LiquidationCooldown_SuppressesThenRetries(t *testing.T) {
	hull := ladenHull(t, "TORWIND-5", "FABRICS", "X1-KA42-A1", 54)
	repo := &liquidationE2EShipRepo{ship: hull}
	daemonClient := &liquidationCapturingDaemonClient{}
	clock := &shared.MockClock{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, clock)
	cooldown := map[string]time.Time{}
	cmd := liquidationCommand(false, 0)

	handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-5"}, "IRON_ORE", cooldown)
	require.Len(t, daemonClient.persistedKinds, 1, "first pass dispatches")

	// Immediately-following pass while still within the cooldown window: no re-dispatch.
	handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-5"}, "IRON_ORE", cooldown)
	require.Len(t, daemonClient.persistedKinds, 1, "a hull within its cooldown is not re-dispatched (no storm)")

	// After the cooldown elapses, the hull is retried.
	clock.Advance(liquidationDispatchCooldown + time.Minute)
	handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-5"}, "IRON_ORE", cooldown)
	require.Len(t, daemonClient.persistedKinds, 2, "after the cooldown elapses the stuck hull is retried, never permanently skipped")
}

// CONTRACT TURNOVER: delivery empties the hold and the next contract is accepted within
// about a second of each other, so a hull parked for holding the OUTGOING contract's good
// is already clear by the time the worker would run. The parking decision was right when
// it was made and wrong when acted on — nothing may be spawned, and above all the hull
// must not be claimed off contract work to run a no-op.
func TestFleetCoordinator_HoldClearsBetweenParkingAndDispatch_NoWorkerSpawned(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	// Persisted view: still holding contract 1's EQUIPMENT load.
	repo := &liquidationE2EShipRepo{ship: ladenHull(t, "TORWIND-7", "EQUIPMENT", "X1-KA42-A1", 18)}

	// Contract 2 was accepted requiring a different good, so the outgoing load reads as
	// unrelated and the hull is parked out of candidacy.
	claimable, parked, err := appContract.FilterUnrelatedCargo(context.Background(), playerID, repo, []string{"TORWIND-7"}, "IRON_ORE")
	require.NoError(t, err)
	require.Empty(t, claimable)
	require.Equal(t, []string{"TORWIND-7"}, parked, "precondition: the parking decision fires on the outgoing load")

	// The delivery that fulfilled contract 1 emptied the hold: the server now says clear.
	repo.serverShip = ladenHull(t, "TORWIND-7", "EQUIPMENT", "X1-KA42-A1", 0)

	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, shared.NewRealClock())
	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), liquidationCommand(false, 0), parked, "IRON_ORE", map[string]time.Time{})

	require.Empty(t, daemonClient.persistedKinds, "no liquidation worker is spawned for a hull that is no longer holding unrelated cargo")
	require.Empty(t, daemonClient.startedKinds, "and none is started")
	require.Empty(t, repo.claims, "and the hull is never claimed off contract work to run a no-op")
	require.Equal(t, []string{"TORWIND-7"}, readmitted, "the void exclusion is acted on: the hull rejoins candidacy for THIS pass")
	require.Empty(t, stillParked, "nothing is left parked on a premise the read just disproved")
}

// The asymmetry that keeps the guard from becoming "never liquidate": when the server
// CONFIRMS the strand is still aboard, the hull gets its worker. This is the tour-leftover
// case auto-liquidation exists for.
func TestFleetCoordinator_ServerConfirmsStrandedHold_WorkerStillSpawned(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	hull := ladenHull(t, "TORWIND-6", "POLYNUCLEOTIDES", "X1-KA42-A1", 4)
	repo := &liquidationE2EShipRepo{ship: hull, serverShip: hull}

	claimable, parked, err := appContract.FilterUnrelatedCargo(context.Background(), playerID, repo, []string{"TORWIND-6"}, "IRON_ORE")
	require.NoError(t, err)
	require.Empty(t, claimable)
	require.Equal(t, []string{"TORWIND-6"}, parked)

	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, shared.NewRealClock())
	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), liquidationCommand(false, 0), parked, "IRON_ORE", map[string]time.Time{})

	require.Empty(t, readmitted, "a confirmed strand is NEVER re-admitted - the NO-CARGO-DUMP guard still binds")
	require.Equal(t, []string{"TORWIND-6"}, stillParked)
	require.Equal(t, []daemon.ContainerKind{daemon.ContainerKindCargoLiquidation}, daemonClient.persistedKinds, "a confirmed strand still gets its worker")
	require.Equal(t, []daemon.ContainerKind{daemon.ContainerKindCargoLiquidation}, daemonClient.startedKinds)
	require.Len(t, repo.claims, 1)
	require.Equal(t, "TORWIND-6", repo.claims[0].symbol)
	dispatched, ok := daemonClient.persistedCmds[0].(*liquidation.LiquidateCargoCommand)
	require.True(t, ok)
	require.Equal(t, "TORWIND-6", dispatched.ShipSymbol)
}

// THE DEADLOCK. The live read is the same call that strands the hull, so when it fails the
// dispatch falls back to the persisted hold FilterUnrelatedCargo already parked on, and the
// remedy still reaches an unreadable ship. Cooldown still bounds the retry.
func TestFleetCoordinator_LiveReadFails_FallsBackToPersistedHold_AndDispatches(t *testing.T) {
	repo := &liquidationE2EShipRepo{
		ship:    ladenHull(t, "TORWIND-9", "FABRICS", "X1-KA42-A1", 54),
		syncErr: errors.New("failed to get ship: max retries exceeded: server error (500)"),
	}
	daemonClient := &liquidationCapturingDaemonClient{}
	clock := &shared.MockClock{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, clock)

	handler.reconcileParkedHulls(context.Background(), liquidationCommand(false, 0), []string{"TORWIND-9"}, "IRON_ORE", map[string]time.Time{})

	require.Equal(t, 1, repo.syncCalls, "the live read is still ATTEMPTED first - the fallback is not a shortcut")
	require.Equal(t, 1, repo.findCalls, "and the persisted hold is what answers when it fails")
	require.Equal(t, []daemon.ContainerKind{daemon.ContainerKindCargoLiquidation}, daemonClient.persistedKinds,
		"a 500 on the hull being cleared must not block clearing it")
	require.Len(t, repo.claims, 1)
	require.Equal(t, "TORWIND-9", repo.claims[0].symbol)
}

// ANTI-VACUITY CONTROL for the test above: same failing live read, but the persisted hold says the
// hull is CLEAR. The fallback must then reach errHoldAlreadyClear and spawn NOTHING - proving the
// dispatch reads the fallback's CONTENT rather than treating any fallback as licence to spawn.
func TestFleetCoordinator_LiveReadFails_PersistedHoldClear_NoSpawn(t *testing.T) {
	repo := &liquidationE2EShipRepo{
		ship:    ladenHull(t, "TORWIND-9", "IRON_ORE", "X1-KA42-A1", 54),
		syncErr: errors.New("failed to get ship: max retries exceeded: server error (500)"),
	}
	daemonClient := &liquidationCapturingDaemonClient{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, &shared.MockClock{})

	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), liquidationCommand(false, 0), []string{"TORWIND-9"}, "IRON_ORE", map[string]time.Time{})

	require.Equal(t, 1, repo.findCalls, "the fallback was reached")
	require.Empty(t, daemonClient.persistedKinds, "but a clear persisted hold still spawns nothing")
	require.Empty(t, repo.claims)
	require.Equal(t, []string{"TORWIND-9"}, readmitted, "whichever read answered is the verdict both decisions are taken from - a clear hold re-enters candidacy")
	require.Empty(t, stillParked)
}

// Fail-closed still applies where it belongs: BOTH reads unavailable and nothing is claimed. The
// cooldown bounds the retry, so a persistent double failure cannot spin.
func TestFleetCoordinator_BothReadsFail_NoSpawn_CooldownBoundsRetry(t *testing.T) {
	repo := &liquidationE2EShipRepo{
		ship:    ladenHull(t, "TORWIND-9", "FABRICS", "X1-KA42-A1", 54),
		syncErr: errors.New("ship fetch failed: 503"),
		findErr: errors.New("ship row unavailable"),
	}
	daemonClient := &liquidationCapturingDaemonClient{}
	clock := &shared.MockClock{}
	handler := newLiquidationDispatchHandler(repo, daemonClient, clock)
	cooldown := map[string]time.Time{}
	cmd := liquidationCommand(false, 0)

	readmitted, stillParked := handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-9"}, "IRON_ORE", cooldown)
	require.Empty(t, daemonClient.persistedKinds, "a hold unverifiable by BOTH reads spawns nothing")
	require.Empty(t, repo.claims, "and the hull is not claimed")
	require.Equal(t, 1, repo.syncCalls)
	require.Empty(t, readmitted, "an unverifiable hold is never re-admitted to candidacy — fail closed")
	require.Equal(t, []string{"TORWIND-9"}, stillParked)

	readmitted, stillParked = handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-9"}, "IRON_ORE", cooldown)
	require.Equal(t, 1, repo.syncCalls, "a hull within its cooldown is not re-verified — a persistent failure cannot spin")
	require.Empty(t, readmitted, "and an unexamined hull keeps its parking rather than being guessed back into the pool")
	require.Equal(t, []string{"TORWIND-9"}, stillParked)

	clock.Advance(liquidationDispatchCooldown + time.Minute)
	repo.syncErr, repo.findErr = nil, nil
	handler.reconcileParkedHulls(context.Background(), cmd, []string{"TORWIND-9"}, "IRON_ORE", cooldown)
	require.Len(t, daemonClient.persistedKinds, 1, "once verification recovers the confirmed strand is dispatched — deferred, never skipped")
}
