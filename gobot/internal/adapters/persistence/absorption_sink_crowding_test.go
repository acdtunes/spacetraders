package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// Sink crowding — the SELL mirror of absorption_source_crowding_test.go.
//
// The sell side already converted its shadows and already carried the fleet-wide bound, but
// it folded them ROW BY ROW: every ordinary sale into a broad sink came out under its own
// recovery floor and reported zero occupied depth, so the fleet could keep dumping into a
// sink it was already dumping into. Measured on the live ledger, only 120 of 991 sell
// shadows still blocked. These drive the pool the fleet actually crowds.
const (
	// A broad hub — the class the per-row rule left open, since breadth discounts each
	// sale's claim to a fraction of its own floor.
	sinkHubListings = 10
	// One hull's dump: a full tranche, exactly the ordinary sale that used to vanish.
	sinkHullLoad = replayTrancheSize
)

func crowdSink() absorption.LaneKey {
	return absorption.LaneKey{Waypoint: "WP-HUB-SINK", Good: "CLOTHING", Side: absorption.SideSell}
}

// sinkOneHull runs one hull's plan-accept against a sink: reserve the dump, then convert it
// at the sale exactly as a flown leg does.
func sinkOneHull(
	t *testing.T, ledger *persistence.AbsorptionLedgerGORM, playerID int,
	container string, key absorption.LaneKey, units int,
) bool {
	t.Helper()
	ctx := context.Background()
	capUnits := replayACapTranches * replayTrancheSize
	if capUnits < units {
		capUnits = units
	}
	_, ok, err := ledger.Reserve(ctx, playerID, container, "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, capUnits, time.Hour)})
	require.NoError(t, err)
	if !ok {
		return false
	}
	require.NoError(t, ledger.ConvertByContainer(ctx, container, playerID, key, units, "WEAK", replayTrancheSize))
	return true
}

// countSinkAdmitted dumps `hulls` hulls into one broad sink and returns how many got in.
func countSinkAdmitted(t *testing.T, hulls, listings int) int {
	t.Helper()
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
	admitted := 0
	for i := 0; i < hulls; i++ {
		if sinkOneHull(t, ledger, playerID, hullContainer(i), key, sinkHullLoad) {
			admitted++
		}
	}
	return admitted
}

// countSinkAdmittedOneHull is countSinkAdmitted with every dump made by the SAME container —
// one hull returning to a sink it already worked, rather than a queue of different hulls.
func countSinkAdmittedOneHull(t *testing.T, dumps, listings int) int {
	t.Helper()
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
	admitted := 0
	for i := 0; i < dumps; i++ {
		if sinkOneHull(t, ledger, playerID, "tour-run-HULL-RETURNING", key, sinkHullLoad) {
			admitted++
		}
	}
	return admitted
}

// Hull after hull dumps into the same broad sink, each sale lawful on its own, and the pool
// stops admitting them once the fleet's sales have taken its measured depth. Row by row this
// hub never closed at all.
func TestSinkCrowding_HullsCannotAllDumpIntoOneHub(t *testing.T) {
	admitted := countSinkAdmitted(t, 40, sinkHubListings)

	require.Less(t, admitted, 40, "the fleet must not be able to queue every hull onto one sink")
}

// The pool counts the shadows the RESERVING container wrote itself, and that is load-bearing.
// Reserve runs at plan-accept, so a container's own shadows on a sink were written by legs it
// has already flown: this is one hull RE-ENTERING a sink it worked before, and the sink pays
// for that re-entry what it pays for any other hull's tranche. Nothing else bounds it — the
// solver's ladder cap is per SOLVE, and a container's own PLANNED rows are released before
// every re-plan — so a pool that skipped its reserver's rows would let one hull dump into the
// same sink every plan, forever.
func TestSinkCrowding_HullReEnteringItsOwnSinkIsHeldOut(t *testing.T) {
	returning := countSinkAdmittedOneHull(t, 40, sinkHubListings)

	require.Less(t, returning, 40, "one hull must not re-enter its own worked sink without bound")
	require.Equal(t, countSinkAdmitted(t, 40, sinkHubListings), returning,
		"a hull's own shadows must hold it out exactly as another hull's would")
}

// A re-plan releases the container's in-flight holds, and it must not hand the hull back the
// sink it just crushed: only the PLANNED rows drop, so the EXECUTED shadow of the hull's own
// sale still holds the depth against its next plan.
func TestSinkCrowding_ReplanDoesNotReturnAHullItsOwnCrowdedSink(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)

	hull := "tour-run-HULL-REPLANNING"
	for i := 0; sinkOneHull(t, ledger, playerID, hull, key, sinkHullLoad); i++ {
		require.Less(t, i, 40, "the hull's own sales must close the sink, not admit forever")
	}

	dropped, err := ledger.ReleaseByContainer(ctx, hull, playerID)
	require.NoError(t, err)
	require.Zero(t, dropped, "every hold this hull took is already an executed shadow")

	_, ok, err := ledger.Reserve(ctx, playerID, hull, "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, sinkHullLoad,
			replayACapTranches*replayTrancheSize, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok, "a re-plan must not free the depth the hull's own sales are still holding")
}

// The bound is measured DEPTH scaled by the sink's breadth, so a hub still absorbs
// materially more of the fleet's flow than a micro-market — the crush prior survives the
// change to per-pool folding.
func TestSinkCrowding_BroadHubStillAbsorbsMoreThanAMicroMarket(t *testing.T) {
	micro := countSinkAdmitted(t, 40, 1)
	hub := countSinkAdmitted(t, 40, sinkHubListings)

	require.Greater(t, hub, micro, "breadth must keep discounting a sale's claim on a hub")
	require.Equal(t, replayACapTranches, micro, "a micro-market closes at its own measured depth")
}

// Crowding is MANY ordinary sales, which is why the pool is floored once rather than row by
// row: judged individually each of these dumps is under its own floor and invisible, and the
// hub reported untouched depth while the fleet crushed its bid.
func TestSinkCrowding_ManySubFloorSalesStillOccupyTheSink(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)

	const dumps = 6
	for i := 0; i < dumps; i++ {
		require.True(t, sinkOneHull(t, ledger, playerID, hullContainer(i), key, sinkHullLoad))
	}

	pools, err := ledger.Outstanding(ctx, playerID)
	require.NoError(t, err)
	require.Positive(t, pools[key].RecoveringResidual,
		"the pool must report the depth the fleet took, not zero per sub-floor row")
}

// A sale too small to matter cannot embargo a sink on its own: the pool's floor is half a
// tranche of depth, so the fleet's first ordinary dump leaves a broad hub fully open.
func TestSinkCrowding_OneOrdinarySaleDoesNotCloseAHub(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)

	require.True(t, sinkOneHull(t, ledger, playerID, "ctr-A", key, sinkHullLoad))
	require.True(t, sinkOneHull(t, ledger, playerID, "ctr-B", key, replayACapTranches*replayTrancheSize),
		"a sub-floor residual must not withdraw a hub's whole depth")
}

// The displaced hull is not stalled: only the crowded pool is withdrawn, and the SAME hull
// sells the same good into another sink in the same breath. A bound that serialized a laden
// fleet behind one sink would be worse than the crowding it fixes.
func TestSinkCrowding_DisplacedHullStillSellsElsewhere(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)
	seedSinkListings(t, db, playerID, "WP-NEXT-SINK", key.Good, sinkHubListings)

	for i := 0; sinkOneHull(t, ledger, playerID, hullContainer(i), key, sinkHullLoad); i++ {
		require.Less(t, i, 40, "the crowded sink must close, not admit forever")
	}

	_, ok, err := ledger.Reserve(ctx, playerID, "ctr-displaced", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-NEXT-SINK", key.Good, sinkHullLoad,
			replayACapTranches*replayTrancheSize, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok, "a hull displaced from one sink must fall through to the next, not stall")
}

// A refusal must name who holds the depth. Under per-pool folding the blockers are rows that
// are each individually sub-floor, so a per-row attribution read would report an empty list
// for a sink the gate had just closed.
func TestSinkCrowding_RefusalNamesTheAccumulatedHolders(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)

	const dumps = 6
	for i := 0; i < dumps; i++ {
		require.True(t, sinkOneHull(t, ledger, playerID, hullContainer(i), key, sinkHullLoad))
	}

	holders, err := ledger.HoldersForKeys(ctx, playerID, []absorption.LaneKey{key})
	require.NoError(t, err)
	require.Len(t, holders[key], dumps, "every hull whose sale still occupies depth must be named")
}

// The sink keeps its own, longer clock: a sale's shadow lives to the sink hard cap, not the
// shorter buy-shadow life the source side runs on.
func TestSinkCrowding_SellShadowKeepsTheSinkHardCap(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	key := crowdSink()
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, sinkHubListings)

	require.True(t, sinkOneHull(t, ledger, playerID, "ctr-A", key, sinkHullLoad))

	var row persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("player_id = ? AND side = ?", playerID, absorption.SideSell).First(&row).Error)
	require.NotNil(t, row.ExecutedAt)
	require.InDelta(t, persistence.DefaultExecutedHardCap.Seconds(),
		row.ExpiresAt.Sub(*row.ExecutedAt).Seconds(), 5,
		"a sink recovers on its own clock, not the source side's")
}

// The source side is untouched by the sell-side fold: a purchase still takes the uniform
// prior, because breadth does not replenish the one good a purchase consumed (RULINGS #4).
func TestSinkCrowding_BuySideStillTakesNoBreadthDiscount(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-BROAD-SOURCE", Good: "FOOD", Side: absorption.SideBuy}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 40)

	units := crowdACapTranches * crowdTrancheSize
	require.True(t, sourceOneHull(t, ledger, playerID, "ctr-A", key, units, crowdTrancheSize))

	pools, err := ledger.Outstanding(ctx, playerID)
	require.NoError(t, err)
	require.InDelta(t, float64(units), pools[key].RecoveringResidual, 1.0,
		"a broad market's listings must not discount what the fleet took off its shelf")
}
