package queries

// The HEAVY TARGET suite (sp-fwk8z): the single shared definition of "which heavy yard would we
// buy at, and what does it ask" that both the fleet autosizer and the sensing buy-floor consume.
//
// TEST BUDGET (behaviour-first): 4 distinct behaviours × 2 = 8 unit tests maximum.
//
//	B1 an availability-only (unpriced) catalogue row alone opens the capability
//	B2 the target is the NEAREST reachable priced yard — presence is never required
//	B3 the class preference mirrors the buy path (preferred class first, then fall back)
//	B4 an unreadable input is an error, never a silent zero

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

type fakeAvailability struct {
	open bool
	err  error
	// types records what the finder asked about, so a caller that lost its heavy set is visible.
	types []string
}

func (f *fakeAvailability) HasAnyOfTypes(_ context.Context, _ int, shipTypes []string) (bool, error) {
	f.types = shipTypes
	return f.open, f.err
}

type fakeRanker struct {
	byType map[string][]YardCandidate
	err    error
	asked  []string
	from   []string
}

func (f *fakeRanker) NearestYardsSelling(_ context.Context, _ int, shipTypes []string, fromSystems []string) ([]YardCandidate, error) {
	f.asked = append(f.asked, shipTypes...)
	f.from = fromSystems
	if f.err != nil {
		return nil, f.err
	}
	if len(shipTypes) != 1 {
		return nil, errors.New("the finder must rank ONE class at a time, mirroring the buy path")
	}
	return f.byType[shipTypes[0]], nil
}

type fakeFleet struct {
	ships []*navigation.Ship
	err   error
}

func (f *fakeFleet) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.ships, f.err
}

func hullAt(symbol, waypoint string) *navigation.Ship {
	pid, _ := shared.NewPlayerID(5)
	wp := &shared.Waypoint{Symbol: waypoint, SystemSymbol: shared.ExtractSystemSymbol(waypoint)}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		panic(err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
	if err != nil {
		panic(err)
	}
	ship, err := navigation.NewShip(symbol, pid, wp, fuel, 100, 80, cargo,
		30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit)
	if err != nil {
		panic(err)
	}
	return ship
}

func candidate(waypoint string, price, hops int) YardCandidate {
	return YardCandidate{
		SystemSymbol: shared.ExtractSystemSymbol(waypoint), WaypointSymbol: waypoint,
		ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: price, Hops: hops,
	}
}

// B1 — an UNPRICED catalogue row alone opens the capability.
//
// This is the production shape and the reason the feature stalled: a heavy yard read without
// presence persists at purchase_price 0, so every priced read reports "nothing found" while the
// yard is plainly known. HasAnyOfTypes carries no price predicate, so the capability must open on
// the availability row alone — and the target must still report NOT PRICED, because a zero can
// never reach a money guard.
func TestHeavyTarget_UnpricedCatalogueRowOpensTheCapabilityWithoutAPrice(t *testing.T) {
	availability := &fakeAvailability{open: true} // the row exists; it just has no ask
	ranker := &fakeRanker{}                       // every priced rank is empty — nothing is priceable
	finder := NewHeavyTargetFinder(availability, ranker, &fakeFleet{ships: []*navigation.Ship{hullAt("TORWIND-A", "X1-HOME-A1")}}, nil)

	target, err := finder.HeavyTarget(context.Background(), 5)

	require.NoError(t, err)
	require.True(t, target.CapabilityOpen, "an availability-only row must open the capability on its own")
	require.False(t, target.Priced, "an availability-only row must never report a usable price")
	require.Zero(t, target.PurchasePrice)
	require.Equal(t, shipyard.DefaultHeavyShipTypes, availability.types, "an empty type set must fall back to the documented heavy classes")
}

// B1 (second half) — no known heavy yard at all closes the capability, and the priced rank is
// never even consulted. This is what tells "there is nothing to save toward" apart from "there is
// something to save toward that we cannot yet price".
func TestHeavyTarget_NoKnownHeavyYardClosesTheCapability(t *testing.T) {
	ranker := &fakeRanker{byType: map[string][]YardCandidate{
		"SHIP_HEAVY_FREIGHTER": {candidate("X1-QR78-AE4F", 1_565_500, 3)},
	}}
	finder := NewHeavyTargetFinder(&fakeAvailability{open: false}, ranker, &fakeFleet{}, nil)

	target, err := finder.HeavyTarget(context.Background(), 5)

	require.NoError(t, err)
	require.False(t, target.CapabilityOpen)
	require.False(t, target.Priced)
	require.Empty(t, ranker.asked, "a closed capability must not cost a yard rank")
}

// B2 — the target is the NEAREST reachable priced yard, and PRESENCE IS NEVER REQUIRED.
//
// The fleet stands only in X1-HOME. Both candidate yards are elsewhere, and the nearer one is the
// DEARER one — so a "cheapest wins" selection would pick the far bargain. It must not: the
// purchase path buys at the nearest yard, and the reservation has to hold back what that yard
// asks or treasury tops out below the price we will actually be quoted.
func TestHeavyTarget_PicksTheNearestPricedYardWithNoPresenceAndNotTheCheapest(t *testing.T) {
	ranker := &fakeRanker{byType: map[string][]YardCandidate{
		"SHIP_HEAVY_FREIGHTER": {
			candidate("X1-QR78-AE4F", 2_400_000, 2), // nearest, dearer — the yard we will buy at
			candidate("X1-FAR99-ZZ9Z", 1_565_500, 6),
		},
	}}
	finder := NewHeavyTargetFinder(&fakeAvailability{open: true}, ranker,
		&fakeFleet{ships: []*navigation.Ship{hullAt("TORWIND-A", "X1-HOME-A1")}}, nil)

	target, err := finder.HeavyTarget(context.Background(), 5)

	require.NoError(t, err)
	require.True(t, target.Priced)
	require.Equal(t, "X1-QR78-AE4F", target.WaypointSymbol, "the target is the nearest reachable yard, never the cheapest")
	require.Equal(t, int64(2_400_000), target.PurchasePrice, "the reservation must track the price we will be asked, not a cheaper one elsewhere")
	require.Equal(t, []string{"X1-HOME"}, ranker.from, "reach is measured from the systems the fleet actually holds")
}

// B3 — class preference mirrors the buy path: the preferred heavy class is asked FIRST, and a
// later class is only reached when the preferred one cannot be priced anywhere. Ranking the whole
// heavy set at once would return a nearer BULK freighter while the buy targeted a HEAVY one, and
// the reservation would be held against a hull nobody is buying.
func TestHeavyTarget_PrefersTheFirstHeavyClassThatCanBePriced(t *testing.T) {
	// The preferred class outranks a NEARER bulk yard, exactly as the buy path would.
	preferredWins := &fakeRanker{byType: map[string][]YardCandidate{
		"SHIP_HEAVY_FREIGHTER": {candidate("X1-QR78-AE4F", 1_565_500, 4)},
		"SHIP_BULK_FREIGHTER":  {candidate("X1-NEAR-BB2B", 2_931_905, 0)},
	}}
	finder := NewHeavyTargetFinder(&fakeAvailability{open: true}, preferredWins,
		&fakeFleet{ships: []*navigation.Ship{hullAt("TORWIND-A", "X1-HOME-A1")}}, nil)
	target, err := finder.HeavyTarget(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, "X1-QR78-AE4F", target.WaypointSymbol)
	require.Equal(t, []string{"SHIP_HEAVY_FREIGHTER"}, preferredWins.asked, "a priceable preferred class must stop the walk")

	// With the preferred class unpriceable anywhere, the next class becomes the target.
	fallback := &fakeRanker{byType: map[string][]YardCandidate{
		"SHIP_BULK_FREIGHTER": {candidate("X1-NEAR-BB2B", 2_931_905, 0)},
	}}
	finder = NewHeavyTargetFinder(&fakeAvailability{open: true}, fallback,
		&fakeFleet{ships: []*navigation.Ship{hullAt("TORWIND-A", "X1-HOME-A1")}}, nil)
	target, err = finder.HeavyTarget(context.Background(), 5)
	require.NoError(t, err)
	require.Equal(t, "X1-NEAR-BB2B", target.WaypointSymbol)
	require.Equal(t, int64(2_931_905), target.PurchasePrice)
	require.Equal(t, shipyard.DefaultHeavyShipTypes, fallback.asked, "every class must be tried before giving up")
}

// B4 — every unreadable input is an ERROR, never a silent zero. Both consumers must be able to
// tell "no heavy yard is known" (a fact about the map, which correctly stands the reservation
// down) from "we could not look" (a fact about the database, which must be surfaced). A zero
// returned for the second is indistinguishable from the feature being off.
func TestHeavyTarget_UnreadableInputsSurfaceAsErrorsNeverAsZero(t *testing.T) {
	hulls := []*navigation.Ship{hullAt("TORWIND-A", "X1-HOME-A1")}
	cases := []struct {
		name   string
		finder *HeavyTargetFinder
	}{
		{"availability unreadable", NewHeavyTargetFinder(&fakeAvailability{err: errors.New("db down")}, &fakeRanker{}, &fakeFleet{ships: hulls}, nil)},
		{"fleet unreadable", NewHeavyTargetFinder(&fakeAvailability{open: true}, &fakeRanker{}, &fakeFleet{err: errors.New("db down")}, nil)},
		{"yard rank unreadable", NewHeavyTargetFinder(&fakeAvailability{open: true}, &fakeRanker{err: errors.New("db down")}, &fakeFleet{ships: hulls}, nil)},
		{"finder unwired", NewHeavyTargetFinder(nil, nil, nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := tc.finder.HeavyTarget(context.Background(), 5)
			require.Error(t, err, "an unreadable input must never be reported as a closed capability")
			require.False(t, target.Priced)
			require.Zero(t, target.PurchasePrice)
		})
	}
}
