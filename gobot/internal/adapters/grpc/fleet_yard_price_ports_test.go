package grpc

// Tests for the nearest-reachable-heavy-yard signal feeding the
// autosizer's YardPriceReader port: the HEAVY class may open on scout-scanned,
// gate-reachable yards when the live in-system walk finds no priced listing —
// and MUST stay fail-closed with no scan data (the historical behavior) and
// for every non-heavy class.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
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
// store holding candidates → PriceFor returns the ask at the nearest candidate
// WE ALREADY STAND ON, with readable=true, and reports the true cheapest across
// every candidate so the premium guard can do its designed job. The rank is asked
// from the fleet's occupied systems.
func TestYardPriceReader_HeavyFallsBackToScannedYards_WhenLiveWalkEmpty(t *testing.T) {
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_300_000, Hops: 1},
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_100_000, Hops: 3},
	}}
	r := &fleetYardPriceReader{
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			tradeShipAt(t, "TR-1", 1, "X1-HOME-A1"),
			// The buy needs a hull AT the counter, not merely a fleet somewhere: the cheaper
			// X1-FAR yard has nobody standing on it and is therefore not a target at any price.
			pairingHull(t, "BUYER-1", "X1-NEAR-Y1", ""),
		}},
		waypointRepo: &fakeYardWaypointLister{}, // no in-system shipyards → live walk finds nothing
		scannedYards: scanned,
	}

	price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.True(t, readable, "a known scanned yard we stand on must open the heavy price signal")
	require.Equal(t, int64(1_300_000), price, "price = the ask where a buyer already stands")
	require.Equal(t, "X1-NEAR-Y1", yard)
	require.Equal(t, int64(1_100_000), cheapest, "cheapest = true minimum across every candidate (premium guard input)")
	require.Equal(t, []string{"SHIP_HEAVY_FREIGHTER"}, scanned.gotTypes)
	require.ElementsMatch(t, []string{"X1-HOME", "X1-NEAR"}, scanned.gotFrom, "the rank must start from the fleet's occupied systems")
}

// With NO scan data (empty store) the heavy branch keeps its historical
// fail-closed behavior: readable=false, no price invented.
func TestYardPriceReader_Heavy_EmptyScanStore_StaysFailClosed(t *testing.T) {
	r := &fleetYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: &fakeScannedYards{}, // wired but empty — the pre-scan universe
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.False(t, readable, "no scan data ⇒ the heavy price guard must stay closed")

	// An unwired ranker (nil) is equally fail-closed — the pre-42ow wiring.
	r.scannedYards = nil
	_, _, _, readable, err = priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.NoError(t, err)
	require.False(t, readable)
}

// A FAILED READ IS NOT AN ABSENCE. It must reach the caller ON THE ERROR CHANNEL while the guard
// stays CLOSED; swallowed, an infrastructure fault reads as "no yard we stand on sells it".
func TestYardPriceReader_Heavy_StoreReadFailure_IsNotReportedAsAbsence(t *testing.T) {
	boom := errors.New("read shipyard inventory: context canceled")
	r := &fleetYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-NEAR-Y1", "")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: &fakeScannedYards{err: boom},
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.False(t, readable, "a failed read still fails CLOSED (RULINGS #4)")
	require.ErrorIs(t, err, boom, "a failed read must NOT be reported as a clean absence of yards")
}

// THE OTHER POPULATION, on the SAME fixture so only the store's answer varies: nothing scanned is a
// genuine absence and must keep reporting a NIL error, or the distinction above is worthless.
func TestYardPriceReader_Heavy_EmptyCandidateSet_IsACleanAbsence(t *testing.T) {
	r := &fleetYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-NEAR-Y1", "")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: &fakeScannedYards{},
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.False(t, readable, "an empty scan surface still fails CLOSED")
	require.NoError(t, err, "an absence of yards is not an infrastructure fault")
}

// The buyer roster is the other read PriceFor cannot proceed without, and it dies in the same
// instant for the same reason. It must report the same way.
func TestYardPriceReader_Heavy_RosterReadFailure_IsNotReportedAsAbsence(t *testing.T) {
	boom := errors.New("find ships by player: context canceled")
	r := &fleetYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{err: boom},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: &fakeScannedYards{},
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, "SHIP_HEAVY_FREIGHTER", true)
	require.False(t, readable, "an unreadable roster still fails CLOSED (RULINGS #4)")
	require.ErrorIs(t, err, boom, "an unreadable roster must NOT be reported as a clean absence of yards")
}

// The fallback is HEAVY-ONLY: a light-class miss must not consult the scanned
// store (lights buy in-system; opening remote yards for them is a policy change
// this seam explicitly does not make).
func TestYardPriceReader_LightClass_NeverConsultsScannedYards(t *testing.T) {
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 400_000, Hops: 1},
	}}
	r := &fleetYardPriceReader{
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeYardWaypointLister{},
		scannedYards: scanned,
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassLight, "SHIP_LIGHT_HAULER", true)
	require.NoError(t, err)
	require.False(t, readable, "a light-class miss must stay fail-closed")
	require.Zero(t, scanned.calls, "the scanned-yard store is a heavy-class signal only")
}
