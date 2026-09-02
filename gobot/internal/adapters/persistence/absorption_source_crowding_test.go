package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// Source crowding. Every anti-herd guard the trade fleet carried was scoped to ONE hull,
// so nothing bounded how many DIFFERENT hulls routed to the same market to buy: each
// ranked the same ask best, each obeyed every rule, and together they bid it up. The ledger
// already carried the fleet-wide bound on the SELL side; these drive the BUY side of it.
const (
	crowdTrancheSize = 100
	// The A-cap the tour reserves against, in tranches — the same fleet-wide ceiling the
	// sell side uses, so the bound is measured market DEPTH, not a hull count.
	crowdACapTranches = 2
	// One hull's load, under half a tranche so no single purchase trips the pool.
	crowdHullLoad = 40
	// Loads of crowdHullLoad that crowdACapTranches × crowdTrancheSize of depth admits.
	crowdAdmittedLoads = 5
)

func buyEntry(wp, good string, units, capUnits int, ttl time.Duration) absorption.ReserveEntry {
	return absorption.ReserveEntry{
		Waypoint: wp, Good: good, Side: absorption.SideBuy,
		Units: units, CapUnits: capUnits, TTL: ttl,
	}
}

// crowdSource is the (market, good) every hull in these tests ranks best.
func crowdSource() absorption.LaneKey {
	return absorption.LaneKey{Waypoint: "WP-SOURCE", Good: "FOOD", Side: absorption.SideBuy}
}

func hullContainer(i int) string { return fmt.Sprintf("tour-run-HULL-%02d", i) }

// sourceOneHull runs one hull's plan-accept against a source: reserve the load, then
// convert it at the purchase exactly as a flown leg does.
func sourceOneHull(
	t *testing.T, ledger *persistence.AbsorptionLedgerGORM, playerID int,
	container string, key absorption.LaneKey, units, trancheSize int,
) bool {
	t.Helper()
	ctx := context.Background()
	capUnits := crowdACapTranches * trancheSize
	if capUnits < units {
		capUnits = units
	}
	_, ok, err := ledger.Reserve(ctx, playerID, container, "tour",
		[]absorption.ReserveEntry{buyEntry(key.Waypoint, key.Good, units, capUnits, time.Hour)})
	require.NoError(t, err)
	if !ok {
		return false
	}
	require.NoError(t, ledger.ConvertByContainer(ctx, container, playerID, key, units, "WEAK", trancheSize))
	return true
}

// countAdmitted sources `hulls` hulls onto one market and returns how many got in.
func countAdmitted(t *testing.T, hulls, trancheSize int) int {
	t.Helper()
	ledger, _, playerID := setupDepthLedger(t)
	admitted := 0
	for i := 0; i < hulls; i++ {
		if sourceOneHull(t, ledger, playerID, hullContainer(i), crowdSource(), crowdHullLoad, trancheSize) {
			admitted++
		}
	}
	return admitted
}

// countAdmittedOneHull is countAdmitted with every load taken by the SAME container — one
// hull returning to a source it already drained, rather than a queue of different hulls.
func countAdmittedOneHull(t *testing.T, loads, trancheSize int) int {
	t.Helper()
	ledger, _, playerID := setupDepthLedger(t)
	admitted := 0
	for i := 0; i < loads; i++ {
		if sourceOneHull(t, ledger, playerID, "tour-run-HULL-RETURNING", crowdSource(), crowdHullLoad, trancheSize) {
			admitted++
		}
	}
	return admitted
}

// Hull after hull sources the same (market, good), each load lawful on its own, and the
// pool stops admitting them once the fleet's purchases have taken its measured depth.
func TestSourceCrowding_HullsCannotAllQueueOntoOneSource(t *testing.T) {
	admitted := countAdmitted(t, 12, crowdTrancheSize)

	require.Less(t, admitted, 12, "the fleet must not be able to queue every hull onto one source")
	require.Equal(t, crowdAdmittedLoads, admitted, "the source admits exactly its measured depth, then closes")
}

// A healthy small fleet never meets the bound: the same source under the same rules is
// wide open to a fleet small enough that its hulls do not stack there.
func TestSourceCrowding_HealthySmallFleetIsUnaffected(t *testing.T) {
	require.Equal(t, 4, countAdmitted(t, 4, crowdTrancheSize),
		"a small fleet's ordinary rotation through a source must never be refused")
}

// The pool counts the shadows the RESERVING container wrote itself, and on the source side
// that is load-bearing for the same reason it is on the sink side and one more. Reserve runs
// at plan-accept, so a container's own shadows on a source were written by legs it has already
// flown: this is one hull RETURNING to a market it already drained. Nothing else bounds that
// return — the solver's ladder cap is per SOLVE, the per-hull rebuy window withdraws only the
// hull's own next SOURCE choice for a fixed clock rather than the market's depth, and a
// container's own PLANNED rows are released before every re-plan. And the ask a returning hull
// pays for its own prior tranches is no smaller than what it pays for a stranger's, so the
// weight on its own rows is the same weight (RULINGS #4).
func TestSourceCrowding_HullReEnteringItsOwnSourceIsHeldOut(t *testing.T) {
	returning := countAdmittedOneHull(t, 12, crowdTrancheSize)

	require.Less(t, returning, 12, "one hull must not re-drain its own worked source without bound")
	require.Equal(t, countAdmitted(t, 12, crowdTrancheSize), returning,
		"a hull's own purchase shadows must hold it out exactly as another hull's would")
}

// A re-plan releases the container's in-flight holds, and it must not hand the hull back the
// source it just drained: only the PLANNED rows drop, so the EXECUTED shadow of the hull's own
// purchase still holds the depth against its next plan. This is the seam that decides whether
// a hull's own rows are stale intent (released) or realized damage (kept) — they are both, and
// the release scope already tells them apart.
func TestSourceCrowding_ReplanDoesNotReturnAHullItsOwnDrainedSource(t *testing.T) {
	ledger, _, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSource()

	hull := "tour-run-HULL-REPLANNING"
	for i := 0; sourceOneHull(t, ledger, playerID, hull, key, crowdHullLoad, crowdTrancheSize); i++ {
		require.Less(t, i, 12, "the hull's own purchases must close the source, not admit forever")
	}

	dropped, err := ledger.ReleaseByContainer(ctx, hull, playerID)
	require.NoError(t, err)
	require.Zero(t, dropped, "every hold this hull took is already an executed shadow")

	_, ok, err := ledger.Reserve(ctx, playerID, hull, "tour",
		[]absorption.ReserveEntry{buyEntry(key.Waypoint, key.Good, crowdHullLoad,
			crowdACapTranches*crowdTrancheSize, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok, "a re-plan must not free the depth the hull's own purchases are still holding")
}

// The displaced hull is not stalled: only the crowded pool is withdrawn, and the SAME hull
// sources the same good elsewhere in the same breath. A bound that serialized the fleet
// behind one market would be worse than the crowding it fixes.
func TestSourceCrowding_DisplacedHullStillSourcesElsewhere(t *testing.T) {
	ledger, _, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSource()

	for i := 0; i < crowdAdmittedLoads; i++ {
		require.True(t, sourceOneHull(t, ledger, playerID, hullContainer(i), key, crowdHullLoad, crowdTrancheSize))
	}
	require.False(t, sourceOneHull(t, ledger, playerID, "ctr-displaced", key, crowdHullLoad, crowdTrancheSize),
		"the crowded source must refuse the displaced hull")

	// Its next-best source, and the same good, are untouched — the bound is pool-scoped.
	_, ok, err := ledger.Reserve(ctx, playerID, "ctr-displaced", "tour",
		[]absorption.ReserveEntry{buyEntry("WP-NEXT-BEST", key.Good, crowdHullLoad, crowdACapTranches*crowdTrancheSize, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok, "a hull displaced from one source must fall through to the next, not stall")
}

// The bound is measured DEPTH, not a hull count, so it scales with the market rather than
// with the fleet: a source three times as deep admits three times the fleet's purchases.
func TestSourceCrowding_BoundScalesWithMarketDepth(t *testing.T) {
	shallow := countAdmitted(t, 30, crowdTrancheSize)
	deep := countAdmitted(t, 30, 3*crowdTrancheSize)

	require.Equal(t, crowdAdmittedLoads, shallow)
	require.Equal(t, 3*crowdAdmittedLoads, deep, "three times the depth admits three times the loads")
}

// A purchase too small to matter cannot embargo a source on its own: the pool's floor is
// half a tranche of depth, so the fleet's first small buy leaves the source fully open.
func TestSourceCrowding_OneSmallBuyDoesNotCloseASource(t *testing.T) {
	ledger, _, playerID := setupDepthLedger(t)
	key := crowdSource()

	require.True(t, sourceOneHull(t, ledger, playerID, "ctr-A", key, crowdTrancheSize/4, crowdTrancheSize))
	require.True(t, sourceOneHull(t, ledger, playerID, "ctr-B", key, crowdACapTranches*crowdTrancheSize, crowdTrancheSize),
		"a sub-floor residual must not withdraw the source's whole depth")
}

// Crowding is MANY ordinary purchases, which is why the pool is floored once rather than
// row by row: judged individually every one of these loads is sub-floor and invisible, and
// the source would report untouched depth while the fleet drained it.
func TestSourceCrowding_ManySubFloorBuysStillOccupyTheSource(t *testing.T) {
	ledger, _, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSource()

	for i := 0; i < crowdAdmittedLoads; i++ {
		require.True(t, sourceOneHull(t, ledger, playerID, hullContainer(i), key, crowdHullLoad, crowdTrancheSize))
	}

	pools, err := ledger.Outstanding(ctx, playerID)
	require.NoError(t, err)
	require.InDelta(t, float64(crowdAdmittedLoads*crowdHullLoad), pools[key].RecoveringResidual, 1.0,
		"the pool must report every unit the fleet took, not zero per sub-floor row")
}

// The fleet's purchase memory runs on its own clock, shorter than a sink's, so a source
// whose ask has come back is offered again.
func TestSourceCrowding_BuyShadowExpiresOnItsOwnClock(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSource()

	require.True(t, sourceOneHull(t, ledger, playerID, "ctr-A", key,
		crowdACapTranches*crowdTrancheSize, crowdTrancheSize))

	var row persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("player_id = ? AND side = ?", playerID, absorption.SideBuy).First(&row).Error)
	require.NotNil(t, row.ExecutedAt)
	life := row.ExpiresAt.Sub(*row.ExecutedAt)
	require.InDelta(t, persistence.DefaultBuyShadowLife.Seconds(), life.Seconds(), 5,
		"a buy shadow lives on the buy clock, not the sink's hard cap")
	require.Less(t, life, persistence.DefaultExecutedHardCap)
}

// A sink keeps its own, longer clock and its per-row rules — this change adds a side, it
// does not retune the one already calibrated (RULINGS #4).
func TestSourceCrowding_SellShadowKeepsItsOwnClockAndRowRules(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "FOOD", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 8)

	_, ok, err := ledger.Reserve(ctx, playerID, "ctr-A", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, crowdHullLoad, crowdACapTranches*crowdTrancheSize, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", playerID, key, crowdHullLoad, "WEAK", crowdTrancheSize))

	var row persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("player_id = ? AND side = ?", playerID, absorption.SideSell).First(&row).Error)
	require.InDelta(t, persistence.DefaultExecutedHardCap.Seconds(), row.ExpiresAt.Sub(*row.ExecutedAt).Seconds(), 5)

	pools, err := ledger.Outstanding(ctx, playerID)
	require.NoError(t, err)
	require.Zero(t, pools[key].RecoveringResidual,
		"one sub-floor sale into a broad sink still blocks nothing, exactly as before")
}

// Fail closed (RULINGS #4): the guard cannot silently disappear when the fleet's own
// purchase volume is unreadable. With the ledger's table gone, both the cap check and the
// consult read surface the failure rather than reporting a fleet that has bought nothing.
func TestSourceCrowding_UnreadableFleetVolumeFailsClosed(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSource()
	require.NoError(t, db.Exec("DROP TABLE market_absorption_ledger").Error)

	units := crowdACapTranches * crowdTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-A", "tour",
		[]absorption.ReserveEntry{buyEntry(key.Waypoint, key.Good, units, units, time.Hour)})
	require.Error(t, err, "an unreadable pool must surface as an error the caller fails closed on")
	require.False(t, ok)

	_, err = ledger.Outstanding(context.Background(), playerID)
	require.Error(t, err, "the consult read must report the failure, never an empty fleet history")
}
