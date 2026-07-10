package commands

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-npyr: shipPoolRefresher's 30s discovery tick logged NOTHING when idle
// haulers were found fleet-wide but every one was already tracked in this
// factory's shipsUsed map - the common/expected steady state once a run's
// initial ships are in flight (shipPoolRefresher never DB-claims a ship
// itself; claiming happens later when a worker pulls it off shipPool and
// calls claimShipForFactory, so the ship stays IsIdle()==true at the DB level
// and FindIdleLightHaulers keeps re-reporting it every tick). From the
// outside this silence compounded the confusion around the real dwell (which
// lives in PollForProduction - see production_executor.go's
// productionDwellWarnThreshold, sp-npyr Fix 1): 30s-cadence "idle light
// haulers discovered" noise with zero explanation of why no task followed.
//
// shipPoolRefresher itself hardcodes a real 30-second time.NewTicker with no
// injectable interval, so its tick body is extracted into refreshShipPoolOnce
// - a directly-callable, directly-testable method - rather than testing the
// goroutine loop with real waits.
func TestRefreshShipPoolOnce_IdleShipsAlreadyTracked_LogsClarifyingReason(t *testing.T) {
	ship := newTestHauler(t, "CRAFTY-11", nil) // idle: never assigned to any container
	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{ship.ShipSymbol(): ship},
		order: []string{ship.ShipSymbol()},
	}
	marketRepo := &factoryFakeMarketRepo{}
	resolver := mfgServices.NewSupplyChainResolver(
		map[string][]string{testOutputGood: {testInputGood}},
		marketRepo,
	)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)
	handler := NewRunFactoryCoordinatorHandler(
		&factoryFakeMediator{}, shipRepo, marketRepo, resolver, marketLocator, &factoryFakeClock{},
		nil, // apiClient: pool-refresh path never buys, so the spend floor is irrelevant here
	)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	shipPool := make(chan *navigation.Ship, 2)
	// Pre-populate shipsUsed as if a prior tick already added this ship to the
	// pool - it stays DB-idle, so FindIdleLightHaulers keeps reporting it.
	shipsUsed := map[string]bool{ship.ShipSymbol(): true}
	var mu sync.Mutex

	handler.refreshShipPoolOnce(ctx, shared.MustNewPlayerID(1), testSystem, nil, shipPool, shipsUsed, &mu, 0)

	found := false
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "already claimed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a clarifying log when idle haulers were found but already tracked, got entries: %+v", entries)
	}
	if len(shipPool) != 0 {
		t.Fatalf("an already-tracked ship must not be re-added to the pool, got pool len %d", len(shipPool))
	}
}

// Regression coverage for the refreshShipPoolOnce extraction: the pre-existing
// "a genuinely new idle ship gets added to the pool" behavior must survive the
// refactor unchanged.
func TestRefreshShipPoolOnce_NewIdleShip_AddedToPoolAndLogged(t *testing.T) {
	ship := newTestHauler(t, "CRAFTY-12", nil)
	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{ship.ShipSymbol(): ship},
		order: []string{ship.ShipSymbol()},
	}
	marketRepo := &factoryFakeMarketRepo{}
	resolver := mfgServices.NewSupplyChainResolver(
		map[string][]string{testOutputGood: {testInputGood}},
		marketRepo,
	)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)
	handler := NewRunFactoryCoordinatorHandler(
		&factoryFakeMediator{}, shipRepo, marketRepo, resolver, marketLocator, &factoryFakeClock{},
		nil, // apiClient: pool-refresh path never buys, so the spend floor is irrelevant here
	)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	shipPool := make(chan *navigation.Ship, 2)
	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	discoveryCount := handler.refreshShipPoolOnce(ctx, shared.MustNewPlayerID(1), testSystem, nil, shipPool, shipsUsed, &mu, 0)

	if discoveryCount != 1 {
		t.Fatalf("expected discoveryCount incremented to 1 for one newly added ship, got %d", discoveryCount)
	}
	if len(shipPool) != 1 {
		t.Fatalf("expected the newly discovered idle ship added to the pool, got pool len %d", len(shipPool))
	}
	if !shipsUsed[ship.ShipSymbol()] {
		t.Fatal("expected the newly added ship recorded in shipsUsed")
	}

	found := false
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "Added new ships to pool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the existing 'Added new ships to pool' log preserved after extraction, got entries: %+v", entries)
	}
}
