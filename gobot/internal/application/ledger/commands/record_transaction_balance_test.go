package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// Callers that skip the balance fetch pass BalanceBefore/After = 0, which used
// to corrupt the ledger's running balance (latest balance_after read -216
// while the real treasury was ~175k). Zero/zero with a nonzero amount is
// arithmetically impossible, so the handler derives balances from the last
// recorded transaction instead (the hack passes before=0, after=amount).
func TestZeroBalancesAreDerivedFromLedgerChain(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)
	ctx := context.Background()

	// Anchor: a correctly-recorded transaction.
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "CONTRACT_ACCEPTED", Amount: 1547,
		BalanceBefore: 175000, BalanceAfter: 176547, Description: "anchor",
	})
	require.NoError(t, err)

	// Refuel with the balance-skip hack (0/0).
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "REFUEL", Amount: -216,
		BalanceBefore: 0, BalanceAfter: -216, Description: "refuel",
	})
	require.NoError(t, err)

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(ctx, pid, ledger.QueryOptions{Limit: 1, OrderBy: "timestamp DESC"})
	require.NoError(t, err)
	require.Len(t, txs, 1)
	require.Equal(t, 176547, txs[0].BalanceBefore(), "derive from last balance_after")
	require.Equal(t, 176331, txs[0].BalanceAfter())
}

func TestFirstEverTransactionWithNoPriorChainKeepsZeroBasedBalances(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT4", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)

	_, err = h.Handle(context.Background(), &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "CONTRACT_ACCEPTED", Amount: 5000,
		BalanceBefore: 0, BalanceAfter: 5000, Description: "first ever",
	})
	require.NoError(t, err)

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(context.Background(), pid, ledger.QueryOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, txs, 1)
	require.Equal(t, 0, txs[0].BalanceBefore())
	require.Equal(t, 5000, txs[0].BalanceAfter())
}

func TestExplicitBalancesAreNotOverridden(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT2", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)

	_, err = h.Handle(context.Background(), &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "SELL_CARGO", Amount: 500,
		BalanceBefore: 9500, BalanceAfter: 10000, Description: "explicit",
	})
	require.NoError(t, err)

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(context.Background(), pid, ledger.QueryOptions{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 9500, txs[0].BalanceBefore())
	require.Equal(t, 10000, txs[0].BalanceAfter())
}

// Concurrent recordings with the balance-skip hack must not fork the chain:
// two writers reading the same "last balance" produced the L28 garbage rows
// (s39/s51/s55/s63). The handler must serialize per player.
func TestConcurrentZeroBalanceRecordingsDoNotForkTheChain(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT3", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)
	ctx := context.Background()

	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "CONTRACT_ACCEPTED", Amount: 100000,
		BalanceBefore: 0, BalanceAfter: 100000, Description: "anchor",
	})
	require.NoError(t, err)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, herr := h.Handle(ctx, &RecordTransactionCommand{
				PlayerID: p.ID, TransactionType: "REFUEL", Amount: -100,
				BalanceBefore: 0, BalanceAfter: -100, Description: "hop",
			})
			errs <- herr
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(ctx, pid, ledger.QueryOptions{Limit: 0})
	require.NoError(t, err)
	require.Len(t, txs, n+1)
	seen := map[int]bool{}
	min := 100000
	for _, tx := range txs {
		require.Equal(t, tx.BalanceBefore()+tx.Amount(), tx.BalanceAfter(), "row arithmetic")
		require.False(t, seen[tx.BalanceAfter()], "duplicate balance = forked chain")
		seen[tx.BalanceAfter()] = true
		if tx.BalanceAfter() < min {
			min = tx.BalanceAfter()
		}
	}
	require.Equal(t, 100000-n*100, min,
		"complete unforked chain reaches exactly the summed balance")
}
