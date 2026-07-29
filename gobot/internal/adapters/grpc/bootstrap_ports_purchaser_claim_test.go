package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// recordingBuyMediator records every command the buy path dispatches, so a test can
// prove what the acquirer did — and, more importantly, what it did NOT do — at the
// driven-port boundary. The purchase itself is answered with a single bought hull so a
// permitted buy completes normally.
type recordingBuyMediator struct {
	common.Mediator
	mu   sync.Mutex
	sent []common.Request
}

func (m *recordingBuyMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.mu.Lock()
	m.sent = append(m.sent, request)
	m.mu.Unlock()

	if _, ok := request.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
		bought, err := navigation.NewShip(
			"TORWIND-99", shared.MustNewPlayerID(1),
			mustWaypoint("X1-HQ-YARD"), mustFuel(), 100, 40, mustCargo(), 30,
			"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusDocked,
		)
		if err != nil {
			return nil, err
		}
		return &shipyardCmd.BatchPurchaseShipsResponse{
			PurchasedShips: []*navigation.Ship{bought}, TotalCost: 1000, ShipsPurchasedCount: 1,
		}, nil
	}
	return nil, nil
}

func (m *recordingBuyMediator) purchaseAttempted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.sent {
		if _, ok := r.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
			return true
		}
	}
	return false
}

func mustWaypoint(symbol string) *shared.Waypoint {
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		panic(err)
	}
	return wp
}

func mustFuel() *shared.Fuel {
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		panic(err)
	}
	return fuel
}

func mustCargo() *shared.Cargo {
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		panic(err)
	}
	return cargo
}

func newBuyAcquirer(med common.Mediator, ships []*navigation.Ship) *bootstrapAcquirer {
	return &bootstrapAcquirer{
		med:      med,
		shipRepo: &fakeAskShipRepo{all: ships},
		lastAsks: map[askKey]int64{},
	}
}

// RULINGS #3 (single-writer ship state) and #7 ("Do not code around fleet pins,
// exclusivity, or the claim tx"). The bootstrap buy runs IN-PROCESS through the
// mediator — buyWith -> BatchPurchaseShipsCommand -> PurchaseShipCommand ->
// NavigateRouteCommand/DockShipCommand — and NOT one of those handlers consults the
// ship claim. So a buy issued against a hull another container is actively running
// flies it to a shipyard and docks it underneath the live worker, with no error
// anywhere: the loud "already assigned" collision the container path reports has a
// silent twin here, and a silent one never shows up in a failure count at all.
//
// buyWith's own FALLBACK search already refuses a held hull — it only takes hulls
// reading s.IsIdle(), which is false for anything ClaimShip'd (AssignToContainer makes
// the hull IsAssigned). The hole is the NAMED purchaser: when a caller names one,
// buyWith skips the search and every check in it. A named purchaser held by another
// container must be refused, not flown.
//
// Refusing can only PREVENT a spend, never enable one, so no money guard is touched
// (RULINGS #4 — strictly stricter).
func TestBootstrapBuyRefusesANamedPurchaserAnotherContainerIsRunning(t *testing.T) {
	frigate := newIdleTradeShip(t, "TORWIND-1", 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet)
	// The exact production holder: the frigate's continuous contract loop, mid-run.
	require.NoError(t, frigate.AssignToContainer("batch_contract_workflow-TORWIND-1-927f57d6", shared.NewRealClock()))

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate})

	_, err := acquirer.buyWith(context.Background(), 1, "SHIP_PROBE", "X1-HQ-YARD", "TORWIND-1")

	require.Error(t, err, "a hull another container is running must never be flown by the buy path")
	require.False(t, med.purchaseAttempted(),
		"the buy must be refused BEFORE it dispatches — the purchase path navigates and docks the purchaser")
	require.Equal(t, "batch_contract_workflow-TORWIND-1-927f57d6", frigate.ContainerID(),
		"the running container's claim must be left untouched")
}

// The buy's sibling leg has the SAME hole. EnsureShipyardReadable sends a hull to the
// home yard so the next tick's price read is not cold, and hullToSend's NAMED-purchaser
// branch returns that hull on nothing but its own position — no ownership question is
// asked at all. That navigate is the first half of the very sequence the live daemon
// logged under a bootstrap container holding no claim (navigate_command_sent ->
// opportunistic_refuel -> route_finished -> bootstrap_bought_hauler), so gating the buy
// while leaving this open would just move the silent write one step earlier.
//
// Declining is free: EnsureShipyardReadable is idempotent and best-effort by
// construction, so a contested hull simply is not sent this tick and the next tick
// re-derives the answer (RULINGS #2).
func TestShipyardScannerRefusesToFlyAHullAnotherContainerIsRunning(t *testing.T) {
	frigate := newIdleTradeShip(t, "TORWIND-1", 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet)
	require.NoError(t, frigate.AssignToContainer("batch_contract_workflow-TORWIND-1-927f57d6", shared.NewRealClock()))

	send, ok := hullToSend([]*navigation.Ship{frigate}, map[string]struct{}{"X1-HQ-YARD": {}}, "TORWIND-1")

	require.False(t, ok, "a hull a container is running must never be flown to the yard")
	require.Empty(t, send)
}

// A purchaser the roster does not carry cannot be shown to be free, so it is not flown
// either — the same fail-closed rule the buy gate applies.
func TestShipyardScannerRefusesAPurchaserMissingFromTheRoster(t *testing.T) {
	other := newIdleTradeShip(t, "TORWIND-8", 1)

	send, ok := hullToSend([]*navigation.Ship{other}, map[string]struct{}{"X1-HQ-YARD": {}}, "TORWIND-1")

	require.False(t, ok, "ownership that cannot be read cannot be cleared")
	require.Empty(t, send)
}

// The scanner must still do its job for a free named purchaser — the ordinary case the
// cold-start price read depends on.
func TestShipyardScannerStillFliesAFreeNamedPurchaser(t *testing.T) {
	frigate := newIdleTradeShip(t, "TORWIND-1", 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet) // free: no container claim

	send, ok := hullToSend([]*navigation.Ship{frigate}, map[string]struct{}{"X1-HQ-YARD": {}}, "TORWIND-1")

	require.True(t, ok)
	require.Equal(t, "TORWIND-1", send)
}

// The refusal must be scoped to hulls another writer owns — it must not break the
// purchase the whole purchasing dedication exists for. A named purchaser that is free
// (the ordinary case: the pivot released the frigate's loop-claim and it now stands by
// as the exclusive buy ship) still buys, and is NOT required to be un-dedicated: the
// purchasing tag is what NAMES it as the buyer.
func TestBootstrapBuyStillUsesAFreeNamedPurchaser(t *testing.T) {
	frigate := newIdleTradeShip(t, "TORWIND-1", 1)
	frigate.SetDedicatedFleet(navigation.PurchasingFleet) // held by no container

	med := &recordingBuyMediator{}
	acquirer := newBuyAcquirer(med, []*navigation.Ship{frigate})

	result, err := acquirer.buyWith(context.Background(), 1, "SHIP_PROBE", "X1-HQ-YARD", "TORWIND-1")

	require.NoError(t, err, "the exclusive purchasing frigate must still be able to buy when it is free")
	require.True(t, med.purchaseAttempted())
	require.Equal(t, "TORWIND-99", result.ShipSymbol)
}
