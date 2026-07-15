package commands

// Acceptance tests for sp-42ow: the scout tour's market visit ALSO scans a
// co-located SHIPYARD (no extra trips, no new tour legs), persisting ship-type
// availability + prices to the shipyard-inventory store and emitting the
// one-time heavy-yard milestone. Port-to-port: enter through the ScoutTour
// driving port, assert at the inventory-store / captain-event driven ports.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// --- fakes at the port boundaries -------------------------------------------------

type fakeTourShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *fakeTourShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

type fakeMarketStore struct {
	upserts int
}

func (f *fakeMarketStore) GetMarketData(context.Context, string, int) (*market.Market, error) {
	return nil, nil
}

func (f *fakeMarketStore) UpsertMarketData(context.Context, uint, string, []market.TradeGood, time.Time) error {
	f.upserts++
	return nil
}

func (f *fakeMarketStore) ListMarketsInSystem(context.Context, uint, string, int) ([]market.Market, error) {
	return nil, nil
}

// fakeScanAPI answers the two live reads the piggybacked visit performs: the
// market scan (already-shipped behavior) and the shipyard scan under test.
type fakeScanAPI struct {
	domainPorts.APIClient
	shipyard     *domainPorts.ShipyardData
	shipyardErr  error
	shipyardGets int
}

func (a *fakeScanAPI) GetMarket(_ context.Context, _, waypointSymbol, _ string) (*domainPorts.MarketData, error) {
	return &domainPorts.MarketData{Symbol: waypointSymbol}, nil
}

func (a *fakeScanAPI) GetShipyard(_ context.Context, _, _, _ string) (*domainPorts.ShipyardData, error) {
	a.shipyardGets++
	return a.shipyard, a.shipyardErr
}

type fakeInventoryStore struct {
	replaced   map[string][]domainShipyard.ShipTypeAvailability // waypoint → last written set
	replaceErr error
}

func newFakeInventoryStore() *fakeInventoryStore {
	return &fakeInventoryStore{replaced: map[string][]domainShipyard.ShipTypeAvailability{}}
}

func (f *fakeInventoryStore) ReplaceScan(_ context.Context, _ int, _, waypointSymbol string, availabilities []domainShipyard.ShipTypeAvailability, _ time.Time) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaced[waypointSymbol] = availabilities
	return nil
}

func (f *fakeInventoryStore) HasAnyOfTypes(_ context.Context, _ int, shipTypes []string) (bool, error) {
	for _, rows := range f.replaced {
		for _, row := range rows {
			for _, t := range shipTypes {
				if row.ShipType == t {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeInventoryStore) ListByTypes(context.Context, int, []string) ([]domainShipyard.ShipTypeAvailability, error) {
	return nil, nil
}

type fakeEventRecorder struct {
	events []*captain.Event
}

func (f *fakeEventRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

// fakeWaypointTraits answers the immutable-trait check keyed by symbol.
type fakeWaypointTraits struct {
	waypoints map[string]*shared.Waypoint
}

func (f *fakeWaypointTraits) HasWaypointTrait(_ context.Context, waypointSymbol, trait string) (bool, error) {
	wp, ok := f.waypoints[waypointSymbol]
	if !ok {
		return false, nil
	}
	return wp.HasTrait(trait), nil
}

// --- fixture ----------------------------------------------------------------------

const scoutedYard = "X1-TEST-Y1"

func scoutAt(t *testing.T, waypoint string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(0, 0)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	scout, err := navigation.NewShip(
		"PROBE-1", shared.MustNewPlayerID(1), wp, fuel, 0, 0, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	return scout
}

func shipyardWaypoint(t *testing.T, symbol string, traits ...string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	require.NoError(t, err)
	wp.Traits = traits
	return wp
}

// tourFixture wires a stationary one-iteration scout tour at scoutedYard through
// the real handler + real scanners, with fakes only at the port boundaries.
func tourFixture(t *testing.T, api *fakeScanAPI, inventory *fakeInventoryStore, events *fakeEventRecorder, heavy domainShipyard.HeavyShipTypeSet) (*ScoutTourHandler, *ScoutTourCommand) {
	t.Helper()
	marketScanner := ship.NewMarketScanner(api, &fakeMarketStore{}, nil, nil)
	shipyardScanner := ship.NewShipyardScanner(
		api, inventory,
		&fakeWaypointTraits{waypoints: map[string]*shared.Waypoint{
			scoutedYard: shipyardWaypoint(t, scoutedYard, "MARKETPLACE", "SHIPYARD"),
		}},
		events, heavy,
	)
	h := NewScoutTourHandler(&fakeTourShipRepo{ship: scoutAt(t, scoutedYard)}, nil, marketScanner, shipyardScanner, &shared.MockClock{CurrentTime: time.Now()})
	cmd := &ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ShipSymbol:         "PROBE-1",
		Markets:            []string{scoutedYard},
		Iterations:         1,
		StartJitterMaxSecs: 1, // stableJitter < 1s on MockClock — no real wall time
	}
	return h, cmd
}

func tourCtx() context.Context {
	return common.WithPlayerToken(context.Background(), "test-token")
}

// --- acceptance: shipyard scan persisted on the tour visit ------------------------

// GIVEN a scout standing at a MARKETPLACE+SHIPYARD waypoint
// WHEN its stationary tour performs the market visit
// THEN the shipyard's ship-type availability AND prices are persisted to the
// shipyard-inventory store in the same visit (no extra trips), and the first
// heavy discovery of the era emits exactly one milestone captain event.
func TestScoutTour_MarketVisitAlsoPersistsShipyardInventory_AndEmitsHeavyMilestone(t *testing.T) {
	api := &fakeScanAPI{shipyard: &domainPorts.ShipyardData{
		Symbol: scoutedYard,
		ShipTypes: []domainPorts.ShipTypeInfo{
			{Type: "SHIP_PROBE"}, {Type: "SHIP_HEAVY_FREIGHTER"},
		},
		Ships: []domainPorts.ShipListingData{
			{Type: "SHIP_PROBE", PurchasePrice: 25_000, Supply: "HIGH"},
			{Type: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_250_000, Supply: "MODERATE"},
		},
	}}
	inventory := newFakeInventoryStore()
	events := &fakeEventRecorder{}
	h, cmd := tourFixture(t, api, inventory, events, domainShipyard.NewHeavyShipTypeSet(nil))

	_, err := h.Handle(tourCtx(), cmd)
	require.NoError(t, err)

	rows := inventory.replaced[scoutedYard]
	require.Len(t, rows, 2, "both listed ship types must be persisted for the scanned yard")
	bySymbol := map[string]domainShipyard.ShipTypeAvailability{}
	for _, r := range rows {
		bySymbol[r.ShipType] = r
	}
	require.Equal(t, 1_250_000, bySymbol["SHIP_HEAVY_FREIGHTER"].PurchasePrice)
	require.Equal(t, "MODERATE", bySymbol["SHIP_HEAVY_FREIGHTER"].Supply)
	require.Equal(t, 25_000, bySymbol["SHIP_PROBE"].PurchasePrice)
	require.Equal(t, scoutedYard, bySymbol["SHIP_PROBE"].WaypointSymbol)
	require.Equal(t, "X1-TEST", bySymbol["SHIP_PROBE"].SystemSymbol)

	require.Len(t, events.events, 1, "first heavy-yard discovery must emit exactly one milestone event")
	require.Equal(t, captain.EventHeavyYardDiscovered, events.events[0].Type)
	require.Equal(t, 1, events.events[0].PlayerID)
	require.Contains(t, events.events[0].Payload, "SHIP_HEAVY_FREIGHTER")
	require.Contains(t, events.events[0].Payload, scoutedYard)
}

// GIVEN the shipyard read fails at the API port
// WHEN the tour performs the same visit
// THEN the tour still completes green (market scan intact) — the piggybacked
// shipyard scan is strictly non-fatal to the hosting tour.
func TestScoutTour_ShipyardScanFailure_DoesNotFailTheTour(t *testing.T) {
	api := &fakeScanAPI{shipyardErr: errors.New("shipyard API 500")}
	inventory := newFakeInventoryStore()
	events := &fakeEventRecorder{}
	h, cmd := tourFixture(t, api, inventory, events, domainShipyard.NewHeavyShipTypeSet(nil))

	resp, err := h.Handle(tourCtx(), cmd)
	require.NoError(t, err, "a shipyard scan failure must never fail the hosting tour")
	require.Equal(t, 1, resp.(*ScoutTourResponse).MarketsVisited, "the market visit itself must still count")
	require.Empty(t, inventory.replaced, "nothing persisted on a failed shipyard read")
	require.Empty(t, events.events)
}
