package ship_test

// TestExecuteRoute_ArrivalAtShipyardOnlyWaypoint_PersistsRow_AndReachesHeavyEvent drives
// the real production driving port (RouteExecutor.ExecuteRoute) to an arrival at a
// SHIPYARD-trait waypoint that carries NO marketplace trait, and asserts the shipyard
// scan still fires: a row is persisted AND the heavy-yard discovery event path is
// reached. Doubles sit ONLY at the port boundaries (the external shipyard API, the
// inventory store, the immutable-trait reader, the captain event outbox).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// --- doubles at the port boundaries ---------------------------------------

// heavyShipyardAPI answers the one live shipyard read with a heavy-freighter yard,
// so the SAME visit that proves the row persists also proves the heavy-yard event
// path is reachable off the decoupled (non-marketplace) arrival.
type heavyShipyardAPI struct {
	data *domainPorts.ShipyardData
	gets int
}

func (a *heavyShipyardAPI) GetShipyard(context.Context, string, string, string) (*domainPorts.ShipyardData, error) {
	a.gets++
	return a.data, nil
}

// fakeVisitInventory records the persisted row set per waypoint and reports the
// era as heavy-free (HasAnyOfTypes false) so a heavy hit reads as the FIRST
// discovery — the milestone predicate the scanner emits the event on.
type fakeVisitInventory struct {
	replaced map[string][]shipyard.ShipTypeAvailability
}

func newFakeVisitInventory() *fakeVisitInventory {
	return &fakeVisitInventory{replaced: map[string][]shipyard.ShipTypeAvailability{}}
}

func (r *fakeVisitInventory) ReplaceScan(_ context.Context, _ int, _, waypointSymbol string, a []shipyard.ShipTypeAvailability, _ time.Time) error {
	r.replaced[waypointSymbol] = a
	return nil
}

func (r *fakeVisitInventory) HasAnyOfTypes(context.Context, int, []string) (bool, error) {
	return false, nil
}

func (r *fakeVisitInventory) ListByTypes(context.Context, int, []string) ([]shipyard.ShipTypeAvailability, error) {
	return nil, nil
}

// shipyardTraitReader answers the scanner's immutable-trait gate: only the yard
// symbol bears SHIPYARD.
type shipyardTraitReader struct {
	yard string
}

func (s *shipyardTraitReader) HasWaypointTrait(_ context.Context, waypointSymbol, trait string) (bool, error) {
	return waypointSymbol == s.yard && trait == "SHIPYARD", nil
}

// spyEvents records every captain event the scanner emits.
type spyEvents struct {
	events []*captain.Event
}

func (s *spyEvents) Record(_ context.Context, e *captain.Event) error {
	s.events = append(s.events, e)
	return nil
}

func TestExecuteRoute_ArrivalAtShipyardOnlyWaypoint_PersistsRow_AndReachesHeavyEvent(t *testing.T) {
	const yard = "X1-DEEP-YARD"
	api := &heavyShipyardAPI{data: &domainPorts.ShipyardData{
		Symbol: yard,
		ShipTypes: []domainPorts.ShipTypeInfo{
			{Type: "SHIP_PROBE"}, {Type: "SHIP_HEAVY_FREIGHTER"},
		},
		Ships: []domainPorts.ShipListingData{
			{Type: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_250_000, Supply: "MODERATE"},
		},
	}}
	inventory := newFakeVisitInventory()
	events := &spyEvents{}
	scanner := ship.NewShipyardScanner(api, inventory, &shipyardTraitReader{yard: yard}, events, shipyard.NewHeavyShipTypeSet(nil))

	// A single-leg route whose destination bears the SHIPYARD trait but NOT
	// MARKETPLACE — a charted-but-un-toured shipyard.
	from := mustTestWaypoint(t, "X1-DEEP-A", 0, 0)
	to := mustTestWaypoint(t, yard, 10, 0)
	to.Traits = []string{"SHIPYARD"}
	segment := domainNavigation.NewRouteSegment(from, to, 10, 10, 0, shared.FlightModeCruise, false)

	route, err := domainNavigation.NewRoute("route-deep-1", "SCOUT-1", 2, []*domainNavigation.RouteSegment{segment}, 400, false)
	require.NoError(t, err)

	playerID := shared.MustNewPlayerID(2)
	shipEntity := newScoutShip(t, from, playerID)

	executor := ship.NewRouteExecutor(
		nil,
		&succeedingMediator{fuelCapacity: 400},
		nil,
		nil, // no market scanner — the shipyard scan must fire on its OWN gate
		scanner,
		nil,
		nil,
		noopSubscriber{},
	)

	ctx := common.WithPlayerToken(context.Background(), "test-token")
	require.NoError(t, executor.ExecuteRoute(ctx, route, shipEntity, playerID))

	require.Equal(t, 1, api.gets, "arriving at a SHIPYARD-only waypoint must trigger one live shipyard read even though it is not a marketplace")

	rows := inventory.replaced[yard]
	require.Len(t, rows, 2, "the shipyard scan must persist a row set for the non-marketplace shipyard visit")
	byType := map[string]shipyard.ShipTypeAvailability{}
	for _, r := range rows {
		byType[r.ShipType] = r
	}
	require.Equal(t, 1_250_000, byType["SHIP_HEAVY_FREIGHTER"].PurchasePrice)

	require.Len(t, events.events, 1, "a heavy listing scanned on the decoupled visit must reach the heavy_yard_discovered event")
	require.Equal(t, captain.EventHeavyYardDiscovered, events.events[0].Type)
	require.Contains(t, events.events[0].Payload, "SHIP_HEAVY_FREIGHTER")
}
