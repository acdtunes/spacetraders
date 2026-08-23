package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// A replay of the two sink classes the absorption model has to tell apart, driven
// through the real Reserve gate rather than the arithmetic underneath it.
//
// Both classes present the SAME history to the ledger — one full tranche sold into the
// sink minutes ago — and the same request: a tour-shaped plan for the fleet-wide A-cap
// of tranches. Under one uniform crush prior the two are indistinguishable and both are
// refused, which is what leaves a clearing deep-hub lane on the board. The sink's
// LISTING BREADTH is what separates them: the micro-market that one tranche really does
// take off the board lists a single good, the hub that absorbs the same tranche without
// a measurable bid move lists many.
const (
	replayTrancheSize = 20
	replayShadowAge   = 21 * time.Minute
	replayHardCap     = 4 * time.Hour
	// The A-cap a tour reserves and the fleet-wide ceiling it reserves against are the
	// same number of tranches, so ANY surviving shadow on the sink breaches the plan —
	// that identity is what makes the crush prior decide the lane.
	replayACapTranches = 2
)

// replayDepthPolicy is the refitted prior under test; replayUniformPolicy is the
// pre-refit behaviour, expressed through the same knobs.
func replayDepthPolicy() absorption.SinkDepthScaling {
	return absorption.SinkDepthScaling{Enabled: true, ThinListings: 2, MinCrushScale: 0.1}
}

func replayUniformPolicy() absorption.SinkDepthScaling {
	return absorption.SinkDepthScaling{}
}

func setupDepthLedger(t *testing.T, policy absorption.SinkDepthScaling) (*persistence.AbsorptionLedgerGORM, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-DEPTH-REPLAY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	ledger := persistence.NewAbsorptionLedger(db, writeTestRecoveryArtifact(t), persistence.AbsorptionLedgerConfig{}, nil)
	ledger.SetSinkDepthScaling(policy)
	return ledger, db, player.ID
}

// seedSinkListings gives the sink waypoint a cached market of `listings` goods, the
// traded good among them.
func seedSinkListings(t *testing.T, db *gorm.DB, playerID int, waypoint, good string, listings int) {
	t.Helper()
	now := time.Now()
	for i := 0; i < listings; i++ {
		symbol := good
		if i > 0 {
			symbol = fmt.Sprintf("FILLER-%02d", i)
		}
		require.NoError(t, db.Create(&persistence.MarketData{
			PlayerID: playerID, WaypointSymbol: waypoint, GoodSymbol: symbol,
			PurchasePrice: 100, SellPrice: 90, TradeVolume: replayTrancheSize, LastUpdated: now,
		}).Error)
	}
}

func insertSinkShadow(t *testing.T, db *gorm.DB, playerID int, key absorption.LaneKey, units, tranche int, tier string, age time.Duration) {
	t.Helper()
	executedAt := time.Now().Add(-age)
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: uuid.NewString(), PlayerID: playerID, ContainerID: "prior-leg", Engine: "tour",
		Waypoint: key.Waypoint, Good: key.Good, Side: key.Side,
		State: "EXECUTED", Units: units, TrancheSize: tranche, TierAtWrite: tier,
		CreatedAt: executedAt, ExecutedAt: &executedAt, ExpiresAt: executedAt.Add(replayHardCap),
	}).Error)
}

// replaySinkAfterOneTranche stages one sink class and returns whether a fresh A-cap
// plan may reserve into it. listings <= 0 leaves the sink's market uncached, which is
// the unreadable-breadth board state.
func replaySinkAfterOneTranche(t *testing.T, policy absorption.SinkDepthScaling, listings int) bool {
	t.Helper()
	ledger, db, playerID := setupDepthLedger(t, policy)
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	if listings > 0 {
		seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
	}
	insertSinkShadow(t, db, playerID, key, replayTrancheSize, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, units, time.Hour)})
	require.NoError(t, err)
	return ok
}

// (a) The deep-hub lane the uniform prior leaves on the board. Same history, same
// request, same guard — only the prior differs.
func TestAbsorptionSinkDepth_DeepHubLaneAdmittedAfterATranche(t *testing.T) {
	const hubListings = 12

	require.False(t, replaySinkAfterOneTranche(t, replayUniformPolicy(), hubListings),
		"the uniform prior refuses the deep-hub lane — this is the refusal under refit")
	require.True(t, replaySinkAfterOneTranche(t, replayDepthPolicy(), hubListings),
		"a multi-listing hub must absorb a fresh A-cap plan a tranche after the last one")
}

// (b) The thin lane stays refused. This is the class the prior was fitted on and the
// reason the protection exists at all.
func TestAbsorptionSinkDepth_ThinMicroMarketStillRefusedAfterItsTranche(t *testing.T) {
	for _, listings := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d listing(s)", listings), func(t *testing.T) {
			require.False(t, replaySinkAfterOneTranche(t, replayDepthPolicy(), listings),
				"a micro-market that one tranche takes off the board must stay embargoed with the refit armed")
			require.False(t, replaySinkAfterOneTranche(t, replayUniformPolicy(), listings))
		})
	}
}

// (d) Unreadable breadth is the conservative case: an uncached sink market decides
// exactly as the uniform prior does.
func TestAbsorptionSinkDepth_UnreadableBreadthRefusesAsBefore(t *testing.T) {
	require.False(t, replaySinkAfterOneTranche(t, replayDepthPolicy(), 0),
		"a sink whose breadth we cannot read keeps the full prior")
	require.False(t, replaySinkAfterOneTranche(t, replayUniformPolicy(), 0))
}

// (d) continued — the residual an unreadable sink reports is not merely the same
// DECISION but the same NUMBER. An unfitted tier decays not at all, so the comparison
// is exact rather than clock-dependent.
func TestAbsorptionSinkDepth_UnreadableBreadthResidualIsUnchanged(t *testing.T) {
	key := absorption.LaneKey{Waypoint: "WP-UNCACHED", Good: "CLOTHING", Side: absorption.SideSell}

	residual := func(policy absorption.SinkDepthScaling) float64 {
		ledger, db, playerID := setupDepthLedger(t, policy)
		insertSinkShadow(t, db, playerID, key, 200, replayTrancheSize, "UNFITTED-TIER", time.Minute)
		out, err := ledger.Outstanding(context.Background(), playerID)
		require.NoError(t, err)
		return out[key].RecoveringResidual
	}

	require.Equal(t, 200.0, residual(replayUniformPolicy()))
	require.Equal(t, 200.0, residual(replayDepthPolicy()))
}

// The refit scales the claim, it does not waive it: above the recovery floor a hub's
// residual is the uniform residual times the breadth discount, still occupying depth.
func TestAbsorptionSinkDepth_ResidualScalesRatherThanClears(t *testing.T) {
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}

	residual := func(policy absorption.SinkDepthScaling, listings int) float64 {
		ledger, db, playerID := setupDepthLedger(t, policy)
		seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
		insertSinkShadow(t, db, playerID, key, 200, replayTrancheSize, "UNFITTED-TIER", time.Minute)
		out, err := ledger.Outstanding(context.Background(), playerID)
		require.NoError(t, err)
		return out[key].RecoveringResidual
	}

	require.Equal(t, 200.0, residual(replayDepthPolicy(), 2), "at the thin threshold the claim is untouched")
	require.Equal(t, 100.0, residual(replayDepthPolicy(), 4), "past it the claim is discounted, not dropped")
	require.Equal(t, 20.0, residual(replayDepthPolicy(), 200), "however broad the hub, the floor keeps a claim standing")
}

// (c) The mechanisms are untouched. In-flight PLANNED depth is another hull's live
// hold, not a recovery prior, so breadth never discounts it — a contended hub refuses
// with the refit armed exactly as it does without.
func TestAbsorptionSinkDepth_PlannedHoldsAreNeverDiscounted(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t, replayDepthPolicy())
	ctx := context.Background()
	seedSinkListings(t, db, playerID, "WP-SINK", "CLOTHING", 40)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(ctx, playerID, "ctr-first", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-SINK", "CLOTHING", units, units, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = ledger.Reserve(ctx, playerID, "ctr-second", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-SINK", "CLOTHING", units, units, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok, "a hull's in-flight hold is not a recovery shadow and gets no breadth discount")
}

// (c) continued — breadth is read for the sink's OWN waypoint only. Another market's
// listings must never buy a micro-market a discount.
func TestAbsorptionSinkDepth_BreadthIsReadPerSinkWaypoint(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t, replayDepthPolicy())
	micro := absorption.LaneKey{Waypoint: "WP-MICRO", Good: "MACHINERY", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, micro.Waypoint, micro.Good, 1)
	seedSinkListings(t, db, playerID, "WP-HUB", "CLOTHING", 40)
	insertSinkShadow(t, db, playerID, micro, replayTrancheSize, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(micro.Waypoint, micro.Good, units, units, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok)
}

// A rival player's rows are a different fleet's cache and must not widen our sink.
func TestAbsorptionSinkDepth_BreadthIsPlayerScoped(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t, replayDepthPolicy())
	rival := persistence.PlayerModel{AgentSymbol: "SP-RIVAL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)

	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, rival.ID, key.Waypoint, key.Good, 40)
	insertSinkShadow(t, db, playerID, key, replayTrancheSize, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, units, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok)
}

// A shadow written before its sink's trade volume could be read is already in the
// fail-closed branch (no recovery floor to fall under). Breadth must not soften it.
func TestAbsorptionSinkDepth_UnreadableTrancheSizeKeepsTheUniformPrior(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t, replayDepthPolicy())
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 40)
	insertSinkShadow(t, db, playerID, key, 200, 0, "UNFITTED-TIER", time.Minute)

	out, err := ledger.Outstanding(context.Background(), playerID)
	require.NoError(t, err)
	require.Equal(t, 200.0, out[key].RecoveringResidual)
}

// The refusal-attribution read reports the same blocking depth the gate counted, so a
// discounted shadow never shows up as a phantom holder of depth it no longer occupies.
func TestAbsorptionSinkDepth_HolderAttributionMatchesTheGate(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t, replayDepthPolicy())
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 12)
	insertSinkShadow(t, db, playerID, key, replayTrancheSize, replayTrancheSize, "WEAK", replayShadowAge)

	holders, err := ledger.HoldersForKeys(context.Background(), playerID, []absorption.LaneKey{key})
	require.NoError(t, err)
	require.Empty(t, holders[key], "a shadow the gate no longer counts must not be reported as a holder")
}
