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

// A replay of the two sink classes the absorption model tells apart, driven through the
// real Reserve gate rather than the arithmetic underneath it.
//
// Both classes present the SAME history to the ledger — the fleet sold one tranche past
// the A-cap into the sink minutes ago — and the same request: a tour-shaped plan under
// the fleet-wide A-cap. The sink's LISTING BREADTH is what separates them: the
// micro-market that those tranches really do take off the board lists a single good, the
// hub that absorbs the same flow without a measurable bid move lists many.
const (
	replayTrancheSize = 20
	replayShadowAge   = 21 * time.Minute
	replayHardCap     = 4 * time.Hour
	// The fleet-wide ceiling a tour reserves against, in tranches. It bounds OTHER
	// containers' depth and never the plan's own size (sp-6zqza), so the replay's prior
	// history sits one tranche above it: a shadow that survives above the cap closes the
	// lane, and the crush prior decides whether a hub's survives at all.
	replayACapTranches = 2
	replayPriorUnits   = (replayACapTranches + 1) * replayTrancheSize
)

func setupDepthLedger(t *testing.T) (*persistence.AbsorptionLedgerGORM, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-DEPTH-REPLAY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	ledger := persistence.NewAbsorptionLedger(db, writeTestRecoveryArtifact(t), persistence.AbsorptionLedgerConfig{}, nil)
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

// replaySinkPastItsCap stages one sink class and returns whether a fresh A-cap plan may
// reserve into it. listings <= 0 leaves the sink's market uncached, which is the
// unreadable-breadth board state.
func replaySinkPastItsCap(t *testing.T, listings int) bool {
	t.Helper()
	ledger, db, playerID := setupDepthLedger(t)
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	if listings > 0 {
		seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
	}
	insertSinkShadow(t, db, playerID, key, replayPriorUnits, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, units, time.Hour)})
	require.NoError(t, err)
	return ok
}

// The deep-hub lane stays on the board: a multi-listing sink takes the same flow
// without the bid move that would justify embargoing its depth.
func TestAbsorptionSinkDepth_DeepHubLaneAdmittedPastItsCap(t *testing.T) {
	const hubListings = 12

	require.True(t, replaySinkPastItsCap(t, hubListings),
		"a multi-listing hub must absorb a fresh A-cap plan minutes after the fleet's flow")
}

// THE KILL SWITCH reaches the ledger: with the prior disabled the deep hub is charged the full
// claim and the lane is refused again.
func TestAbsorptionSinkDepth_DeepHubRefusedUnderTheKillSwitch(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	ledger.SetSinkDepthScaling(absorption.SinkDepthScaling{ThinListings: 2, MinCrushScale: 0.1})
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 12)
	insertSinkShadow(t, db, playerID, key, replayPriorUnits, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, units, time.Hour)})

	require.NoError(t, err)
	require.False(t, ok, "a disabled prior charges every sink the full claim")
}

// The thin lane is refused. This is the class the protection exists for.
func TestAbsorptionSinkDepth_ThinMicroMarketRefusedPastItsCap(t *testing.T) {
	for _, listings := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d listing(s)", listings), func(t *testing.T) {
			require.False(t, replaySinkPastItsCap(t, listings),
				"a micro-market the fleet has sold past its cap stays embargoed")
		})
	}
}

// Unreadable breadth is the conservative case: an uncached sink market keeps the full
// claim and refuses.
func TestAbsorptionSinkDepth_UnreadableBreadthRefuses(t *testing.T) {
	require.False(t, replaySinkPastItsCap(t, 0),
		"a sink whose breadth we cannot read keeps the full prior")
}

// The residual an unreadable sink reports is not merely the same DECISION but the same
// NUMBER: its full claim, undiscounted. An unfitted tier decays not at all, so the
// comparison is exact rather than clock-dependent.
func TestAbsorptionSinkDepth_UnreadableBreadthResidualIsTheFullClaim(t *testing.T) {
	key := absorption.LaneKey{Waypoint: "WP-UNCACHED", Good: "CLOTHING", Side: absorption.SideSell}
	ledger, db, playerID := setupDepthLedger(t)
	insertSinkShadow(t, db, playerID, key, 200, replayTrancheSize, "UNFITTED-TIER", time.Minute)

	out, err := ledger.Outstanding(context.Background(), playerID)

	require.NoError(t, err)
	require.Equal(t, 200.0, out[key].RecoveringResidual)
}

// Breadth scales the claim, it does not waive it: above the recovery floor a hub's
// residual is the full claim times the breadth discount, still occupying depth.
func TestAbsorptionSinkDepth_ResidualScalesRatherThanClears(t *testing.T) {
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}

	residual := func(listings int) float64 {
		ledger, db, playerID := setupDepthLedger(t)
		seedSinkListings(t, db, playerID, key.Waypoint, key.Good, listings)
		insertSinkShadow(t, db, playerID, key, 200, replayTrancheSize, "UNFITTED-TIER", time.Minute)
		out, err := ledger.Outstanding(context.Background(), playerID)
		require.NoError(t, err)
		return out[key].RecoveringResidual
	}

	require.Equal(t, 200.0, residual(2), "at the thin threshold the claim is untouched")
	require.Equal(t, 100.0, residual(4), "past it the claim is discounted, not dropped")
	require.Equal(t, 20.0, residual(200), "however broad the hub, the floor keeps a claim standing")
}

// In-flight PLANNED depth is another hull's live hold, not a recovery prior, so breadth
// never discounts it — a contended hub refuses however broad it is.
func TestAbsorptionSinkDepth_PlannedHoldsAreNeverDiscounted(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
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

// Breadth is read for the sink's OWN waypoint only. Another market's listings must never
// buy a micro-market a discount.
func TestAbsorptionSinkDepth_BreadthIsReadPerSinkWaypoint(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	micro := absorption.LaneKey{Waypoint: "WP-MICRO", Good: "MACHINERY", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, micro.Waypoint, micro.Good, 1)
	seedSinkListings(t, db, playerID, "WP-HUB", "CLOTHING", 40)
	insertSinkShadow(t, db, playerID, micro, replayPriorUnits, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(micro.Waypoint, micro.Good, units, units, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok)
}

// A rival player's rows are a different fleet's cache and must not widen our sink.
func TestAbsorptionSinkDepth_BreadthIsPlayerScoped(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
	rival := persistence.PlayerModel{AgentSymbol: "SP-RIVAL", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&rival).Error)

	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, rival.ID, key.Waypoint, key.Good, 40)
	insertSinkShadow(t, db, playerID, key, replayPriorUnits, replayTrancheSize, "WEAK", replayShadowAge)

	units := replayACapTranches * replayTrancheSize
	_, ok, err := ledger.Reserve(context.Background(), playerID, "ctr-tour", "tour",
		[]absorption.ReserveEntry{sellEntry(key.Waypoint, key.Good, units, units, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok)
}

// A shadow written before its sink's trade volume could be read is in the fail-closed
// branch (no recovery floor to fall under). Breadth must not soften it.
func TestAbsorptionSinkDepth_UnreadableTrancheSizeKeepsTheFullClaim(t *testing.T) {
	ledger, db, playerID := setupDepthLedger(t)
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
	ledger, db, playerID := setupDepthLedger(t)
	key := absorption.LaneKey{Waypoint: "WP-SINK", Good: "CLOTHING", Side: absorption.SideSell}
	seedSinkListings(t, db, playerID, key.Waypoint, key.Good, 12)
	insertSinkShadow(t, db, playerID, key, replayPriorUnits, replayTrancheSize, "WEAK", replayShadowAge)

	holders, err := ledger.HoldersForKeys(context.Background(), playerID, []absorption.LaneKey{key})
	require.NoError(t, err)
	require.Empty(t, holders[key], "a shadow the gate no longer counts must not be reported as a holder")
}
