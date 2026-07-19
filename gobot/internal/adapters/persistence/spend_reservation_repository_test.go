package persistence_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The per-buy floor checks live treasury PER CONTAINER, so N factory
// containers can each clear it inside their own check->buy window and collectively dip below
// the reserve. This suite drives the shared-DB reservation ledger that closes that race:
// "record my spend intent, then verify live treasury minus the SUM of all active reservations
// still clears the reserve" as one serialized atomic step.
//
// Fixed economics below: reserve floor 50000, live treasury 60000. A single 8000 buy clears
// (60000-8000=52000 >= 50000); two of them together breach (60000-16000=44000 < 50000). So
// each buy is individually affordable and ONLY the combined in-flight exposure trips the cap —
// exactly the race the per-buy floor cannot see.
const (
	testReserveFloor   = 50000
	testLiveCredits    = 60000
	testAffordableCost = 8000
)

func setupSpendLedger(t *testing.T) (*persistence.SpendReservationLedgerGORM, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return persistence.NewSpendReservationLedger(db), db
}

func countReservations(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&persistence.SpendReservationModel{}).Count(&n).Error)
	return n
}

// A lone buy that clears the reserve proceeds and its reservation persists.
func TestSpendReservationLedger_ReserveWithinReserve_Proceeds(t *testing.T) {
	ledger, db := setupSpendLedger(t)
	ctx := context.Background()

	resID, ok, err := ledger.Reserve(ctx, 1, "ctr-A", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, ok, "a buy that clears the reserve must proceed")
	require.NotEmpty(t, resID)
	require.Equal(t, int64(1), countReservations(t, db), "the proceeding reservation must persist")
}

// The core race closure, made deterministic: with reservation A already in flight, a second
// buy whose COMBINED cost breaches the reserve must be rejected — and rolled back, so a
// rejected reservation never lingers to poison later checks.
func TestSpendReservationLedger_SequentialCombinedBreach_SecondParksAndRollsBack(t *testing.T) {
	ledger, db := setupSpendLedger(t)
	ctx := context.Background()

	resA, okA, err := ledger.Reserve(ctx, 1, "ctr-A", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, okA)
	require.NotEmpty(t, resA)

	resB, okB, err := ledger.Reserve(ctx, 1, "ctr-B", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err, "a cap breach is a park, not an error")
	require.False(t, okB, "the second buy's combined spend breaches the reserve — it must park")
	require.Empty(t, resB)

	require.Equal(t, int64(1), countReservations(t, db), "the parked reservation must be rolled back, leaving only A")
}

// Acceptance #1: two CONCURRENT buys whose combined cost would breach the reserve — no
// interleaving may let both through. Exactly one proceeds; the other parks.
func TestSpendReservationLedger_ConcurrentCombinedBreach_ExactlyOneProceeds(t *testing.T) {
	ledger, db := setupSpendLedger(t)
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
		go func(n int) {
			defer wg.Done()
			<-start // release both goroutines together to maximize contention
			_, ok, err := ledger.Reserve(ctx, 1, "ctr", testAffordableCost, testLiveCredits, testReserveFloor)
			require.NoError(t, err)
			if ok {
				mu.Lock()
				proceed++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, proceed, "exactly one of two combined-breaching concurrent buys may proceed")
	require.Equal(t, int64(1), countReservations(t, db), "only the winner's reservation may persist")
}

// Acceptance #2: releasing a completed buy's reservation frees the budget — a buy that
// parked while an earlier reservation was in flight succeeds once that reservation releases.
func TestSpendReservationLedger_ReleaseFreesBudget(t *testing.T) {
	ledger, db := setupSpendLedger(t)
	ctx := context.Background()

	resA, okA, err := ledger.Reserve(ctx, 1, "ctr-A", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, okA)

	_, okB, err := ledger.Reserve(ctx, 1, "ctr-B", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.False(t, okB, "B breaches while A is in flight")

	require.NoError(t, ledger.Release(ctx, resA))

	_, okB2, err := ledger.Reserve(ctx, 1, "ctr-B", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, okB2, "with A released, B's spend clears the reserve and must proceed")
	require.Equal(t, int64(1), countReservations(t, db), "only B's reservation remains after A released")
}

// Releasing an already-gone reservation (e.g. one the staleness sweep already reclaimed) is a
// no-op, not an error — release must never fail an otherwise-successful buy.
func TestSpendReservationLedger_ReleaseMissingIsNoOp(t *testing.T) {
	ledger, _ := setupSpendLedger(t)
	require.NoError(t, ledger.Release(context.Background(), "does-not-exist"))
	require.NoError(t, ledger.Release(context.Background(), ""))
}

// Acceptance #3a: the staleness sweep removes reservations older than the window.
func TestSpendReservationLedger_ExpireStale_RemovesOldRows(t *testing.T) {
	ledger, db := setupSpendLedger(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.SpendReservationModel{
		ID: "stale-1", PlayerID: 1, ContainerID: "ctr-dead",
		ProjectedCost: testAffordableCost, CreatedAt: time.Now().Add(-10 * time.Minute),
	}).Error)
	require.NoError(t, db.Create(&persistence.SpendReservationModel{
		ID: "fresh-1", PlayerID: 1, ContainerID: "ctr-live",
		ProjectedCost: testAffordableCost, CreatedAt: time.Now(),
	}).Error)

	removed, err := ledger.ExpireStale(ctx, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, removed, "only the row older than the window is reclaimed")
	require.Equal(t, int64(1), countReservations(t, db), "the fresh reservation survives")
}

// Acceptance #3b: a dead container's un-released reservation cannot wedge the budget — the
// next live buy's own Reserve sweeps the stale hold before summing, so it proceeds. Without
// the sweep the stale 20000 hold plus this 8000 buy would breach (60000-28000=32000 < 50000).
func TestSpendReservationLedger_StaleReservationDoesNotWedgeBudget(t *testing.T) {
	ledger, db := setupSpendLedger(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&persistence.SpendReservationModel{
		ID: "stale-big", PlayerID: 1, ContainerID: "ctr-dead",
		ProjectedCost: 20000, CreatedAt: time.Now().Add(-10 * time.Minute),
	}).Error)

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-live", testAffordableCost, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, ok, "the live buy must proceed once the dead container's stale hold is swept")
	require.Equal(t, int64(1), countReservations(t, db), "the stale hold is gone, only the live reservation remains")
}

// Reservations are scoped per player: one player's in-flight spend never caps another's.
func TestSpendReservationLedger_ScopedPerPlayer(t *testing.T) {
	ledger, _ := setupSpendLedger(t)
	ctx := context.Background()

	// Player 1 reserves right up to the edge (a big buy that alone clears).
	_, ok1, err := ledger.Reserve(ctx, 1, "ctr-A", 9000, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, ok1)

	// Player 2's buy must not see player 1's reservation in its sum.
	_, ok2, err := ledger.Reserve(ctx, 2, "ctr-B", 9000, testLiveCredits, testReserveFloor)
	require.NoError(t, err)
	require.True(t, ok2, "player 2's cap must ignore player 1's reservations")
}
