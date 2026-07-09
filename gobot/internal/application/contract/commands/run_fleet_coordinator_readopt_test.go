package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// readoptFakeShipRepo supports BOTH the FindByContainer scan the re-adoption pass
// uses to discover interrupted-worker ships AND the FindBySymbol reload
// spawnContractWorker does, tracking every Save so a test can inspect the ship's
// final container assignment.
type readoptFakeShipRepo struct {
	navigation.ShipRepository

	ship   *navigation.Ship
	onSave func()
	saves  []contractShipSnapshot
	claims []contractShipClaim // atomic ClaimShip calls the re-adoption issues (sp-lprs)
}

func (r *readoptFakeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return []*navigation.Ship{r.ship}, nil
}

func (r *readoptFakeShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if r.ship.ContainerID() == containerID {
		return []*navigation.Ship{r.ship}, nil
	}
	return nil, nil
}

func (r *readoptFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *readoptFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.saves = append(r.saves, contractShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	if r.onSave != nil {
		r.onSave()
	}
	return nil
}

// ClaimShip records the atomic operation-checked claim spawnContractWorker now
// issues to acquire the re-adopted hull for the fresh worker (sp-lprs). The old
// AssignToContainer+Save happy path is gone, so the ship's final assignment is
// observed here (and on the in-memory entity), not via a Save.
func (r *readoptFakeShipRepo) ClaimShip(_ context.Context, symbol string, containerID string, _ shared.PlayerID, operation string) error {
	r.claims = append(r.claims, contractShipClaim{symbol: symbol, containerID: containerID, operation: operation})
	return nil
}

func (r *readoptFakeShipRepo) lastClaim(t *testing.T) contractShipClaim {
	t.Helper()
	if len(r.claims) == 0 {
		t.Fatalf("expected at least one ClaimShip call, got none")
	}
	return r.claims[len(r.claims)-1]
}

func newReadoptHandler(repo *readoptFakeShipRepo, containerRepo *reclaimFakeContainerRepo, daemonClient *spawnContractFakeDaemonClient) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		clock:                  shared.NewRealClock(),
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
	}
}

// newLadenContractTestShip builds a hauler mid-delivery: it holds contract cargo
// (40 units of LIQUID_NITROGEN) for an already-accepted contract, exactly the
// state a daemon restart orphans.
func newLadenContractTestShip(t *testing.T) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	item, err := shared.NewCargoItem("LIQUID_NITROGEN", "Liquid Nitrogen", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	cargo, err := shared.NewCargo(80, 40, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-6",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		80,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// A daemon restart marks the in-flight worker FAILED but preserves the ship +
// cargo. The coordinator must RE-ADOPT that ship — spawn a fresh delivery worker
// so it resumes the delivery leg — instead of force-releasing it and restarting
// the workflow from negotiate/find-purchase-market (the 15-30min throughput hole,
// sp-tgp5). Because the handler's contractMarketService is nil, if re-adoption
// failed and the ship fell through to the main loop's NegotiateContract path the
// test would nil-panic — so a clean pass is itself evidence the workflow was NOT
// restarted from scratch.
func TestFleetCoordinator_ReadoptsInFlightDelivery_ResumesWithoutReNegotiation(t *testing.T) {
	ship := newLadenContractTestShip(t)
	if err := ship.AssignToContainer("contract-work-TORWIND-6-dead", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &readoptFakeShipRepo{ship: ship}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-TORWIND-6-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newReadoptHandler(repo, containerRepo, daemonClient)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, _ = handler.Handle(ctx, contractSpawnCommand())

	// A fresh contract-work worker is spawned + started for the cargo-laden ship.
	if len(daemonClient.started) != 1 || !strings.HasPrefix(daemonClient.started[0], "contract-work-") {
		t.Fatalf("expected a re-adopted contract-work worker to be started, got %v", daemonClient.started)
	}
	// The ship ends up assigned to the NEW worker (not idle, not the dead
	// container), so it resumes the delivery leg instead of returning to blind
	// discovery. Post-sp-lprs the acquisition is the atomic operation-checked
	// ClaimShip under the contract fleet identity — not an AssignToContainer+Save
	// — so the final assignment is observed on the claim and the in-memory entity
	// rather than a happy-path Save.
	claim := repo.lastClaim(t)
	if claim.symbol != "TORWIND-6" || claim.operation != "contract" {
		t.Fatalf("expected the re-adopted ship claimed under operation contract, got %+v", claim)
	}
	if claim.containerID != daemonClient.started[0] {
		t.Fatalf("expected ship claimed by the re-adopted worker %q, got %q", daemonClient.started[0], claim.containerID)
	}
	if claim.containerID == "contract-work-TORWIND-6-dead" {
		t.Fatalf("expected ship moved off the dead worker container, still on it: %q", claim.containerID)
	}
	if !ship.IsAssigned() || ship.ContainerID() != daemonClient.started[0] {
		t.Fatalf("expected the re-adopted ship assigned in-memory to worker %q, got assigned=%v container=%q",
			daemonClient.started[0], ship.IsAssigned(), ship.ContainerID())
	}
}

// An interrupted worker whose ship holds NO cargo was mid-purchase or mid-nav with
// nothing aboard: there is no delivery to resume, so it must NOT be re-adopted
// (that would restart the workflow from scratch and burn a worker). It falls
// through to ReclaimShipsFromInterruptedWorkers, which frees it into normal
// discovery. This guards the cargo split at the heart of the fix.
func TestFleetCoordinator_DoesNotReadoptEmptyInterruptedShip(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit) // empty cargo
	if err := ship.AssignToContainer("contract-work-dead", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &readoptFakeShipRepo{ship: ship}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newReadoptHandler(repo, containerRepo, daemonClient)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	// The empty ship is not re-adopted; it is reclaimed to idle by the loop's
	// ReclaimShipsFromInterruptedWorkers. Cancel on that release Save so Handle
	// returns at the next ctx.Done() check instead of proceeding to negotiate a
	// fresh contract (which the nil contractMarketService cannot serve).
	repo.onSave = cancel
	_, _ = handler.Handle(ctx, contractSpawnCommand())

	if len(daemonClient.persisted) != 0 || len(daemonClient.started) != 0 {
		t.Fatalf("expected no worker spawned for empty interrupted ship, got persisted=%v started=%v", daemonClient.persisted, daemonClient.started)
	}
	// Empty interrupted ship is reclaimed to the idle pool instead.
	if ship.IsAssigned() {
		t.Fatalf("expected empty interrupted ship released to idle pool, still assigned to %q", ship.ContainerID())
	}
}
