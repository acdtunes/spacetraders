package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

var (
	player      = shared.MustNewPlayerID(1)
	otherPlayer = shared.MustNewPlayerID(2)
)

func ctx() context.Context { return context.Background() }

func hoursAgo(h float64) time.Time {
	return time.Now().Add(-time.Duration(h * float64(time.Hour)))
}

// seededTx is one ledger row expressed as what these tests care about: whose it is, when it
// landed, and the balance it left behind.
type seededTx struct {
	playerID     shared.PlayerID
	at           time.Time
	balanceAfter int
}

func txAt(at time.Time, balanceAfter int) seededTx {
	return seededTx{playerID: player, at: at, balanceAfter: balanceAfter}
}

func playerTx(pid shared.PlayerID, at time.Time, balanceAfter int) seededTx {
	return seededTx{playerID: pid, at: at, balanceAfter: balanceAfter}
}

func seedTransactions(t *testing.T, rows ...seededTx) *persistence.GormTransactionRepository {
	t.Helper()
	return seedTransactionsForPlayers(t, rows...)
}

// seedTransactionsForPlayers builds a real SQLite-backed repository. The rows go in through the
// model rather than through the domain writer so a test can pin an exact balance at an exact
// instant, which is the whole subject here.
func seedTransactionsForPlayers(t *testing.T, rows ...seededTx) *persistence.GormTransactionRepository {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// transactions carry a foreign key onto players, enforced in the test harness.
	for _, pid := range []shared.PlayerID{player, otherPlayer} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID:          pid.Value(),
			AgentSymbol: fmt.Sprintf("HIGH-WATER-%d", pid.Value()),
			Token:       "tok",
			CreatedAt:   time.Now(),
		}).Error)
	}

	for i, row := range rows {
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID:              fmt.Sprintf("tx-high-water-%d", i),
			PlayerID:        row.playerID.Value(),
			Timestamp:       row.at,
			CreatedAt:       row.at,
			TransactionType: "SELL_CARGO",
			Category:        "TRADING_REVENUE",
			Amount:          row.balanceAfter,
			BalanceBefore:   0,
			BalanceAfter:    row.balanceAfter,
		}).Error)
	}

	return persistence.NewGormTransactionRepository(db)
}

// The high-water mark is the PEAK balance across the window — not the latest, and not an
// average. It is what makes the reachability clause independent of where in a trade cycle the
// tick landed.
func TestTreasuryHighWaterSince_ReturnsThePeakNotTheLatest(t *testing.T) {
	repo := seedTransactions(t,
		txAt(hoursAgo(0.9), 300_000),
		txAt(hoursAgo(0.5), 1_500_000), // the peak
		txAt(hoursAgo(0.1), 119_000),   // the latest
	)
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil || !readable {
		t.Fatalf("expected a readable peak, got readable=%v err=%v", readable, err)
	}
	if hw != 1_500_000 {
		t.Fatalf("expected the peak 1500000, got %d", hw)
	}
}

// EMPTY IS NOT ZERO. A window with no rows must report UNREADABLE — a zero would be the strong
// claim "this fleet has never held money", which the predicate would correctly treat as a genuine
// unreachable.
func TestTreasuryHighWaterSince_EmptyWindowIsUnreadableNotZero(t *testing.T) {
	repo := seedTransactions(t) // no rows
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("an empty window is not an error: %v", err)
	}
	if readable {
		t.Fatalf("an empty window must be UNREADABLE, got readable with high-water %d", hw)
	}
}

// A genuine zero balance in the window is READABLE. The two cases must stay distinguishable, or
// a blind read reports as a verdict.
func TestTreasuryHighWaterSince_GenuineZeroIsReadable(t *testing.T) {
	repo := seedTransactions(t, txAt(hoursAgo(0.5), 0))
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil || !readable || hw != 0 {
		t.Fatalf("expected a readable 0, got (%d,%v,%v)", hw, readable, err)
	}
}

// Rows outside the window are not the fleet's current capacity. A windfall two cycles ago must
// age out, or the mark never decays and the fleet is judged on history it has spent.
func TestTreasuryHighWaterSince_IgnoresRowsOutsideTheWindow(t *testing.T) {
	repo := seedTransactions(t,
		txAt(hoursAgo(3), 9_000_000), // long gone
		txAt(hoursAgo(0.5), 400_000),
	)
	hw, readable, _ := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if !readable || hw != 400_000 {
		t.Fatalf("expected the in-window peak 400000, got %d readable=%v", hw, readable)
	}
}

// Scoped per player, like every other read on this table.
func TestTreasuryHighWaterSince_IsPlayerScoped(t *testing.T) {
	repo := seedTransactionsForPlayers(t,
		playerTx(player, hoursAgo(0.5), 400_000),
		playerTx(otherPlayer, hoursAgo(0.5), 9_000_000),
	)
	hw, _, _ := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if hw != 400_000 {
		t.Fatalf("another player's balance leaked in: got %d", hw)
	}
}
