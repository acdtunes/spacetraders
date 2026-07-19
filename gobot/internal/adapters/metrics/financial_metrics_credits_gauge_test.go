package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerQueries "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubCreditsGaugeMediator answers GetProfitLossQuery with a fixed response,
// just enough to drive the poller (updateProfitLoss) without a database.
type stubCreditsGaugeMediator struct {
	common.Mediator
	plResponse *ledgerQueries.GetProfitLossResponse
}

func (s *stubCreditsGaugeMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	return s.plResponse, nil
}

// stubCreditsGaugePlayerRepo mirrors the REAL GormPlayerRepository contract:
// FindByID never populates Credits from the DB (the column isn't persisted
// there - see player_repository.go's modelToPlayer). Callers who need a real
// balance are expected to fetch it live from the API and assign it
// themselves. A fake that returned a nonzero Credits here would not be
// testing the contract production code actually has.
type stubCreditsGaugePlayerRepo struct {
	player.PlayerRepository
	agentSymbol string
}

func (s *stubCreditsGaugePlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	return &player.Player{ID: id, AgentSymbol: s.agentSymbol, Credits: 0}, nil
}

// stubCreditsGaugeContainerInfo is the minimal ContainerInfo the poller
// touches: updateProfitLoss only reads PlayerID().
type stubCreditsGaugeContainerInfo struct {
	playerID int
}

func (s stubCreditsGaugeContainerInfo) PlayerID() int                     { return s.playerID }
func (s stubCreditsGaugeContainerInfo) Type() container.ContainerType     { return "" }
func (s stubCreditsGaugeContainerInfo) Status() container.ContainerStatus { return "" }
func (s stubCreditsGaugeContainerInfo) RestartCount() int                 { return 0 }
func (s stubCreditsGaugeContainerInfo) CurrentIteration() int             { return 0 }
func (s stubCreditsGaugeContainerInfo) RuntimeDuration() time.Duration    { return 0 }

// TestUpdateProfitLoss_NeverStompsCreditsBalanceToZero pins the poller's
// must-not-overwrite guarantee.
//
// player_credits_balance has two writers: the 60s P&L poller
// (updateProfitLoss) and RecordTransaction (fired per ledger entry with the
// authoritative/reconstructed balance). GormPlayerRepository.FindByID never
// populates Credits from the DB (by design; real credits require a live API
// fetch the poller never makes), so the poller must never write this gauge —
// RecordTransaction is its sole writer.
func TestUpdateProfitLoss_NeverStompsCreditsBalanceToZero(t *testing.T) {
	const playerID = 42
	const agentSymbol = "TEST_AGENT"
	const realBalance = 3245000

	mediator := &stubCreditsGaugeMediator{
		plResponse: &ledgerQueries.GetProfitLossResponse{
			RevenueBreakdown: map[string]int{},
			ExpenseBreakdown: map[string]int{},
			NetProfit:        realBalance,
		},
	}
	playerRepo := &stubCreditsGaugePlayerRepo{agentSymbol: agentSymbol}
	getContainers := func() map[string]ContainerInfo {
		return map[string]ContainerInfo{
			"HULL-1": stubCreditsGaugeContainerInfo{playerID: playerID},
		}
	}

	collector := NewFinancialMetricsCollector(mediator, playerRepo, getContainers)

	// A real transaction lands first - the ledger's authoritative value.
	collector.RecordTransaction(playerID, agentSymbol, "SELL_CARGO", "trade", 1000, realBalance, "tour")

	if got := testutil.ToFloat64(collector.creditsBalance.WithLabelValues("42", agentSymbol)); got != realBalance {
		t.Fatalf("after RecordTransaction: creditsBalance = %v, want %v", got, realBalance)
	}

	// The 60s P&L poll tick fires next.
	collector.updateProfitLoss()

	if got := testutil.ToFloat64(collector.creditsBalance.WithLabelValues("42", agentSymbol)); got != realBalance {
		t.Fatalf("after poller tick: creditsBalance = %v, want %v (poller must never overwrite the accurate per-transaction balance with a DB-sourced zero)", got, realBalance)
	}
}

// TestUpdateProfitLoss_StillUpdatesProfitAndLossMetrics guards against a fix
// that silences the poller entirely: revenue/expense/net-profit are this
// poller's actual job (unlike the credits gauge) and must keep landing.
func TestUpdateProfitLoss_StillUpdatesProfitAndLossMetrics(t *testing.T) {
	const playerID = 7
	const agentSymbol = "OTHER_AGENT"

	mediator := &stubCreditsGaugeMediator{
		plResponse: &ledgerQueries.GetProfitLossResponse{
			RevenueBreakdown: map[string]int{"trade": 500},
			ExpenseBreakdown: map[string]int{"fuel": 200},
			NetProfit:        300,
		},
	}
	playerRepo := &stubCreditsGaugePlayerRepo{agentSymbol: agentSymbol}
	getContainers := func() map[string]ContainerInfo {
		return map[string]ContainerInfo{
			"HULL-2": stubCreditsGaugeContainerInfo{playerID: playerID},
		}
	}

	collector := NewFinancialMetricsCollector(mediator, playerRepo, getContainers)
	collector.updateProfitLoss()

	if got := testutil.ToFloat64(collector.totalRevenue.WithLabelValues("7", agentSymbol, "trade")); got != 500 {
		t.Errorf("totalRevenue[trade] = %v, want 500", got)
	}
	if got := testutil.ToFloat64(collector.totalExpenses.WithLabelValues("7", agentSymbol, "fuel")); got != 200 {
		t.Errorf("totalExpenses[fuel] = %v, want 200", got)
	}
	if got := testutil.ToFloat64(collector.netProfit.WithLabelValues("7")); got != 300 {
		t.Errorf("netProfit = %v, want 300", got)
	}
}
