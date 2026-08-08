package grpc

// Tests for fleetHeavyYardCatalog — the ONE read that can see a heavy yard's availability-only
// (unpriced) catalogue row, and therefore the only thing that can ever tell the pricing errand
// where to fly. Nothing here spends, but everything here decides whether a 1.5–2.9M heavy is ever
// priced at all: an empty-looking catalogue reads to the errand as "every known yard is already
// priced" and it silently stands down, so a read failure must surface as a REFUSAL (an error the
// coordinator logs and waits on), never as a silent empty list.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// fakeHeavyYardRanker is the heavyYardRanker double — the rank that KEEPS availability-only rows.
// Deliberately separate from fakeScannedYards (which implements only NearestYardsSelling): the two
// surfaces are what the wiring's type assertion distinguishes, so one fake satisfying both would
// erase the very distinction those tests pin.
type fakeHeavyYardRanker struct {
	candidates []shipyardQueries.YardCandidate
	err        error
	calls      int
	gotTypes   []string
	gotFrom    []string
}

func (f *fakeHeavyYardRanker) AllYardsSelling(_ context.Context, _ int, shipTypes, fromSystems []string) ([]shipyardQueries.YardCandidate, error) {
	f.calls++
	f.gotTypes = shipTypes
	f.gotFrom = fromSystems
	return f.candidates, f.err
}

// THE CATALOGUE'S REASON TO EXIST: the unpriced row survives. Every money-facing heavy read filters
// purchase_price 0 away (a zero can never feed a price guard), which is exactly how a fleet knows
// where to buy a heavy and still never forms a reservation. This port must carry that row through
// with its ask intact at 0, alongside the priced ones, annotated with gate reach measured from the
// systems the fleet actually stands in.
func TestKnownHeavyYards_CarriesTheUnpricedRowsEveryMoneyReadDiscards(t *testing.T) {
	ranker := &fakeHeavyYardRanker{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 0, Hops: 1},
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y1", ShipType: "SHIP_BULK_FREIGHTER", PurchasePrice: 2_931_905, Hops: 3},
	}}
	c := &fleetHeavyYardCatalog{
		ranker:   ranker,
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
	}

	yards, err := c.KnownHeavyYards(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, yards, 2, "the availability-only row must NOT be filtered away — it is the row the errand acts on")

	require.Equal(t, "X1-NEAR", yards[0].SystemSymbol)
	require.Equal(t, "X1-NEAR-Y1", yards[0].WaypointSymbol)
	require.Equal(t, "SHIP_HEAVY_FREIGHTER", yards[0].ShipType)
	require.Equal(t, int64(0), yards[0].PurchasePrice, "an unread ask stays 0 — reporting anything else would hand a guard a fabricated price")
	require.Equal(t, 1, yards[0].Hops)
	require.True(t, yards[0].Reachable)

	require.Equal(t, "X1-FAR-Y1", yards[1].WaypointSymbol)
	require.Equal(t, int64(2_931_905), yards[1].PurchasePrice)
	require.Equal(t, 3, yards[1].Hops)
	require.True(t, yards[1].Reachable)

	require.Equal(t, shipyardDomain.DefaultHeavyShipTypes, ranker.gotTypes,
		"the catalogue must ask for the documented heavy classes, not an invented set")
	require.Equal(t, []string{"X1-HOME"}, ranker.gotFrom,
		"reach is measured from WHERE OUR HULLS ARE — the errand has to fly there")
}

// REACH IS DERIVED FROM THE ROW, NOT ASSERTED OVER IT. A candidate whose hop count lies beyond the
// heavy reach bound is reported UNREACHABLE, so the errand policy (which drops !Reachable yards)
// never sends a hull somewhere it cannot fly. The bound itself is inclusive — a yard at exactly the
// bound is flyable and must stay a candidate.
func TestKnownHeavyYards_AYardBeyondTheHeavyReachBoundIsNotReachable(t *testing.T) {
	ranker := &fakeHeavyYardRanker{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-HERE", WaypointSymbol: "X1-HERE-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 0},
		{SystemSymbol: "X1-EDGE", WaypointSymbol: "X1-EDGE-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: gategraph.MaxJumpPath},
		{SystemSymbol: "X1-BEYOND", WaypointSymbol: "X1-BEYOND-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: gategraph.MaxJumpPath + 1},
		{SystemSymbol: "X1-NONSENSE", WaypointSymbol: "X1-NONSENSE-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: -1},
	}}
	c := &fleetHeavyYardCatalog{
		ranker:   ranker,
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
	}

	yards, err := c.KnownHeavyYards(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, yards, 4)

	require.True(t, yards[0].Reachable, "a yard in a system we already hold is 0 hops away")
	require.True(t, yards[1].Reachable, "the bound is INCLUSIVE — a yard at exactly the heavy reach bound is flyable")
	require.False(t, yards[2].Reachable,
		"a yard past the heavy reach bound must be reported unreachable — the errand drops !Reachable yards, and a hull cannot be sent where it cannot fly")
	require.False(t, yards[3].Reachable,
		"a nonsense hop count is not a reachability proof — it must fail closed, not read as 'nearby'")
}

// UNWIRED REPORTS NOTHING RATHER THAN INVENTING YARDS. Neither half of the catalogue can be
// substituted: with no ranker there is no scan surface to read, and with no fleet there is nothing
// to measure reach FROM, so a row emitted here would carry a hop count derived from nowhere.
func TestKnownHeavyYards_UnwiredReportsNoYardsAndReadsNothing(t *testing.T) {
	ranker := &fakeHeavyYardRanker{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 1},
	}}

	noRanker := &fleetHeavyYardCatalog{
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
	}
	yards, err := noRanker.KnownHeavyYards(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, yards)

	noFleet := &fleetHeavyYardCatalog{ranker: ranker}
	yards, err = noFleet.KnownHeavyYards(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, yards)
	require.Zero(t, ranker.calls, "with no fleet to measure reach from, the scan surface must not even be read")
}

// RULINGS #4-ADJACENT REFUSAL: a read failure surfaces as an ERROR, never as an empty catalogue.
// The distinction is load-bearing rather than stylistic — an empty list reads to the errand as
// "every known heavy yard is already priced" and it stands down silently forever, whereas an error
// is logged as "the catalogue is unreadable" and retried next tick. Both halves of the read fail
// the same way, so there is one policy here rather than two.
func TestKnownHeavyYards_AReadFailureRefusesInsteadOfReportingAnEmptyCatalogue(t *testing.T) {
	hulls := []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}
	priced := []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-NEAR", WaypointSymbol: "X1-NEAR-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", Hops: 1},
	}

	cases := []struct {
		name    string
		catalog *fleetHeavyYardCatalog
	}{
		{"the fleet read fails, so reach cannot be measured", &fleetHeavyYardCatalog{
			ranker:   &fakeHeavyYardRanker{candidates: priced},
			shipRepo: &fakeHeavyShipRepo{err: errors.New("db down")},
		}},
		{"the scan surface read fails", &fleetHeavyYardCatalog{
			ranker:   &fakeHeavyYardRanker{err: errors.New("inventory unreadable")},
			shipRepo: &fakeHeavyShipRepo{all: hulls},
		}},
		{"the player id is invalid", &fleetHeavyYardCatalog{
			ranker:   &fakeHeavyYardRanker{candidates: priced},
			shipRepo: &fakeHeavyShipRepo{all: hulls},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			playerID := 1
			if tc.name == "the player id is invalid" {
				playerID = 0
			}
			yards, err := tc.catalog.KnownHeavyYards(context.Background(), playerID)
			require.Error(t, err, "an unreadable catalogue must REFUSE — an empty list reads as 'everything is priced' and the errand stands down forever")
			require.Nil(t, yards)
		})
	}
}
