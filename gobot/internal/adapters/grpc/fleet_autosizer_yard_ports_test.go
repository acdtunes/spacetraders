package grpc

// Tests for the sp-42ow nearest-reachable-heavy-yard signal feeding the
// autosizer's YardPriceReader port: the HEAVY class may open on scout-scanned,
// gate-reachable yards when the live in-system walk finds no priced listing —
// and MUST stay fail-closed with no scan data (the historical behavior) and
// for every non-heavy class.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeYardWaypointLister struct {
	waypoints []*shared.Waypoint
}

func (f *fakeYardWaypointLister) ListBySystemWithTrait(context.Context, string, string) ([]*shared.Waypoint, error) {
	return f.waypoints, nil
}

type fakeScannedYards struct {
	candidates []shipyardQueries.YardCandidate
	err        error
	calls      int
	gotTypes   []string
	gotFrom    []string
}

func (f *fakeScannedYards) NearestYardsSelling(_ context.Context, _ int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error) {
	f.calls++
	f.gotTypes = shipTypes
	f.gotFrom = fromSystems
	return f.candidates, f.err
}

// The heavy branch OPENS on the scanned-yard signal: live walk empty, scan
// store holding reachable candidates → PriceFor returns the nearest candidate's
// price + yard with readable=true, and reports the true cheapest across
// candidates so the premium guard can do its designed job. The rank is asked
// from the fleet's occupied systems.
func TestYardPriceReader_HeavyFallsBackToScannedYards_WhenLiveWalkEmpty(t *testing.T) {
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_300_000, Hops: 1},
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_100_000, Hops: 3},
	}}
	r := &autosizerYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeYardWaypointLister{}, // no in-system shipyards → live walk finds nothing
		scannedYards: scanned,
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, fleetCmd.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.True(t, readable, "a known, reachable scanned yard must open the heavy price signal")
	require.Equal(t, int64(1_300_000), price, "price = the NEAREST candidate's ask (hops dominate)")
	require.Equal(t, "X1-NEAR-Y1", yard)
	require.Equal(t, int64(1_100_000), cheapest, "cheapest = true minimum across reachable candidates (premium guard input)")
	require.Equal(t, []string{"SHIP_HEAVY_FREIGHTER"}, scanned.gotTypes)
	require.Equal(t, []string{"X1-HOME"}, scanned.gotFrom, "the rank must start from the fleet's occupied systems")
}

// With NO scan data (empty store) the heavy branch keeps its historical
// fail-closed behavior: readable=false, no price invented.
func TestYardPriceReader_Heavy_EmptyScanStore_StaysFailClosed(t *testing.T) {
	r := &autosizerYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: &fakeScannedYards{}, // wired but empty — the pre-scan universe
	}

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, fleetCmd.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.False(t, readable, "no scan data ⇒ the heavy price guard must stay closed")

	// An unwired ranker (nil) is equally fail-closed — the pre-42ow wiring.
	r.scannedYards = nil
	_, _, _, readable, err = r.PriceFor(context.Background(), 1, fleetCmd.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.False(t, readable)
}

// The fallback is HEAVY-ONLY: a light-class miss must not consult the scanned
// store (lights buy in-system; opening remote yards for them is a policy change
// this seam explicitly does not make).
func TestYardPriceReader_LightClass_NeverConsultsScannedYards(t *testing.T) {
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 400_000, Hops: 1},
	}}
	r := &autosizerYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: scanned,
	}

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, fleetCmd.HullClassLight, "SHIP_LIGHT_HAULER", true)
	require.NoError(t, err)
	require.False(t, readable, "a light-class miss must stay fail-closed")
	require.Zero(t, scanned.calls, "the scanned-yard store is a heavy-class signal only")
}
