package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// activeContractRepo makes NegotiateContract RESUME a pre-built active contract
// (its first call is FindActiveContracts — see contract_market_service.go), so a
// restart re-negotiates the SAME contract every pass without a mediator.
type activeContractRepo struct {
	domainContract.ContractRepository
	c *domainContract.Contract
}

func (r *activeContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*domainContract.Contract, error) {
	return []*domainContract.Contract{r.c}, nil
}

// inventoryFinderStub makes PlanSourcing succeed via the INVENTORY-first path
// (sourcing_optimizer.go returns the inventory plan before ever touching the
// market repo), so the multi-restart test reaches selection with no heavy market
// fake — exactly the seam sp-cwx5k calls out.
type inventoryFinderStub struct {
	good     string
	units    int
	waypoint string
}

func (s inventoryFinderStub) FindInSystemInventory(_ context.Context, _ int, _ string, good string) *appContract.InventorySource {
	if good != s.good {
		return nil
	}
	return &appContract.InventorySource{OperationID: "op-inv", StorageWaypoint: s.waypoint, UnitsAvailable: s.units}
}

// erroringGraphProvider fails any distance lookup. When a cargo-holder exists the
// coordinator MUST short-circuit to it and never reach SelectClosestShip's graph
// path — so in the green (fixed) run this is never called. If a future change
// removes the holder short-circuit, selection falls through to the distance path,
// hits this error, dispatches nothing, and the holder-assignment assertions below
// fail CLEANLY (a genuine regression lock, not a nil-panic).
type erroringGraphProvider struct {
	system.ISystemGraphProvider
}

func (erroringGraphProvider) GetGraph(_ context.Context, _ string, _ bool, _ int) (*system.GraphLoadResult, error) {
	return nil, errors.New("stub: distance selection must not be reached while a cargo-holder exists")
}

// newInTransitHolderShip builds a cargo HOLDER the daemon would leave after a
// restart that interrupted it mid-flight: unassigned (reclaimed to idle) yet
// still physically IN TRANSIT and holding its full contract load. In transit is
// the crux — FindIdleLightHaulers / FindIdleShipsByFleet both drop in-transit
// hulls, so the holder is INVISIBLE to normal candidate discovery and can only
// be chosen by the holder short-circuit (idleContractCargoHolder scans the full
// fleet). That is precisely the gap the short-circuit closed and this test locks.
func newInTransitHolderShip(t *testing.T, symbol string, units int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
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
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), location, fuel, 100, 80, cargo, 9,
		"FRAME_HAULER", "HAULER", nil, navigation.NavStatusInTransit)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// REGRESSION LOCK for sp-6v8in / sp-zve2q full restart idempotency (Admiral P0,
// validated live over 4 restarts incl. a mid-source one). Simulates the
// coordinator restarting REPEATEDLY with a cargo-holder present: on EVERY restart
// the SAME single hull (the holder TORWIND-D) is selected to complete its own
// load, and the closest EMPTY hull (TORWIND-9) is NEVER dispatched — zero second
// sourcing, no double-buy, across all N restarts.
//
// Each iteration is a fresh Handle invocation over a freshly-recovered fleet
// (the holder reclaimed to idle-in-transit still holding its 15 units), which is
// what a rapid restart leaves behind. The holder is IN TRANSIT so ordinary
// candidate discovery excludes it and only the holder short-circuit can pick it —
// so if that short-circuit ever regresses, selection falls to the (erroring)
// distance path, TORWIND-D is not claimed, and the per-restart assertions fail.
func TestFleetCoordinator_MultiRestart_HolderAlwaysServes_ZeroSecondSourcing(t *testing.T) {
	const restarts = 4
	// Shared across restarts so `started` accumulates every dispatched worker —
	// after N restarts it must hold exactly N, one per restart, all the holder.
	daemonClient := &spawnContractFakeDaemonClient{}

	for restart := 1; restart <= restarts; restart++ {
		holder := newInTransitHolderShip(t, "TORWIND-D", 15)                           // the recovered cargo holder (15 = full need)
		empty := newOrphanReclaimTestShip(t, "TORWIND-9", navigation.NavStatusInOrbit) // the tempting closest empty hull
		repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{holder, empty}}
		containerRepo := &reclaimFakeContainerRepo{} // no live/failed workers this recovered pass

		handler := &RunFleetCoordinatorHandler{
			workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
			contractMarketService:  contractServices.NewContractMarketService(nil, &activeContractRepo{c: fulfillVerifyContract(t, "C-1", "LIQUID_NITROGEN", 15, 0)}),
			shipRepo:               repo,
			daemonClient:           daemonClient,
			graphProvider:          erroringGraphProvider{}, // distance path must never be reached while the holder exists
			clock:                  &shared.MockClock{CurrentTime: time.Now()},
			eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
		}
		// Inventory-first sourcing: the holder's good is on hand in-system, so
		// PlanSourcing succeeds with no market fake and selection is reached.
		handler.SetInventoryFinder(inventoryFinderStub{good: "LIQUID_NITROGEN", units: 15, waypoint: "X1-TEST-A1"})

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, _ = handler.Handle(ctx, contractSpawnCommand())
		cancel()

		// Exactly ONE new worker was dispatched this restart (running total == restart) ...
		if got := len(daemonClient.started); got != restart {
			t.Fatalf("restart %d: expected exactly one worker dispatched this restart (%d total), got %d total: %v",
				restart, restart, got, daemonClient.started)
		}
		// ... it is the HOLDER completing its own load (claimed to the worker) ...
		if !holder.IsAssigned() {
			t.Fatalf("restart %d: the cargo holder TORWIND-D was NOT selected — the holder must always win selection over the closest empty hull (restart idempotency regression)", restart)
		}
		// ... and the empty closest hull was NEVER dispatched — zero second sourcing.
		if empty.IsAssigned() {
			t.Fatalf("restart %d: empty hull TORWIND-9 was dispatched — a SECOND sourcing (double-buy) slipped through on restart", restart)
		}
	}

	// Across all N restarts: one worker per restart, every one the holder.
	if len(daemonClient.started) != restarts {
		t.Fatalf("expected exactly %d workers across %d restarts (one holder dispatch each, zero duplicates), got %d: %v",
			restarts, restarts, len(daemonClient.started), daemonClient.started)
	}
}
