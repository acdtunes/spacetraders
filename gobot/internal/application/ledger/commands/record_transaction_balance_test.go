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

// In-band agent.credits returned by a transaction's own API response is ground
// truth: it must re-anchor the running chain even when the reconstructed value
// would differ. This is the fix for balance_after forking away from the API
// (sp-sc6u): purchase/sell/refuel/contract responses carry data.agent.credits,
// and the ledger must prefer it over lastBalance+amount reconstruction.
func TestAuthoritativeBalanceReanchorsTheChain(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT5", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)
	ctx := context.Background()

	// Anchor the in-memory chain at 100000 via reconstruction.
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "CONTRACT_ACCEPTED", Amount: 100000,
		BalanceBefore: 0, BalanceAfter: 100000, Description: "anchor",
	})
	require.NoError(t, err)

	// The reconstructed chain would say 100000-5000 = 95000, but the API
	// reported the agent actually holds 130000 after this purchase (another
	// income landed out-of-band). The authoritative value must win.
	authoritative := 130000
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "PURCHASE_CARGO", Amount: -5000,
		BalanceBefore: 0, BalanceAfter: -5000, // caller's zero-baseline hack
		AuthoritativeBalance: &authoritative,
		Description:          "purchase with in-band credits",
	})
	require.NoError(t, err)

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(ctx, pid, ledger.QueryOptions{Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC"})
	require.NoError(t, err)
	require.Len(t, txs, 1)
	require.Equal(t, 130000, txs[0].BalanceAfter(), "balance_after must equal in-band agent.credits")
	require.Equal(t, 135000, txs[0].BalanceBefore(), "balance_before derived as credits - amount")

	// A subsequent reconstruction chains off the re-anchored (authoritative) value.
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "REFUEL", Amount: -1000,
		BalanceBefore: 0, BalanceAfter: -1000, Description: "refuel after re-anchor",
	})
	require.NoError(t, err)
	txs, err = repo.FindByPlayer(ctx, pid, ledger.QueryOptions{Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC"})
	require.NoError(t, err)
	require.Equal(t, 129000, txs[0].BalanceAfter(), "reconstruction chains off the authoritative anchor")
}

// Manufacturing records send no balance fields at all (BalanceBefore=0,
// BalanceAfter=0). The old heuristic only reconstructed when after==amount, so
// 0/0 fell through to the "explicit" path and either violated the balance
// invariant or reset the running balance to zero (sp-sc6u root cause #2). A
// zero balance_before with a nonzero amount must always reconstruct from the
// chain, never zero it.
func TestZeroZeroManufacturingRecordReconstructsInsteadOfZeroing(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT6", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	repo := persistence.NewGormTransactionRepository(db)
	h := NewRecordTransactionHandler(repo, nil)
	ctx := context.Background()

	// Establish a running balance.
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "CONTRACT_ACCEPTED", Amount: 200000,
		BalanceBefore: 0, BalanceAfter: 200000, Description: "anchor",
	})
	require.NoError(t, err)

	// Manufacturing purchase: amount only, both balance fields zero.
	_, err = h.Handle(ctx, &RecordTransactionCommand{
		PlayerID: p.ID, TransactionType: "PURCHASE_CARGO", Amount: -5000,
		BalanceBefore: 0, BalanceAfter: 0, Description: "mfg: buy 10 IRON_ORE",
	})
	require.NoError(t, err)

	pid, _ := shared.NewPlayerID(p.ID)
	txs, err := repo.FindByPlayer(ctx, pid, ledger.QueryOptions{Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC"})
	require.NoError(t, err)
	require.Len(t, txs, 1)
	require.Equal(t, 200000, txs[0].BalanceBefore(), "chain from last balance, not zero")
	require.Equal(t, 195000, txs[0].BalanceAfter(), "must not collapse the running balance to zero")
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
