package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	daemonPort "github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- test doubles (port boundaries only) -------------------------------------

type spawnShipSnapshot struct {
	containerID string
	assigned    bool
}

type spawnFakeShipRepo struct {
	navigation.ShipRepository

	ship    *navigation.Ship
	findErr error
	saveErr error
	saves   []spawnShipSnapshot
}

func (r *spawnFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.ship, nil
}

func (r *spawnFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, spawnShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	return nil
}

func (r *spawnFakeShipRepo) lastSave(t *testing.T) spawnShipSnapshot {
	t.Helper()
	if len(r.saves) == 0 {
		t.Fatalf("expected at least one ship Save, got none")
	}
	return r.saves[len(r.saves)-1]
}

type spawnFakeDaemonClient struct {
	daemon.DaemonClient

	persistErr error
	startErr   error

	persisted     []string
	started       []string
	stopped       []string
	persistedKind []daemon.ContainerKind
	startedKind   []daemon.ContainerKind
}

func (d *spawnFakeDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, id string, _ uint, _ interface{}) error {
	d.persisted = append(d.persisted, id)
	d.persistedKind = append(d.persistedKind, kind)
	return d.persistErr
}

func (d *spawnFakeDaemonClient) StartContainer(_ context.Context, kind daemon.ContainerKind, id string) error {
	d.started = append(d.started, id)
	d.startedKind = append(d.startedKind, kind)
	return d.startErr
}

func (d *spawnFakeDaemonClient) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	return nil
}

func newSpawnTestShip(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		100,
		40,
		cargo,
		9,
		"FRAME_FRIGATE",
		"SIPHON",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func newGasSpawnHandler(shipRepo navigation.ShipRepository, daemonClient *spawnFakeDaemonClient) *RunGasCoordinatorHandler {
	return &RunGasCoordinatorHandler{
		shipRepo:     shipRepo,
		daemonClient: daemonClient,
		clock:        shared.NewRealClock(),
	}
}

func gasSpawnCommand() *RunGasCoordinatorCommand {
	return &RunGasCoordinatorCommand{
		GasOperationID: "gas-op-1",
		PlayerID:       shared.MustNewPlayerID(1),
		GasGiant:       "X1-TEST-GG",
		ContainerID:    "gas-coordinator-1",
	}
}

// --- siphon worker pins ------------------------------------------------------

func TestSpawnSiphonWorker_HappyPath_PersistsAssignsStarts(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	if err := ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock()); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	id, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "siphon-worker-") {
		t.Fatalf("expected siphon-worker id prefix, got %q", id)
	}
	if len(daemon.persisted) != 1 || daemon.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemon.persisted)
	}
	if len(daemon.started) != 1 || daemon.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemon.started)
	}
	if daemon.persistedKind[0] != daemonPort.ContainerKindGasSiphonWorker || daemon.startedKind[0] != daemonPort.ContainerKindGasSiphonWorker {
		t.Fatalf("expected gas siphon worker kind, got persist=%v start=%v", daemon.persistedKind, daemon.startedKind)
	}
	if snap := repo.lastSave(t); snap.containerID != id || !snap.assigned {
		t.Fatalf("expected ship saved assigned to %q, got %+v", id, snap)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemon.stopped)
	}
}

func TestSpawnSiphonWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemon.started)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing to roll back), got %v", daemon.stopped)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no ship save when persist failed, got %v", repo.saves)
	}
}

func TestSpawnSiphonWorker_SaveFails_ContainerStoppedNoShipLeak(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship, saveErr: errors.New("save failed")}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when save fails")
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on save failure, got %v", daemon.stopped)
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected worker not started on save failure, got %v", daemon.started)
	}
}

func TestSpawnSiphonWorker_StartFails_ShipTransferredBackToCoordinator(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-1")
	_ = ship.AssignToContainer("gas-coordinator-1", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnSiphonWorker(context.Background(), gasSpawnCommand(), "AGENT-SIPHON-1")
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback returns the ship to the coordinator (no ship leak).
	if snap := repo.lastSave(t); snap.containerID != "gas-coordinator-1" || !snap.assigned {
		t.Fatalf("expected ship transferred back to coordinator, got %+v", snap)
	}
	// Current behavior: the persisted container is NOT stopped on start failure.
	if len(daemon.stopped) != 0 {
		t.Fatalf("pinned behavior: container not stopped on start failure, got %v", daemon.stopped)
	}
}

// --- storage ship worker pins ------------------------------------------------

func TestSpawnStorageShipWorker_HappyPath_PersistsAssignsStarts(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	id, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "storage-ship-") {
		t.Fatalf("expected storage-ship id prefix, got %q", id)
	}
	if len(daemon.persisted) != 1 || daemon.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemon.persisted)
	}
	if len(daemon.started) != 1 || daemon.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemon.started)
	}
	if daemon.persistedKind[0] != daemonPort.ContainerKindStorageShip || daemon.startedKind[0] != daemonPort.ContainerKindStorageShip {
		t.Fatalf("expected storage ship kind, got persist=%v start=%v", daemon.persistedKind, daemon.startedKind)
	}
	if snap := repo.lastSave(t); snap.containerID != id || !snap.assigned {
		t.Fatalf("expected ship saved assigned to %q, got %+v", id, snap)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemon.stopped)
	}
}

func TestSpawnStorageShipWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemon.started)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing persisted), got %v", daemon.stopped)
	}
}

func TestSpawnStorageShipWorker_SaveFails_ContainerStopped(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship, saveErr: errors.New("save failed")}
	daemon := &spawnFakeDaemonClient{}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when save fails")
	}
	if len(daemon.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on save failure, got %v", daemon.stopped)
	}
	if len(daemon.started) != 0 {
		t.Fatalf("expected worker not started on save failure, got %v", daemon.started)
	}
}

func TestSpawnStorageShipWorker_StartFails_ShipReleased(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-1")
	_ = ship.AssignToContainer("old-container", shared.NewRealClock())
	repo := &spawnFakeShipRepo{ship: ship}
	daemon := &spawnFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newGasSpawnHandler(repo, daemon)

	_, err := handler.spawnStorageShipWorker(context.Background(), gasSpawnCommand(), "AGENT-STORAGE-1")
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback force-releases the ship (no ship leak).
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected ship released on start failure, got %+v", snap)
	}
	if len(daemon.stopped) != 0 {
		t.Fatalf("pinned behavior: container not stopped on start failure, got %v", daemon.stopped)
	}
}
