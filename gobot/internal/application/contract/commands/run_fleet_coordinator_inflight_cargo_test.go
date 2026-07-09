package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newInFlightCargoTestShip builds a hauler carrying units of LIQUID_NITROGEN,
// already assigned to containerID (or left idle if containerID is empty).
// Mirrors newLadenContractTestShip (run_fleet_coordinator_readopt_test.go) but
// parameterized over symbol/units/container so a single test can build
// several distinct laden ships at once.
func newInFlightCargoTestShip(t *testing.T, symbol string, units int, containerID string) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	item, err := shared.NewCargoItem("LIQUID_NITROGEN", "Liquid Nitrogen", "", units)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	cargo, err := shared.NewCargo(80, units, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
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
	if containerID != "" {
		if err := ship.AssignToContainer(containerID, shared.NewRealClock()); err != nil {
			t.Fatalf("AssignToContainer: %v", err)
		}
	}
	return ship
}

// newInFlightCargoHandler builds the minimal handler calculateInFlightCargo
// actually touches (workerLifecycleManager + shipRepo) — it is called
// directly in these tests, bypassing Handle(), so the rest of the handler's
// dependencies (contractMarketService, clock, eventSubscriber, ...) are
// intentionally left nil.
func newInFlightCargoHandler(repo *multiOrphanFakeShipRepo, containerRepo *reclaimFakeContainerRepo) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(&spawnContractFakeDaemonClient{}, containerRepo, repo),
		shipRepo:               repo,
	}
}

// A ship whose contract worker died (its container marked FAILED) still
// physically holds its contract cargo: ReclaimShipsFromInterruptedWorkers
// deliberately preserves the assignment right up until it force-releases the
// ship, and readoptInterruptedDeliveries re-adopts at most one ship per
// startup, so a second laden interrupted ship (or one that slips past
// re-adoption) can sit attached to its dead FAILED container for one or more
// coordinator passes. Before sp-u20w this cargo was invisible to
// calculateInFlightCargo (it only looked at RUNNING workers), so the
// coordinator could go purchase units redundant with what this hull is still
// physically holding — the bug this test guards against.
func TestCalculateInFlightCargo_CountsInterruptedLadenShip(t *testing.T) {
	ship := newInFlightCargoTestShip(t, "TORWIND-6", 25, "contract-work-dead")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{ship}}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"FAILED": {{ID: "contract-work-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	handler := newInFlightCargoHandler(repo, containerRepo)

	got, err := handler.calculateInFlightCargo(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("calculateInFlightCargo: %v", err)
	}
	if got != 25 {
		t.Fatalf("expected 25 units in-flight from the interrupted ship's own cargo, got %d", got)
	}
}

// Baseline regression guard: a ship attached to a live RUNNING worker is
// counted exactly as before this fix — the pre-existing RUNNING-workers pass
// is untouched by the sp-u20w addition.
func TestCalculateInFlightCargo_RunningWorkerCountingUnchanged(t *testing.T) {
	ship := newInFlightCargoTestShip(t, "TORWIND-7", 30, "contract-work-live")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{ship}}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "contract-work-live", ContainerType: "CONTRACT_WORKFLOW", Status: "RUNNING"}},
	}}
	handler := newInFlightCargoHandler(repo, containerRepo)

	got, err := handler.calculateInFlightCargo(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("calculateInFlightCargo: %v", err)
	}
	if got != 30 {
		t.Fatalf("expected 30 units in-flight from the running worker's ship, got %d", got)
	}
}

// Two distinct ships — one on a live RUNNING worker, one stuck on a dead
// FAILED one — must both contribute: the interrupted-ships source is added
// alongside the RUNNING-workers source, not a replacement for it.
func TestCalculateInFlightCargo_SumsRunningAndInterruptedShips(t *testing.T) {
	running := newInFlightCargoTestShip(t, "TORWIND-7", 15, "contract-work-live")
	interrupted := newInFlightCargoTestShip(t, "TORWIND-6", 10, "contract-work-dead")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{running, interrupted}}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "contract-work-live", ContainerType: "CONTRACT_WORKFLOW", Status: "RUNNING"}},
		"FAILED":  {{ID: "contract-work-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	handler := newInFlightCargoHandler(repo, containerRepo)

	got, err := handler.calculateInFlightCargo(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("calculateInFlightCargo: %v", err)
	}
	if got != 25 {
		t.Fatalf("expected 25 units (15 running + 10 interrupted), got %d", got)
	}
}

// readoptInterruptedDeliveries (sp-tgp5) moves a successfully re-adopted ship
// off its dead FAILED container and onto a fresh RUNNING one before the main
// loop — and therefore before calculateInFlightCargo — ever runs. The old
// dead container can still be listed as FAILED (its own row isn't cleaned up
// just because the ship that used to sit on it moved away), so the guard
// against double-counting has to come from the ship no longer matching that
// container's FindByContainer query, not from the container disappearing
// from the FAILED list. This asserts a re-adopted ship's cargo is counted
// exactly once — via the new RUNNING container — not twice, and not zero.
func TestCalculateInFlightCargo_ReadoptedShipCountedExactlyOnce(t *testing.T) {
	ship := newInFlightCargoTestShip(t, "TORWIND-6", 40, "contract-work-TORWIND-6-new")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{ship}}
	containerRepo := &reclaimFakeContainerRepo{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "contract-work-TORWIND-6-new", ContainerType: "CONTRACT_WORKFLOW", Status: "RUNNING"}},
		"FAILED":  {{ID: "contract-work-TORWIND-6-dead", ContainerType: "CONTRACT_WORKFLOW", Status: "FAILED"}},
	}}
	handler := newInFlightCargoHandler(repo, containerRepo)

	got, err := handler.calculateInFlightCargo(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("calculateInFlightCargo: %v", err)
	}
	if got != 40 {
		t.Fatalf("expected the re-adopted ship's 40 units counted exactly once, got %d", got)
	}
}
