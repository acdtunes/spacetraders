package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type contractShipSnapshot struct {
	containerID string
	assigned    bool
}

type spawnContractFakeShipRepo struct {
	navigation.ShipRepository

	ship    *navigation.Ship
	findErr error
	saveErr error
	saves   []contractShipSnapshot
}

func (r *spawnContractFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.ship, nil
}

func (r *spawnContractFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, contractShipSnapshot{containerID: ship.ContainerID(), assigned: ship.IsAssigned()})
	return nil
}

func (r *spawnContractFakeShipRepo) lastSave(t *testing.T) contractShipSnapshot {
	t.Helper()
	if len(r.saves) == 0 {
		t.Fatalf("expected at least one ship Save, got none")
	}
	return r.saves[len(r.saves)-1]
}

type spawnContractFakeDaemonClient struct {
	daemon.DaemonClient

	persistErr error
	startErr   error

	persisted     []string
	started       []string
	stopped       []string
	persistedKind []daemon.ContainerKind
	startedKind   []daemon.ContainerKind
}

func (d *spawnContractFakeDaemonClient) PersistContainer(_ context.Context, kind daemon.ContainerKind, id string, _ uint, _ interface{}) error {
	d.persisted = append(d.persisted, id)
	d.persistedKind = append(d.persistedKind, kind)
	return d.persistErr
}

func (d *spawnContractFakeDaemonClient) StartContainer(_ context.Context, kind daemon.ContainerKind, id string) error {
	d.started = append(d.started, id)
	d.startedKind = append(d.startedKind, kind)
	return d.startErr
}

func (d *spawnContractFakeDaemonClient) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	return nil
}

func newContractSpawnHandler(repo navigation.ShipRepository, daemonClient *spawnContractFakeDaemonClient) *RunFleetCoordinatorHandler {
	return &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, nil, repo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		clock:                  shared.NewRealClock(),
	}
}

func contractSpawnCommand() *RunFleetCoordinatorCommand {
	return &RunFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "fleet-coordinator-1",
	}
}

func TestSpawnContractWorker_HappyPath_PersistsAssignsStarts(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	id, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3")
	if err != nil {
		t.Fatalf("expected happy path, got error: %v", err)
	}
	if !strings.HasPrefix(id, "contract-work-") {
		t.Fatalf("expected contract-work id prefix, got %q", id)
	}
	if len(daemonClient.persisted) != 1 || daemonClient.persisted[0] != id {
		t.Fatalf("expected persist of %q, got %v", id, daemonClient.persisted)
	}
	if len(daemonClient.started) != 1 || daemonClient.started[0] != id {
		t.Fatalf("expected start of %q, got %v", id, daemonClient.started)
	}
	if daemonClient.persistedKind[0] != daemon.ContainerKindContractWorkflow || daemonClient.startedKind[0] != daemon.ContainerKindContractWorkflow {
		t.Fatalf("expected contract workflow kind, got persist=%v start=%v", daemonClient.persistedKind, daemonClient.startedKind)
	}
	if snap := repo.lastSave(t); snap.containerID != id || !snap.assigned {
		t.Fatalf("expected ship saved assigned to %q, got %+v", id, snap)
	}
	if len(daemonClient.stopped) != 0 {
		t.Fatalf("expected no StopContainer on happy path, got %v", daemonClient.stopped)
	}
}

func TestSpawnContractWorker_PersistFails_NothingToRollBack(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{persistErr: errors.New("db down")}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3")
	if err == nil {
		t.Fatalf("expected error when persist fails")
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("expected no start when persist failed, got %v", daemonClient.started)
	}
	if len(daemonClient.stopped) != 0 {
		t.Fatalf("expected no StopContainer (nothing to roll back), got %v", daemonClient.stopped)
	}
	if len(repo.saves) != 0 {
		t.Fatalf("expected no ship save when persist failed, got %v", repo.saves)
	}
}

func TestSpawnContractWorker_SaveFails_ContainerStoppedNoShipLeak(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship, saveErr: errors.New("save failed")}
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3")
	if err == nil {
		t.Fatalf("expected error when save fails")
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("expected container stopped exactly once on save failure, got %v", daemonClient.stopped)
	}
	if len(daemonClient.started) != 0 {
		t.Fatalf("expected worker not started on save failure, got %v", daemonClient.started)
	}
}

func TestSpawnContractWorker_StartFails_ShipReleased(t *testing.T) {
	ship := newNegotiateTestShip(t, navigation.NavStatusInOrbit)
	repo := &spawnContractFakeShipRepo{ship: ship}
	daemonClient := &spawnContractFakeDaemonClient{startErr: errors.New("start boom")}
	handler := newContractSpawnHandler(repo, daemonClient)

	_, err := handler.spawnContractWorker(context.Background(), contractSpawnCommand(), "TORWIND-3")
	if err == nil {
		t.Fatalf("expected error when start fails")
	}
	// Rollback releases the assignment so the ship returns to the idle pool.
	if snap := repo.lastSave(t); snap.assigned {
		t.Fatalf("expected ship released on start failure, got %+v", snap)
	}
	if len(daemonClient.stopped) != 1 {
		t.Fatalf("expected persisted container stopped exactly once on start failure, got %v", daemonClient.stopped)
	}
}
