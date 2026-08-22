package commands

import (
	"context"
	"testing"
	"time"

	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Mirrors TestHasRegularHaulerCandidate's style: pins the pure predicate the settle-window
// gate is built from, independent of the main loop.
func TestGeneralPoolCommandDraft(t *testing.T) {
	command := newCommandFrigateTestShip(t) // undedicated COMMAND hull

	dedicatedCommand := newCommandFrigateTestShip(t)
	dedicatedCommand.SetDedicatedFleet(dedicatedFleetContract) // a standing pin, never last-resort

	hauler := newHaulerShip(t, "TORWIND-3", "")

	tests := []struct {
		name     string
		entities []*navigation.Ship
		selected string
		want     bool
	}{
		{
			name:     "undedicated command hull selected out of the general pool",
			entities: []*navigation.Ship{command},
			selected: command.ShipSymbol(),
			want:     true,
		},
		{
			name:     "a regular hauler selected is never a command draft",
			entities: []*navigation.Ship{hauler},
			selected: hauler.ShipSymbol(),
			want:     false,
		},
		{
			name:     "a dedicated command hull is a standing assignment, not last-resort",
			entities: []*navigation.Ship{dedicatedCommand},
			selected: dedicatedCommand.ShipSymbol(),
			want:     false,
		},
		{
			name:     "selected ship absent from the general pool (depot route or holder)",
			entities: []*navigation.Ship{command},
			selected: "SOME-OTHER-SHIP",
			want:     false,
		},
		{
			name:     "empty general pool",
			entities: nil,
			selected: command.ShipSymbol(),
			want:     false,
		},
		{
			name:     "command hull alongside a hauler, hauler selected",
			entities: []*navigation.Ship{command, hauler},
			selected: hauler.ShipSymbol(),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generalPoolCommandDraft(tt.entities, tt.selected)
			if got != tt.want {
				t.Fatalf("generalPoolCommandDraft() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Pins the bounded-window arithmetic directly: blocks while a tracked entry is in the future,
// clears once "now" passes it, so the hull can never be benched indefinitely.
func TestCommandFrigateCooldownRemaining(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	t.Run("no tracked entry is immediately draftable", func(t *testing.T) {
		cooldowns := map[string]time.Time{}
		if got := commandFrigateCooldownRemaining(cooldowns, "TORWIND-1", now); got != 0 {
			t.Fatalf("expected 0 remaining with no tracked entry, got %s", got)
		}
	})

	t.Run("an entry in the future blocks for exactly the remaining span", func(t *testing.T) {
		cooldowns := map[string]time.Time{"TORWIND-1": now.Add(30 * time.Second)}
		if got := commandFrigateCooldownRemaining(cooldowns, "TORWIND-1", now); got != 30*time.Second {
			t.Fatalf("expected exactly 30s remaining, got %s", got)
		}
	})

	t.Run("an entry exactly at now is clear", func(t *testing.T) {
		cooldowns := map[string]time.Time{"TORWIND-1": now}
		if got := commandFrigateCooldownRemaining(cooldowns, "TORWIND-1", now); got != 0 {
			t.Fatalf("expected 0 remaining exactly at the boundary, got %s", got)
		}
	})

	t.Run("an entry in the past is clear", func(t *testing.T) {
		cooldowns := map[string]time.Time{"TORWIND-1": now.Add(-time.Second)}
		if got := commandFrigateCooldownRemaining(cooldowns, "TORWIND-1", now); got != 0 {
			t.Fatalf("expected 0 remaining once elapsed, got %s", got)
		}
	})

	t.Run("a tracked entry for a different ship never blocks this one", func(t *testing.T) {
		cooldowns := map[string]time.Time{"OTHER-SHIP": now.Add(time.Hour)}
		if got := commandFrigateCooldownRemaining(cooldowns, "TORWIND-1", now); got != 0 {
			t.Fatalf("expected 0 remaining for an untracked symbol, got %s", got)
		}
	})

	t.Run("the settle window clears a full bootstrap poll cadence", func(t *testing.T) {
		// Pinned as a literal, not imported: the contract engine stays phase-blind to
		// bootstrap by construction.
		bootstrapPollPeriod := 45 * time.Second
		if commandFrigateLastResortSettleWindow < 2*bootstrapPollPeriod {
			t.Fatalf("settle window (%s) must be at least 2x the poll cadence (%s), got margin %s",
				commandFrigateLastResortSettleWindow, bootstrapPollPeriod, commandFrigateLastResortSettleWindow-bootstrapPollPeriod)
		}
	})
}

// settleWindowDaemonClient wraps spawnContractFakeDaemonClient and signals every
// StartContainer call on a buffered channel, so a concurrent goroutine can observe a dispatch
// without racing the underlying started slice. Assertions read .started only after Handle
// returns.
type settleWindowDaemonClient struct {
	*spawnContractFakeDaemonClient
	startedSignal chan string
}

func newSettleWindowDaemonClient() *settleWindowDaemonClient {
	return &settleWindowDaemonClient{
		spawnContractFakeDaemonClient: &spawnContractFakeDaemonClient{},
		startedSignal:                 make(chan string, 8),
	}
}

func (d *settleWindowDaemonClient) StartContainer(ctx context.Context, kind daemon.ContainerKind, id string) error {
	err := d.spawnContractFakeDaemonClient.StartContainer(ctx, kind, id)
	d.startedSignal <- id
	return err
}

// settleWindowGraph co-locates the ship, the inventory source and the delivery destination at
// X1-TEST-A1 (matching newCommandFrigateTestShip and fulfillVerifyContract), so
// SelectClosestShip resolves a trivial zero-distance pick.
func settleWindowGraph(t *testing.T) *system.NavigationGraph {
	t.Helper()
	wp, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	return &system.NavigationGraph{Waypoints: map[string]*shared.Waypoint{"X1-TEST-A1": wp}}
}

func settleWindowHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), location, fuel, 400, 80, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	ship.SetDedicatedFleet(dedicatedFleetContract)
	return ship
}

// Drives the real Handle() loop through a full last-resort leg (negotiate -> plan -> select ->
// spawn), simulates it completing, and asserts the coordinator does not immediately draft a
// second leg — it waits, and the frigate is left idle for bootstrap to reclaim.
func TestFleetCoordinator_CommandFrigateLastResort_NotRedraftedDuringSettleWindow(t *testing.T) {
	frigate := newCommandFrigateTestShip(t) // idle, undedicated COMMAND hull at X1-TEST-A1
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate}}
	daemonClient := newSettleWindowDaemonClient()
	containerRepo := &reclaimFakeContainerRepo{}
	workerCh := make(chan navigation.WorkerCompletedEvent)
	mockClock := &shared.MockClock{CurrentTime: time.Now()}

	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, &activeContractRepo{c: fulfillVerifyContract(t, "C-1", "LIQUID_NITROGEN", 500, 0)}),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		graphProvider:          &placementStubGraphProvider{graph: settleWindowGraph(t)},
		clock:                  mockClock,
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: workerCh},
	}
	// Inventory-first sourcing (zero-ask) so the defer projection never parks the contract.
	handler.SetInventoryFinder(inventoryFinderStub{good: "LIQUID_NITROGEN", units: 500, waypoint: "X1-TEST-A1"})

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = handler.Handle(ctx, contractSpawnCommand())
		close(done)
	}()

	// Pass 1: the frigate is the only hull, so it is correctly drafted as the last resort.
	select {
	case <-daemonClient.startedSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first (last-resort) dispatch")
	}

	// Release before publishing completion, mirroring ContainerRunner's own ordering.
	frigate.ForceRelease("completed", shared.NewRealClock())
	select {
	case workerCh <- navigation.WorkerCompletedEvent{ShipSymbol: frigate.ShipSymbol(), Success: true}:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle never consumed the completion event")
	}

	select {
	case id := <-daemonClient.startedSignal:
		t.Fatalf("the frigate was redrafted immediately after its last-resort leg completed (zero gap); second dispatch: %s", id)
	case <-done:
		// Handle returned (context expired) without a second dispatch: correct.
	}

	if got := len(daemonClient.started); got != 1 {
		t.Fatalf("expected exactly ONE dispatch within the settle window, got %d: %v", got, daemonClient.started)
	}
	if frigate.IsAssigned() {
		t.Fatalf("the frigate must be IDLE during the settle window, got assigned to container %q", frigate.ContainerID())
	}
}

// No regression: a genuinely dedicated contract hauler (EXCLUSIVE MODE, never the command
// hull) must dispatch leg after leg with no added wait.
func TestFleetCoordinator_DedicatedHauler_TwoLegsBackToBack_NoSettleWindowDelay(t *testing.T) {
	hauler := settleWindowHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{hauler}}
	daemonClient := newSettleWindowDaemonClient()
	containerRepo := &reclaimFakeContainerRepo{}
	workerCh := make(chan navigation.WorkerCompletedEvent)
	mockClock := &shared.MockClock{CurrentTime: time.Now()}

	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, &activeContractRepo{c: fulfillVerifyContract(t, "C-1", "LIQUID_NITROGEN", 500, 0)}),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		graphProvider:          &placementStubGraphProvider{graph: settleWindowGraph(t)},
		clock:                  mockClock,
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: workerCh},
	}
	handler.SetInventoryFinder(inventoryFinderStub{good: "LIQUID_NITROGEN", units: 500, waypoint: "X1-TEST-A1"})

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = handler.Handle(ctx, contractSpawnCommand())
		close(done)
	}()

	select {
	case <-daemonClient.startedSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first dispatch")
	}
	hauler.ForceRelease("completed", shared.NewRealClock())
	select {
	case workerCh <- navigation.WorkerCompletedEvent{ShipSymbol: hauler.ShipSymbol(), Success: true}:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle never consumed leg 1's completion event")
	}

	select {
	case <-daemonClient.startedSignal:
		// Correct: the second leg dispatched with no added delay.
	case <-done:
		t.Fatal("Handle returned without a second dispatch")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the second (back-to-back) dispatch")
	}

	cancel()
	<-done

	if got := len(daemonClient.started); got != 2 {
		t.Fatalf("expected exactly TWO back-to-back dispatches, got %d: %v", got, daemonClient.started)
	}
}
