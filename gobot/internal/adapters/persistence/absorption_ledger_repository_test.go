package persistence_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-78ai L1: the cross-engine absorption ledger. Five engines absorb the same
// market depth; the only cross-container signal is the market cache, which reflects
// EXECUTED trades seconds late and never shows in-flight intent or the recovery a
// dump imposes (design §0). This suite drives the DB-backed substrate that carries
// both: PLANNED rows (in-flight, undecayed) and EXECUTED shadows (decaying on the
// fitted per-tier half-life, hard-capped at 12h), reserved all-or-nothing under a
// per-player advisory lock so a co-dump (sp-lbbm, −80k) cannot slip the cap.
//
// Fixed recovery artifact below: WEAK half-life 60min, STRONG 120min. So an EXECUTED
// WEAK shadow of 100 units decays to 50 at 60min and 25 at 120min — deterministic
// against real wall-clock by writing executed_at in the past.
const (
	testWeakHalfLifeMin   = 60
	testStrongHalfLifeMin = 120
)

func writeTestRecoveryArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "market_model.json")
	body := `{
	  "fit_version": "test",
	  "recovery": {
	    "": {"half_life_minutes": 1000.0, "n_series": 3},
	    "WEAK": {"half_life_minutes": 60.0, "n_series": 20},
	    "STRONG": {"half_life_minutes": 120.0, "n_series": 1}
	  }
	}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// fakeLiveness answers LiveContainerIDs from a fixed set — the container registry
// the dead-container reclaim consults.
type fakeLiveness struct {
	live map[string]struct{}
	err  error
}

func (f fakeLiveness) LiveContainerIDs(_ context.Context, _ int) (map[string]struct{}, error) {
	return f.live, f.err
}

func liveSet(ids ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

func setupAbsorptionLedger(t *testing.T, liveness persistence.ContainerLivenessProvider) (*persistence.AbsorptionLedgerGORM, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	artifact := writeTestRecoveryArtifact(t)
	ledger := persistence.NewAbsorptionLedger(db, artifact, persistence.AbsorptionLedgerConfig{}, liveness)
	return ledger, db
}

func countAbsorption(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.MarketAbsorptionLedgerModel{}).Count(&n).Error)
	return n
}

func sellEntry(wp, good string, units, cap int, ttl time.Duration) absorption.ReserveEntry {
	return absorption.ReserveEntry{
		Waypoint: wp, Good: good, Side: absorption.SideSell,
		Units: units, CapUnits: cap, TTL: ttl,
	}
}

// --- CRUD + conditional reserve ---

// A lone reservation that clears its cap proceeds and persists one PLANNED row.
func TestAbsorptionLedger_ReserveWithinCap_Proceeds(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	ids, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok, "a reservation that clears its cap must proceed")
	require.Len(t, ids, 1)
	require.Equal(t, int64(1), countAbsorption(t, db))
}

// Binary exclusion (idle-arb semantics): with a PLANNED leg already on a sink, a
// second reservation whose CapUnits equals its own units breaches — ANY other
// outstanding on the key trips it — and rolls back, leaving only the first.
func TestAbsorptionLedger_BinaryExclusion_SecondBreachesAndRollsBack(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	_, okA, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, okA)

	idsB, okB, err := ledger.Reserve(ctx, 1, "ctr-B", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err, "a cap breach parks the plan, it is not an error")
	require.False(t, okB, "a second leg on the same sink breaches binary exclusion")
	require.Empty(t, idsB)
	require.Equal(t, int64(1), countAbsorption(t, db), "the breaching plan is rolled back, only A remains")
}

// Tour semantics: a tranche cap (A-cap × trade_volume) lets tranches lawfully STACK
// up to the cap and rejects the one that would exceed it — the fleet-wide A-cap.
func TestAbsorptionLedger_TrancheCap_StacksToCapThenBreaches(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	// Cap 400 (A-cap 2 × trade_volume 200). Two 100-unit tranches fit (200 ≤ 400).
	_, ok1, err := ledger.Reserve(ctx, 1, "ctr-A", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 100, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok1)

	_, ok2, err := ledger.Reserve(ctx, 1, "ctr-B", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 100, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok2, "a second tranche within the depth cap must stack")

	// A third 250-unit tranche would push outstanding to 450 > 400 → breach.
	_, ok3, err := ledger.Reserve(ctx, 1, "ctr-C", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 250, 400, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok3, "the tranche that would exceed the depth cap must park")
}

// All-or-nothing: a multi-sink plan is rejected WHOLE when any one sink breaches —
// no partial reservation lingers to poison a re-plan.
func TestAbsorptionLedger_MultiSinkPlan_RollsBackWholeOnAnyBreach(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	// Occupy WP-2 first so the plan's second sink breaches.
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-pre", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-2", "GOLD", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = ledger.Reserve(ctx, 1, "ctr-plan", "tour", []absorption.ReserveEntry{
		sellEntry("WP-1", "IRON", 50, 50, time.Hour), // clear
		sellEntry("WP-2", "GOLD", 50, 50, time.Hour), // breaches (WP-2 occupied)
	})
	require.NoError(t, err)
	require.False(t, ok, "the whole plan parks when any sink breaches")
	require.Equal(t, int64(1), countAbsorption(t, db), "no partial rows persist — only the pre-existing WP-2 hold")
}

// Acceptance: two CONCURRENT reservations on one binary sink — the per-player
// advisory lock (Postgres) / global writer serialization (SQLite) admits exactly
// one. This is the co-dump race closed, fail-closed.
func TestAbsorptionLedger_ConcurrentBinaryReserve_ExactlyOneProceeds(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	const workers = 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		proceed int
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := ledger.Reserve(ctx, 1, "ctr", "idle-arb",
				[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
			require.NoError(t, err)
			if ok {
				mu.Lock()
				proceed++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, proceed, "exactly one of two concurrent co-dumps into one sink may proceed")
	require.Equal(t, int64(1), countAbsorption(t, db), "only the winner's reservation persists")
}

// Release frees a hold: a leg that breached while an earlier reservation was in
// flight proceeds once that reservation releases.
func TestAbsorptionLedger_ReleaseFreesSink(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	idsA, okA, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, okA)

	_, okB, err := ledger.Reserve(ctx, 1, "ctr-B", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.False(t, okB)

	require.NoError(t, ledger.Release(ctx, idsA[0]))

	_, okB2, err := ledger.Reserve(ctx, 1, "ctr-B", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, okB2, "with A released, B's sink is clear and it proceeds")
}

func TestAbsorptionLedger_ReleaseMissingIsNoOp(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, nil)
	require.NoError(t, ledger.Release(context.Background(), "does-not-exist"))
	require.NoError(t, ledger.Release(context.Background(), ""))
}

// Reservations are scoped per player: one player's in-flight absorption never caps
// another's.
func TestAbsorptionLedger_ScopedPerPlayer(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	_, ok1, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok1)

	_, ok2, err := ledger.Reserve(ctx, 2, "ctr-B", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok2, "player 2's cap must ignore player 1's reservation on the same sink")
}

// --- Outstanding (consult read) ---

func TestAbsorptionLedger_Outstanding_ReportsPlannedPerKey(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb", []absorption.ReserveEntry{
		sellEntry("WP-1", "IRON", 40, 400, time.Hour),
		sellEntry("WP-2", "GOLD", 25, 400, time.Hour),
	})
	require.NoError(t, err)
	require.True(t, ok)

	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 40, out[absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}].PlannedUnits)
	require.Equal(t, 25, out[absorption.LaneKey{Waypoint: "WP-2", Good: "GOLD", Side: "sell"}].PlannedUnits)
}

// --- Convert (PLANNED → EXECUTED shadow) ---

// A tagged sale converts the in-flight hold into a decaying recovery shadow stamped
// with realized units, the live tier, and a 12h hard cap.
func TestAbsorptionLedger_Convert_TaggedSale_WritesDecayingShadow(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 60, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	// Sold 40 of the 60 held into a WEAK sink of trade_volume 40 — one full tranche,
	// so the shadow (floor = 20) blocks at its realized units.
	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1, key, 40, "WEAK", 40))

	var row persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.Where("player_id = 1").First(&row).Error)
	require.Equal(t, "EXECUTED", row.State)
	require.Equal(t, 40, row.Units, "shadow carries REALIZED units, not the full hold")
	require.Equal(t, "WEAK", row.TierAtWrite)
	require.Equal(t, 40, row.TrancheSize)
	require.NotNil(t, row.ExecutedAt)

	// Fresh shadow (elapsed ~0) blocks at full realized units.
	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 40.0, out[key].RecoveringResidual, 0.5)
}

// Trade-analyst Q2: an UNTAGGED sink gets NO shadow — the PLANNED hold is released,
// the model cannot price what it has not fit.
func TestAbsorptionLedger_Convert_UntaggedSink_WritesNoShadow(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 60, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1, key, 40, "", 200))
	require.Equal(t, int64(0), countAbsorption(t, db), "an untagged sink leaves no row at all")
}

// A zero-unit sale (nothing sold) leaves no shadow — the hold is released.
func TestAbsorptionLedger_Convert_ZeroUnits_WritesNoShadow(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}
	_, _, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 60, 400, time.Hour)})
	require.NoError(t, err)

	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1, key, 0, "WEAK", 200))
	require.Equal(t, int64(0), countAbsorption(t, db))
}

// Convert is idempotent: a retry after the row has already converted finds no
// PLANNED row and is a clean no-op (no double shadow).
func TestAbsorptionLedger_Convert_Idempotent(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}
	_, _, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 60, 400, time.Hour)})
	require.NoError(t, err)

	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1, key, 40, "WEAK", 200))
	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1, key, 40, "WEAK", 200))
	require.Equal(t, int64(1), countAbsorption(t, db), "a second convert writes no second shadow")
}

// --- ReleaseByContainer: the tour re-plan / restart de-dup seam (sp-78ai L3) ---

func plannedCountFor(t *testing.T, db *gorm.DB, containerID string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.MarketAbsorptionLedgerModel{}).
		Where("container_id = ? AND state = ?", containerID, "PLANNED").Count(&n).Error)
	return n
}

// ReleaseByContainer drops ONLY the container's still-PLANNED rows: its own EXECUTED
// shadow (real recovering damage) survives, and another container's PLANNED rows are
// untouched. This is the tour writer's release-before-(re)plan invariant.
func TestAbsorptionLedger_ReleaseByContainer_DropsOnlyOwnPlanned(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	// ctr-A reserves two sinks; ctr-B reserves a third.
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "tour", []absorption.ReserveEntry{
		sellEntry("WP-1", "IRON", 40, 400, time.Hour),
		sellEntry("WP-2", "COPPER", 40, 400, time.Hour),
	})
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = ledger.Reserve(ctx, 1, "ctr-B", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-3", "GOLD", 40, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	// ctr-A converts one sink (WP-1) to an EXECUTED recovery shadow.
	require.NoError(t, ledger.ConvertByContainer(ctx, "ctr-A", 1,
		absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}, 40, "WEAK", 40))
	require.Equal(t, int64(3), countAbsorption(t, db)) // A: 1 PLANNED + 1 EXECUTED, B: 1 PLANNED

	dropped, err := ledger.ReleaseByContainer(ctx, "ctr-A", 1)
	require.NoError(t, err)
	require.Equal(t, 1, dropped, "only ctr-A's remaining PLANNED row (WP-2) is dropped")

	require.Equal(t, int64(0), plannedCountFor(t, db, "ctr-A"), "no PLANNED left for ctr-A")
	require.Equal(t, int64(1), plannedCountFor(t, db, "ctr-B"), "ctr-B's PLANNED is untouched")
	// ctr-A's EXECUTED shadow survives — real market damage still recovering.
	var execN int64
	require.NoError(t, db.Model(&persistence.MarketAbsorptionLedgerModel{}).
		Where("container_id = ? AND state = ?", "ctr-A", "EXECUTED").Count(&execN).Error)
	require.Equal(t, int64(1), execN, "the recovery shadow must persist across release")
	require.Equal(t, int64(2), countAbsorption(t, db)) // A: EXECUTED, B: PLANNED
}

// Release is idempotent and a blank container is a no-op — a restart-resumed tour with
// nothing yet planned, or a double-release, never errors.
func TestAbsorptionLedger_ReleaseByContainer_IdempotentAndBlankNoop(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "tour",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 40, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	dropped, err := ledger.ReleaseByContainer(ctx, "ctr-A", 1)
	require.NoError(t, err)
	require.Equal(t, 1, dropped)

	dropped, err = ledger.ReleaseByContainer(ctx, "ctr-A", 1)
	require.NoError(t, err)
	require.Equal(t, 0, dropped, "a second release finds nothing")

	dropped, err = ledger.ReleaseByContainer(ctx, "", 1)
	require.NoError(t, err)
	require.Equal(t, 0, dropped, "a blank container id is a no-op")
	require.Equal(t, int64(0), countAbsorption(t, db))
}

// --- Decay math + 50%-floor unblocking ---

// insertExecuted writes an EXECUTED shadow directly with a chosen age, so decay is
// exercised against real wall-clock deterministically.
func insertExecuted(t *testing.T, db *gorm.DB, key absorption.LaneKey, units, tranche int, tier string, age time.Duration, hardCap time.Duration) {
	t.Helper()
	executedAt := time.Now().Add(-age)
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: uuid.NewString(), PlayerID: 1, ContainerID: "dead", Engine: "idle-arb",
		Waypoint: key.Waypoint, Good: key.Good, Side: key.Side,
		State: "EXECUTED", Units: units, TrancheSize: tranche, TierAtWrite: tier,
		CreatedAt: executedAt, ExecutedAt: &executedAt, ExpiresAt: executedAt.Add(hardCap),
	}).Error)
}

// A WEAK shadow (60min half-life) decays to half at one half-life and a quarter at
// two — the fitted curve, read at wall-clock now.
func TestAbsorptionLedger_ShadowDecay_PerTierHalfLife(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}

	// tranche 10 → floor 5; residuals (50, 25) stay above it, so decay is visible.
	insertExecuted(t, db, key, 100, 10, "WEAK", time.Duration(testWeakHalfLifeMin)*time.Minute, 12*time.Hour)
	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 50.0, out[key].RecoveringResidual, 1.0, "one half-life → half the units remain occupied")

	// A STRONG shadow at two of ITS half-lives (240min, 120min each) → quarter.
	key2 := absorption.LaneKey{Waypoint: "WP-9", Good: "IRON", Side: "sell"}
	insertExecuted(t, db, key2, 100, 10, "STRONG", 2*time.Duration(testStrongHalfLifeMin)*time.Minute, 12*time.Hour)
	out, err = ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 25.0, out[key2].RecoveringResidual, 1.0, "two half-lives → a quarter remains occupied")
}

// The 50%-of-a-tranche floor: a shadow still above its floor blocks; once decay
// carries it below the floor it stops blocking (contributes 0) so a new sell may
// take the recovered depth (trade-analyst Q2).
func TestAbsorptionLedger_ShadowFloor_UnblocksBelowHalfTranche(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	// tranche 100 → floor 50. WEAK 100-unit shadow: at half a half-life residual is
	// ~70.7 (above the floor, still blocks); at two half-lives it is 25 (below the
	// floor, unblocks). Tested clear of the exact boundary to avoid clock jitter.
	blocking := absorption.LaneKey{Waypoint: "WP-BLOCK", Good: "IRON", Side: "sell"}
	insertExecuted(t, db, blocking, 100, 100, "WEAK", time.Duration(testWeakHalfLifeMin)*time.Minute/2, 12*time.Hour)

	unblocked := absorption.LaneKey{Waypoint: "WP-FREE", Good: "IRON", Side: "sell"}
	insertExecuted(t, db, unblocked, 100, 100, "WEAK", 2*time.Duration(testWeakHalfLifeMin)*time.Minute, 12*time.Hour)

	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 70.7, out[blocking].RecoveringResidual, 1.0, "above the floor the shadow still blocks")
	require.Equal(t, 0.0, out[unblocked].RecoveringResidual, "below the floor the shadow stops blocking")
}

// A recovering shadow below its floor no longer occupies depth for the cap check, so
// a new leg into that sink RESERVES successfully — the floor composes with Reserve.
func TestAbsorptionLedger_ReserveClearsPastRecoveredShadow(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}

	// A shadow recovered well past its floor (3 half-lives → 12.5 < floor 50).
	insertExecuted(t, db, key, 100, 100, "WEAK", 3*time.Duration(testWeakHalfLifeMin)*time.Minute, 12*time.Hour)

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-new", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok, "a sink whose shadow has recovered past the floor accepts a new leg")
}

// A still-blocking shadow keeps a new binary leg OUT (its residual exceeds the leg's
// own cap) — the recovery externality enforced in quantity space.
func TestAbsorptionLedger_ReserveBlockedByRecoveringShadow(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}

	// Fresh WEAK shadow of 100 units (residual ~100), tranche 100 (floor 50) → blocks.
	insertExecuted(t, db, key, 100, 100, "WEAK", time.Minute, 12*time.Hour)

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-new", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.False(t, ok, "a sink under a live recovery shadow rejects a new leg")
}

// --- Sweeps: TTL, 12h hard cap, dead-container reclaim ---

// A PLANNED row past its TTL is swept — a wedged container cannot hold depth forever.
func TestAbsorptionLedger_Sweep_ExpiresPlannedPastTTL(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: uuid.NewString(), PlayerID: 1, ContainerID: "ctr-wedged", Engine: "tour",
		Waypoint: "WP-1", Good: "IRON", Side: "sell", State: "PLANNED", Units: 50,
		CreatedAt: past.Add(-time.Hour), ExpiresAt: past,
	}).Error)

	n, err := ledger.Sweep(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, int64(0), countAbsorption(t, db))
}

// An EXECUTED shadow past its 12h hard cap is wiped regardless of decay (Q2) — and
// never appears in Outstanding once expired.
func TestAbsorptionLedger_Sweep_WipesExecutedPastHardCap(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, nil)
	ctx := context.Background()
	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}

	// Executed 13h ago with a 12h cap → expires_at is in the past.
	insertExecuted(t, db, key, 100, 10, "WEAK", 13*time.Hour, 12*time.Hour)

	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0, out[key].RecoveringResidual, "an expired shadow is filtered from reads")

	n, err := ledger.Sweep(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the sweep physically wipes the hard-capped shadow")
	require.Equal(t, int64(0), countAbsorption(t, db))
}

// Dead-container reclaim: a PLANNED row whose container is absent from the live set
// (and older than the grace) is reclaimed; a live container's hold survives.
func TestAbsorptionLedger_Sweep_ReclaimsDeadContainerPlanned(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, fakeLiveness{live: liveSet("ctr-live")})
	ctx := context.Background()

	old := time.Now().Add(-time.Minute) // older than the 30s reclaim grace
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: "dead-row", PlayerID: 1, ContainerID: "ctr-dead", Engine: "idle-arb",
		Waypoint: "WP-1", Good: "IRON", Side: "sell", State: "PLANNED", Units: 50,
		CreatedAt: old, ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: "live-row", PlayerID: 1, ContainerID: "ctr-live", Engine: "idle-arb",
		Waypoint: "WP-2", Good: "GOLD", Side: "sell", State: "PLANNED", Units: 50,
		CreatedAt: old, ExpiresAt: time.Now().Add(time.Hour),
	}).Error)

	n, err := ledger.Sweep(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the dead container's hold is reclaimed")

	var remaining persistence.MarketAbsorptionLedgerModel
	require.NoError(t, db.First(&remaining).Error)
	require.Equal(t, "live-row", remaining.ID, "the live container's hold survives")
}

// A dead container's leaked hold cannot wedge a sink: the next reserve sweeps it
// before its cap check, so a fresh leg proceeds. Without the reclaim the stale
// PLANNED 50 would breach the new binary leg.
func TestAbsorptionLedger_DeadContainerHoldDoesNotWedgeSink(t *testing.T) {
	ledger, db := setupAbsorptionLedger(t, fakeLiveness{live: liveSet("ctr-new")})
	ctx := context.Background()

	old := time.Now().Add(-time.Minute)
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: "dead-row", PlayerID: 1, ContainerID: "ctr-dead", Engine: "idle-arb",
		Waypoint: "WP-1", Good: "IRON", Side: "sell", State: "PLANNED", Units: 50,
		CreatedAt: old, ExpiresAt: time.Now().Add(time.Hour),
	}).Error)

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-new", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok, "the live leg proceeds once the dead container's hold is reclaimed")
}

// A liveness read error degrades to skip-reclaim (rely on TTL) — a hold we cannot
// confirm dead is never freed, and the reserve still succeeds.
func TestAbsorptionLedger_LivenessError_SkipsReclaimNotReserve(t *testing.T) {
	ledger, _ := setupAbsorptionLedger(t, fakeLiveness{err: context.DeadlineExceeded})
	ctx := context.Background()

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-A", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 50, 50, time.Hour)})
	require.NoError(t, err, "a liveness read error must not fail the reserve")
	require.True(t, ok)
}

// --- Restart survival + re-adoption (RULINGS #2) ---

// Rows are DB state: a fresh ledger instance (a daemon restart) sees prior rows via
// Outstanding, a recovered container keeps its hold, and only the unrecovered
// container's hold is reclaimed.
func TestAbsorptionLedger_RestartSurvivesAndReAdopts(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	artifact := writeTestRecoveryArtifact(t)

	// Pre-restart ledger reserves two legs on two containers.
	pre := persistence.NewAbsorptionLedger(db, artifact, persistence.AbsorptionLedgerConfig{}, fakeLiveness{live: liveSet("ctr-1", "ctr-2")})
	_, ok, err := pre.Reserve(ctx, 1, "ctr-1", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-1", "IRON", 40, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = pre.Reserve(ctx, 1, "ctr-2", "idle-arb",
		[]absorption.ReserveEntry{sellEntry("WP-2", "GOLD", 30, 400, time.Hour)})
	require.NoError(t, err)
	require.True(t, ok)

	// "Restart": a NEW ledger over the SAME db. Only ctr-1 recovered; ctr-2 did not.
	post := persistence.NewAbsorptionLedger(db, artifact, persistence.AbsorptionLedgerConfig{}, fakeLiveness{live: liveSet("ctr-1")})

	out, err := post.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 40, out[absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}].PlannedUnits,
		"the recovered container's hold survives the restart")

	// A sweep on the restarted ledger reclaims only the unrecovered container's hold.
	// (The rows predate the grace only if aged; force it by aging both created_at.)
	require.NoError(t, db.Model(&persistence.MarketAbsorptionLedgerModel{}).
		Where("player_id = 1").Update("created_at", time.Now().Add(-time.Minute)).Error)
	n, err := post.Sweep(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the unrecovered container's hold is reclaimed on restart")

	out, err = post.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 40, out[absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}].PlannedUnits)
	require.Equal(t, 0, out[absorption.LaneKey{Waypoint: "WP-2", Good: "GOLD", Side: "sell"}].PlannedUnits,
		"the unrecovered container re-links to nothing — its hold is gone")
}

// --- Fail-closed decay when the artifact is unreadable ---

// With no artifact, decay cannot be computed, so an EXECUTED residual is treated as
// UNDECAYED (full units) until its hard cap — fail closed, never optimistically
// freed (design §2, RULINGS #4).
func TestAbsorptionLedger_UnreadableArtifact_FailsClosedUndecayed(t *testing.T) {
	ctx := context.Background()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	ledger := persistence.NewAbsorptionLedger(db, filepath.Join(t.TempDir(), "missing.json"), persistence.AbsorptionLedgerConfig{}, nil)

	key := absorption.LaneKey{Waypoint: "WP-1", Good: "IRON", Side: "sell"}
	// 100-unit WEAK shadow aged well past several half-lives — WOULD decay to near 0
	// with a model, but with none it stays at full units.
	insertExecuted(t, db, key, 100, 10, "WEAK", 6*time.Hour, 12*time.Hour)

	out, err := ledger.Outstanding(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 100.0, out[key].RecoveringResidual, 0.5, "no model → undecayed, fail closed")
}
