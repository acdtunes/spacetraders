package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// singleHullFakeShipRepo is a multi-ship fake that ALSO serves the atomic
// ClaimShip + FindBySymbol calls spawnContractWorker issues when the coordinator
// re-adopts an interrupted delivery — readoptFakeShipRepo is single-ship and
// multiOrphanFakeShipRepo has no ClaimShip. FindBySymbol returns the shared ship
// pointer so a spawn's in-memory AssignToContainer mutates the same entity
// FindAllByPlayer later reports (a re-adopted hull reads back as assigned).
type singleHullFakeShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *singleHullFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *singleHullFakeShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var matched []*navigation.Ship
	for _, ship := range r.ships {
		if ship.ContainerID() == containerID {
			matched = append(matched, ship)
		}
	}
	return matched, nil
}

func (r *singleHullFakeShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	for _, ship := range r.ships {
		if ship.ShipSymbol() == symbol {
			return ship, nil
		}
	}
	return nil, fmt.Errorf("ship not found: %s", symbol)
}

func (r *singleHullFakeShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

func (r *singleHullFakeShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	sh, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(sh)
	if err != nil {
		return sh, false, err
	}
	return sh, changed, nil
}

func (r *singleHullFakeShipRepo) ClaimShip(_ context.Context, _ string, _ string, _ shared.PlayerID, _ string) error {
	return nil
}

// negotiationProbeContractRepo records whether the coordinator proceeded to
// NEGOTIATE a contract for a NEW hull: NegotiateContract calls FindActiveContracts
// first (contract_market_service.go), so a call here means the loop got past its
// single-hull gate and is on its way to selecting + dispatching another hull. It
// cancels the run on the first call so the double-dispatch case returns promptly,
// and returns an error so negotiation stops cleanly BEFORE the nil mediator is
// dereferenced (mirrors alwaysErrContractRepo). Zero calls == the coordinator
// waited for the already-active hull instead of dispatching a second (the
// sp-zve2q deterministic single-hull invariant).
type negotiationProbeContractRepo struct {
	domainContract.ContractRepository
	cancel context.CancelFunc
	calls  int
}

func (r *negotiationProbeContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*domainContract.Contract, error) {
	r.calls++
	if r.cancel != nil {
		r.cancel()
	}
	return nil, errors.New("stub: negotiation probe — coordinator proceeded toward dispatching a new hull")
}

func newSingleHullHandler(
	repo navigation.ShipRepository,
	containerRepo *reclaimFakeContainerRepo,
	daemonClient *spawnContractFakeDaemonClient,
	contractRepo domainContract.ContractRepository,
) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, contractRepo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		// MockClock so the failing-negotiate retry's 30s sleep is instant, not a
		// real 30s block, in the double-dispatch path.
		clock:           &shared.MockClock{CurrentTime: time.Now()},
		eventSubscriber: &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
	}
}

// LIVE BUG sp-zve2q (observed 2026-07-22): a daemon restart during active
// contract work re-adopts the cargo-laden hull orphaned mid-delivery (TORWIND-D),
// but the coordinator ALSO selected a SECOND hull (TORWIND-9) for the SAME
// contract; both bought the good and one load was orphaned.
//
// readoptInterruptedDeliveries records the re-adopted hull's fresh worker in
// activeWorkerContainerID SYNCHRONOUSLY, but StartContainer is async — that worker
// has not yet surfaced as RUNNING in the container store. During that window the
// loop's FindExistingWorkers DB check sees zero running workers, calculateInFlightCargo
// sees the hull neither on a RUNNING worker nor on the dead FAILED one (it moved
// to the fresh worker), and idleReclaimedContractCargoHeld sees it as assigned —
// so every existing defense is blind and, without an in-memory single-hull gate,
// the loop negotiates + dispatches a second hull. The contract runs exactly ONE
// hull at a time: with a re-adopted hull active the coordinator must WAIT for it,
// never negotiate a second.
func TestFleetCoordinator_DoesNotDispatchSecondHull_WhileReadoptedWorkerActive(t *testing.T) {
	laden := newInFlightCargoTestShip(t, "TORWIND-D", 40, "contract-work-TORWIND-D-dead") // mid-delivery, orphaned by restart
	idle := newOrphanReclaimTestShip(t, "TORWIND-9", navigation.NavStatusInOrbit)         // a second idle hull the scaler left available
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{laden, idle}}

	// The re-adopted worker's container is deliberately ABSENT from RUNNING — the
	// recovery race window where the just-started worker has not yet surfaced as
	// RUNNING. Only the dead worker is on record (FAILED).
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-TORWIND-D-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	daemonClient := &spawnContractFakeDaemonClient{}
	contractRepo := &negotiationProbeContractRepo{}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	contractRepo.cancel = cancel

	handler := newSingleHullHandler(repo, containerRepo, daemonClient, contractRepo)
	_, _ = handler.Handle(ctx, contractSpawnCommand())

	// The re-adopted hull's worker WAS started (recovery happened) ...
	if len(daemonClient.started) != 1 {
		t.Fatalf("expected exactly the re-adopted hull's worker started, got %v", daemonClient.started)
	}
	// ... and the coordinator did NOT go on to negotiate a contract for a second
	// hull while that re-adopted hull was still active — it treated it as the SOLE
	// hull (deterministic single-hull re-selection).
	if contractRepo.calls != 0 {
		t.Fatalf("coordinator dispatched toward a SECOND hull while the re-adopted hull was active (negotiated %d time(s)) - double-buy regression", contractRepo.calls)
	}
	// The second idle hull was never claimed for the contract.
	if idle.IsAssigned() {
		t.Fatalf("expected the second idle hull %s left untouched, got it assigned to %q", idle.ShipSymbol(), idle.ContainerID())
	}
}

// Behaviour preservation: with NO active/re-adopted hull (a normal cold pass), the
// single-hull gate is a no-op — the coordinator proceeds to negotiate + select a
// hull exactly as before. Guards against the gate over-blocking normal dispatch.
func TestFleetCoordinator_DispatchesNormally_WhenNoActiveHull(t *testing.T) {
	idle := newOrphanReclaimTestShip(t, "TORWIND-9", navigation.NavStatusInOrbit)
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{idle}}
	containerRepo := &reclaimFakeContainerRepo{} // no FAILED, no RUNNING — nothing active
	daemonClient := &spawnContractFakeDaemonClient{}
	contractRepo := &negotiationProbeContractRepo{}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	contractRepo.cancel = cancel

	handler := newSingleHullHandler(repo, containerRepo, daemonClient, contractRepo)
	_, _ = handler.Handle(ctx, contractSpawnCommand())

	if contractRepo.calls == 0 {
		t.Fatalf("expected the coordinator to negotiate/select normally when no hull is active, but it never did (gate over-blocked the normal path)")
	}
}

// newImpureCargoTestShip builds an IDLE hauler holding the contract good PLUS an
// unrelated good — the NO-CARGO-DUMP-guard shape: claiming it for the contract
// could jettison the unrelated load, so the holder short-circuit must NOT pick it.
func newImpureCargoTestShip(t *testing.T, symbol, primaryGood string, primaryUnits int, otherGood string, otherUnits int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	primary, err := shared.NewCargoItem(primaryGood, primaryGood, "", primaryUnits)
	if err != nil {
		t.Fatalf("NewCargoItem(primary): %v", err)
	}
	other, err := shared.NewCargoItem(otherGood, otherGood, "", otherUnits)
	if err != nil {
		t.Fatalf("NewCargoItem(other): %v", err)
	}
	cargo, err := shared.NewCargo(80, primaryUnits+otherUnits, []*shared.CargoItem{primary, other})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), location, fuel, 0, 80, cargo, 9, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// LIVE BUG sp-zve2q (08:45:45 smoking gun): an idle hull (TORWIND-15) already held
// 43 FERTILIZERS — the exact contract need — the coordinator logged it would
// "complete that load with its holder", then fell through to "Selecting closest
// ship" and picked a DIFFERENT empty hull (TORWIND-8), which DOUBLE-SOURCED. The
// upstream candidate discovery drops such a holder when it is in transit or
// undedicated-while-a-dedicated-fleet-is-active, so it never reaches
// SelectClosestShip's own cargo-priority. idleContractCargoHolder binds detection
// to selection: it NAMES the holder the coordinator must select. It sees the
// holder regardless of those filters (full-fleet scan), so the deterministic
// short-circuit picks it over the closest empty hull — the pure holder wins over
// an empty hull, an ASSIGNED holder (handled on its worker), and an IMPURE holder
// (NO-CARGO-DUMP park/liquidate territory).
func TestIdleContractCargoHolder_NamesPureIdleHolder_OverEmptyAssignedAndImpure(t *testing.T) {
	empty := newOrphanReclaimTestShip(t, "TORWIND-8", navigation.NavStatusInOrbit)                // closest empty hull — the wrong pick
	pureHolder := newInFlightCargoTestShip(t, "TORWIND-15", 43, "")                               // idle, holds only the good — the RIGHT pick
	assignedHolder := newInFlightCargoTestShip(t, "TORWIND-3", 50, "cw-running")                  // holds MORE, but on a worker → excluded
	impureHolder := newImpureCargoTestShip(t, "TORWIND-7", "LIQUID_NITROGEN", 30, "ICE_WATER", 5) // holds the good + unrelated → excluded
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{empty, pureHolder, assignedHolder, impureHolder}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	got, err := handler.idleContractCargoHolder(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("idleContractCargoHolder: %v", err)
	}
	if got != "TORWIND-15" {
		t.Fatalf("expected the pure idle holder TORWIND-15 selected over the empty/assigned/impure hulls, got %q", got)
	}
}

// Two pure idle holders: the coordinator must pick ONE deterministically so a
// restart re-selects the SAME hull (never a second cargo-laden one). Rule: most
// units, ties broken by the lexicographically-smallest symbol.
func TestIdleContractCargoHolder_PicksMostUnits_ThenSymbol_Deterministic(t *testing.T) {
	partial := newInFlightCargoTestShip(t, "TORWIND-9", 20, "") // fewer units
	fullA := newInFlightCargoTestShip(t, "TORWIND-5", 43, "")   // tie on units, larger symbol
	fullB := newInFlightCargoTestShip(t, "TORWIND-2", 43, "")   // tie on units, smaller symbol → winner
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{partial, fullA, fullB}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	got, err := handler.idleContractCargoHolder(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("idleContractCargoHolder: %v", err)
	}
	if got != "TORWIND-2" {
		t.Fatalf("expected the most-units holder with the smallest symbol (TORWIND-2) for restart-deterministic re-selection, got %q", got)
	}
}

// No idle hull holds the good → empty string, so the coordinator falls through to
// its unchanged distance-based selection (behaviour preservation).
func TestIdleContractCargoHolder_NoHolder_ReturnsEmpty(t *testing.T) {
	empty := newOrphanReclaimTestShip(t, "TORWIND-8", navigation.NavStatusInOrbit)
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{empty}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	got, err := handler.idleContractCargoHolder(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("idleContractCargoHolder: %v", err)
	}
	if got != "" {
		t.Fatalf("expected no holder (empty string) so distance selection runs unchanged, got %q", got)
	}
}
