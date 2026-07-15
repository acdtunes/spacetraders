package queries

// Unit tests for the reachable-yard ranking (sp-42ow): hop distance first,
// price second, over the stored gate adjacency (under-construction edges
// excluded), bounded to the strict heavy reach. 4 distinct behaviors:
// ordering, reachability exclusion, empty-store empty rank, unpriced exclusion.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

type fakeInventoryRows struct {
	rows []shipyard.ShipTypeAvailability
	err  error
}

func (f *fakeInventoryRows) ListByTypes(context.Context, int, []string) ([]shipyard.ShipTypeAvailability, error) {
	return f.rows, f.err
}

type fakeAdjacency struct {
	adj map[string][]system.GateEdge
	err error
}

func (f *fakeAdjacency) Adjacency(context.Context) (map[string][]system.GateEdge, error) {
	return f.adj, f.err
}

func edges(to ...string) []system.GateEdge {
	out := make([]system.GateEdge, 0, len(to))
	for _, s := range to {
		out = append(out, system.GateEdge{ConnectedSystem: s, GateWaypoint: s + "-I1"})
	}
	return out
}

func yardRow(sys, waypoint string, price int) shipyard.ShipTypeAvailability {
	return shipyard.ShipTypeAvailability{
		SystemSymbol: sys, WaypointSymbol: waypoint,
		ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: price,
		Supply: "MODERATE", LastScanned: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	}
}

// Ranking: hop distance dominates price; price breaks ties within a hop tier;
// a yard inside a reference system ranks at 0 hops ahead of a cheaper one a
// jump away. Reference systems are a SET (nearest-of-any wins) — the fleet's
// occupied systems.
func TestNearestYardsSelling_RanksByHopsThenPrice(t *testing.T) {
	// Topology: HOME ↔ NEAR ↔ FAR, and HOME ↔ SIDE.
	gates := &fakeAdjacency{adj: map[string][]system.GateEdge{
		"X1-HOME": edges("X1-NEAR", "X1-SIDE"),
		"X1-NEAR": edges("X1-HOME", "X1-FAR"),
		"X1-SIDE": edges("X1-HOME"),
		"X1-FAR":  edges("X1-NEAR"),
	}}
	inventory := &fakeInventoryRows{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-FAR", "X1-FAR-Y1", 900_000),     // cheapest but 2 hops
		yardRow("X1-NEAR", "X1-NEAR-Y1", 1_200_000), // 1 hop, pricier
		yardRow("X1-SIDE", "X1-SIDE-Y1", 1_000_000), // 1 hop, cheaper — wins the tier
		yardRow("X1-HOME", "X1-HOME-Y1", 1_500_000), // 0 hops — wins overall despite price
	}}
	f := NewReachableYardFinder(inventory, gates)

	got, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
	require.NoError(t, err)

	waypoints := make([]string, 0, len(got))
	for _, c := range got {
		waypoints = append(waypoints, c.WaypointSymbol)
	}
	require.Equal(t, []string{"X1-HOME-Y1", "X1-SIDE-Y1", "X1-NEAR-Y1", "X1-FAR-Y1"}, waypoints,
		"rank must be hops first (0 < 1 < 2), price second within a hop tier")
	require.Equal(t, 0, got[0].Hops)
	require.Equal(t, 1, got[1].Hops)
	require.Equal(t, 2, got[3].Hops)
	require.Equal(t, 1_500_000, got[0].PurchasePrice)
}

// Reachability: a yard with no stored route from any reference system is
// excluded, and an edge whose gate is UNDER CONSTRUCTION does not carry the
// route (the gategraph never routes into an unbuilt gate — sp-8qhu semantics).
func TestNearestYardsSelling_ExcludesUnreachableAndUnderConstructionRoutes(t *testing.T) {
	gates := &fakeAdjacency{adj: map[string][]system.GateEdge{
		// The ONLY route HOME→DARK is under construction.
		"X1-HOME": {{ConnectedSystem: "X1-DARK", GateWaypoint: "X1-DARK-I1", UnderConstruction: true}},
		// X1-LOST has no edges from anywhere.
	}}
	inventory := &fakeInventoryRows{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK", "X1-DARK-Y1", 800_000),
		yardRow("X1-LOST", "X1-LOST-Y1", 700_000),
	}}
	f := NewReachableYardFinder(inventory, gates)

	got, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
	require.NoError(t, err)
	require.Empty(t, got, "unroutable yards must never surface as candidates")
}

// Fail-closed substrate: an empty scan store ranks to empty WITHOUT error (the
// autosizer's price guard then fails closed as before), while a genuine store/
// graph read failure surfaces as an error.
func TestNearestYardsSelling_EmptyStoreEmptyRank_ReadFailuresSurface(t *testing.T) {
	t.Run("empty store → empty rank, no error", func(t *testing.T) {
		f := NewReachableYardFinder(&fakeInventoryRows{}, &fakeAdjacency{adj: map[string][]system.GateEdge{}})
		got, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("inventory read failure surfaces", func(t *testing.T) {
		f := NewReachableYardFinder(&fakeInventoryRows{err: errors.New("db down")}, &fakeAdjacency{adj: map[string][]system.GateEdge{}})
		_, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
		require.Error(t, err)
	})
	t.Run("adjacency read failure surfaces", func(t *testing.T) {
		inv := &fakeInventoryRows{rows: []shipyard.ShipTypeAvailability{yardRow("X1-AA", "X1-AA-Y1", 1)}}
		f := NewReachableYardFinder(inv, &fakeAdjacency{err: errors.New("graph down")})
		_, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
		require.Error(t, err)
	})
}

// Unpriced rows (availability known, price 0) prove discovery but can never
// feed a price guard — the buy-signal rank excludes them.
func TestNearestYardsSelling_ExcludesUnpricedRows(t *testing.T) {
	gates := &fakeAdjacency{adj: map[string][]system.GateEdge{}}
	inventory := &fakeInventoryRows{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-HOME", "X1-HOME-Y2", 0), // listed, unpriced
	}}
	f := NewReachableYardFinder(inventory, gates)

	got, err := f.NearestYardsSelling(context.Background(), 1, []string{"SHIP_HEAVY_FREIGHTER"}, []string{"X1-HOME"})
	require.NoError(t, err)
	require.Empty(t, got, "an unpriced row must not surface into a price-guard signal")
}
