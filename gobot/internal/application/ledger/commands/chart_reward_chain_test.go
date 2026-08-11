package commands_test

// The defect this reproduces: a charting reward lands in the balance with no ledger row, so the
// NEXT anchored transaction's balance_before leaps past the previous row's balance_after by the
// reward. The chain is what a treasury read walks, and a gap in it is credits nothing can explain.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// chainHarness wires the REAL RecordTransactionHandler over a real test DB behind a real
// mediator, so the recorder exercises the true recording path rather than a spy that would
// accept any balance at all. The clock is stepped between writes so row order is deterministic.
type chainHarness struct {
	med      mediator.Mediator
	repo     ledger.TransactionRepository
	clock    *shared.MockClock
	playerID shared.PlayerID
}

func newChainHarness(t *testing.T) *chainHarness {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)}
	repo := persistence.NewGormTransactionRepository(db)
	med := mediator.NewMediator()
	require.NoError(t, mediator.RegisterHandler[*ledgerCommands.RecordTransactionCommand](
		med, ledgerCommands.NewRecordTransactionHandler(repo, clock)))

	return &chainHarness{med: med, repo: repo, clock: clock, playerID: shared.MustNewPlayerID(playerRow.ID)}
}

// anchored records an ordinary transaction whose own API response reported the post-transaction
// credits — the same re-anchoring shape a jump gate fee or a cargo sale uses.
func (h *chainHarness) anchored(t *testing.T, txType string, amount, credits int) {
	t.Helper()
	h.clock.Advance(time.Second)
	_, err := h.med.Send(context.Background(), &ledgerCommands.RecordTransactionCommand{
		PlayerID: h.playerID.Value(), TransactionType: txType, Amount: amount,
		BalanceBefore: 0, BalanceAfter: amount,
		AuthoritativeBalance: &credits, Description: "anchored",
	})
	require.NoError(t, err)
}

func (h *chainHarness) rowsInOrder(t *testing.T) []*ledger.Transaction {
	t.Helper()
	rows, err := h.repo.FindByPlayer(context.Background(), h.playerID, ledger.QueryOptions{
		Limit: 0, OrderBy: "timestamp ASC, created_at ASC",
	})
	require.NoError(t, err)
	return rows
}

// TestChartRewardKeepsTheBalanceChainContinuous is THE acceptance test for the defect.
//
// Kills the mutation "drop the chart recording": the JUMP row's balance_before is derived from
// the API's own post-jump credits, so it already accounts for the reward; without the CHART row
// in between, it exceeds the previous row's balance_after by exactly the reward.
func TestChartRewardKeepsTheBalanceChainContinuous(t *testing.T) {
	const startingCredits, reward, gateFee = 1000000, 10000, 6000
	h := newChainHarness(t)

	h.anchored(t, string(ledger.TransactionTypeSellCargo), 30000, startingCredits)

	postChart := startingCredits + reward
	h.clock.Advance(time.Second)
	ledgerCommands.RecordChartReward(context.Background(), h.med, h.playerID, "TORWIND-16",
		&ports.ChartResult{WaypointSymbol: "X1-DA78-C24B", Reward: reward, AgentCredits: &postChart})

	h.anchored(t, string(ledger.TransactionTypeJump), -gateFee, postChart-gateFee)

	rows := h.rowsInOrder(t)
	require.NotEmpty(t, rows, "precondition: there must be a chain to walk")
	for i := 1; i < len(rows); i++ {
		require.Equal(t, rows[i-1].BalanceAfter(), rows[i].BalanceBefore(),
			"row %d (%s) opens at %d but row %d (%s) closed at %d: an unexplained %d gap in the chain",
			i, rows[i].TransactionType(), rows[i].BalanceBefore(),
			i-1, rows[i-1].TransactionType(), rows[i-1].BalanceAfter(),
			rows[i].BalanceBefore()-rows[i-1].BalanceAfter())
	}
	require.Len(t, rows, 3, "the chain must carry the anchor, the CHART reward and the jump")

	chart, jump := rows[1], rows[2]
	require.Equal(t, string(ledger.TransactionTypeChart), string(chart.TransactionType()))
	require.Equal(t, startingCredits, chart.BalanceBefore())
	require.Equal(t, postChart, chart.BalanceAfter(), "the reward must re-anchor to the in-band credits")
	require.Equal(t, chart.BalanceAfter(), jump.BalanceBefore(),
		"the jump must chain off the CHART row, not leap over it")
}

// With no agent block the reward is still recorded, reconstructing balance_after from the chain
// rather than re-anchoring. Recording nothing would reopen the same gap for a quieter reason.
func TestChartRewardWithoutInBandCreditsReconstructsFromTheChain(t *testing.T) {
	const startingCredits, reward = 250000, 4000
	h := newChainHarness(t)

	h.anchored(t, string(ledger.TransactionTypeSellCargo), 12000, startingCredits)

	h.clock.Advance(time.Second)
	ledgerCommands.RecordChartReward(context.Background(), h.med, h.playerID, "TORWIND-16",
		&ports.ChartResult{WaypointSymbol: "X1-DA78-C24B", Reward: reward})

	rows := h.rowsInOrder(t)
	require.Len(t, rows, 2)
	require.Equal(t, startingCredits, rows[1].BalanceBefore())
	require.Equal(t, startingCredits+reward, rows[1].BalanceAfter())
}
