package metrics

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerQueries "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FinancialMetricsCollector handles all financial metrics (credits, transactions, P&L)
type FinancialMetricsCollector struct {
	// Dependencies
	mediator      common.Mediator
	playerRepo    player.PlayerRepository         // For fetching player data
	getContainers func() map[string]ContainerInfo // Function to get current containers

	// Balance metrics
	creditsBalance *prometheus.GaugeVec

	// Transaction metrics
	transactionsTotal *prometheus.CounterVec
	transactionAmount *prometheus.HistogramVec

	// Ledger-flow counters (sp-miqt): monotonic signed-amount sums split by
	// sign so PromQL rate() can drive the cr/hr financial panels. Labeled by
	// operation_type (contract/tour/arbitrage/...) + category + player_id.
	ledgerRevenueTotal *prometheus.CounterVec // += amount when amount > 0
	ledgerCostTotal    *prometheus.CounterVec // += -amount when amount < 0

	// P&L metrics
	totalRevenue  *prometheus.GaugeVec
	totalExpenses *prometheus.GaugeVec
	netProfit     *prometheus.GaugeVec

	// Trade profitability metrics (optional)
	tradeProfitPerUnit *prometheus.HistogramVec
	tradeMarginPercent *prometheus.HistogramVec

	// Lifecycle
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// NewFinancialMetricsCollector creates a new financial metrics collector
func NewFinancialMetricsCollector(
	mediator common.Mediator,
	playerRepo player.PlayerRepository,
	getContainers func() map[string]ContainerInfo,
) *FinancialMetricsCollector {
	return &FinancialMetricsCollector{
		mediator:      mediator,
		playerRepo:    playerRepo,
		getContainers: getContainers,

		// Current credits balance gauge
		creditsBalance: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "player_credits_balance",
				Help:      "Current credits balance for each player",
			},
			[]string{"player_id", "agent"},
		),

		// Transaction count by type/category
		transactionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "transactions_total",
				Help:      "Total number of transactions by type and category",
			},
			[]string{"player_id", "type", "category"},
		),

		// Transaction amount distribution
		transactionAmount: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "transaction_amount",
				Help:      "Transaction amount distribution",
				Buckets:   []float64{100, 500, 1000, 5000, 10000, 50000, 100000, 500000},
			},
			[]string{"player_id", "type", "category"},
		),

		// Ledger revenue (positive inflow) running total by operation/category (sp-miqt)
		ledgerRevenueTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "ledger_revenue_total",
				Help:      "Cumulative positive ledger amount (revenue) by operation_type and category",
			},
			[]string{"operation_type", "category", "player_id"},
		),

		// Ledger cost (negative outflow magnitude) running total by operation/category (sp-miqt)
		ledgerCostTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "ledger_cost_total",
				Help:      "Cumulative negative ledger amount magnitude (cost) by operation_type and category",
			},
			[]string{"operation_type", "category", "player_id"},
		),

		// Total revenue by category
		totalRevenue: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "total_revenue",
				Help:      "Total revenue by category",
			},
			[]string{"player_id", "agent", "category"},
		),

		// Total expenses by category
		totalExpenses: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "total_expenses",
				Help:      "Total expenses by category",
			},
			[]string{"player_id", "agent", "category"},
		),

		// Net profit
		netProfit: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "net_profit",
				Help:      "Net profit (revenue - expenses)",
			},
			[]string{"player_id"},
		),

		// Profit per unit (for trades)
		tradeProfitPerUnit: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "trade_profit_per_unit",
				Help:      "Profit per unit from trades",
				Buckets:   []float64{1, 5, 10, 50, 100, 500, 1000},
			},
			[]string{"player_id", "good_symbol"},
		),

		// Trade margin percentage
		tradeMarginPercent: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "trade_margin_percent",
				Help:      "Trade margin percentage ((sell-buy)/buy * 100)",
				Buckets:   []float64{5, 10, 25, 50, 75, 100, 150, 200},
			},
			[]string{"player_id", "good_symbol"},
		),
	}
}

// Register registers all financial metrics with the Prometheus registry
func (c *FinancialMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.creditsBalance,
		c.transactionsTotal,
		c.transactionAmount,
		c.ledgerRevenueTotal,
		c.ledgerCostTotal,
		c.totalRevenue,
		c.totalExpenses,
		c.netProfit,
		c.tradeProfitPerUnit,
		c.tradeMarginPercent,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// Start begins the P&L polling goroutine
func (c *FinancialMetricsCollector) Start(ctx context.Context) {
	c.ctx, c.cancelFunc = context.WithCancel(ctx)

	// Start P&L polling (every 60 seconds)
	c.wg.Add(1)
	go c.pollProfitLoss(60 * time.Second)
}

// Stop gracefully stops the financial metrics collector
func (c *FinancialMetricsCollector) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	c.wg.Wait()
}

// pollProfitLoss polls P&L data periodically
func (c *FinancialMetricsCollector) pollProfitLoss(interval time.Duration) {
	defer c.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do initial poll immediately
	c.updateProfitLoss()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateProfitLoss()
		}
	}
}

// updateProfitLoss fetches and updates P&L metrics
func (c *FinancialMetricsCollector) updateProfitLoss() {
	if c.mediator == nil {
		return
	}

	// Get unique player IDs from active containers
	containers := c.getContainers()
	if len(containers) == 0 {
		// No active containers, skip P&L metrics
		return
	}

	playerIDs := make(map[int]bool)
	for _, containerInfo := range containers {
		playerIDs[containerInfo.PlayerID()] = true
	}

	// Collect metrics for each player with active containers
	for playerID := range playerIDs {
		// Execute GetProfitLossQuery for all-time P&L
		// Use epoch start and far future to capture all transactions
		query := &ledgerQueries.GetProfitLossQuery{
			PlayerID:  playerID,
			StartDate: time.Unix(0, 0),                // Epoch start (1970-01-01)
			EndDate:   time.Now().Add(24 * time.Hour), // Tomorrow to ensure we get everything
		}

		response, err := c.mediator.Send(context.Background(), query)
		if err != nil {
			log.Printf("Failed to fetch profit/loss for player %d: %v", playerID, err)
			continue // Skip this player but continue with others
		}

		plResponse, ok := response.(*ledgerQueries.GetProfitLossResponse)
		if !ok {
			log.Printf("Unexpected response type for P&L query: %T", response)
			continue
		}

		playerIDStr := strconv.Itoa(playerID)

		// Fetch player data from database to get agent symbol
		playerEntity, err := c.playerRepo.FindByID(context.Background(), shared.MustNewPlayerID(playerID))
		if err != nil {
			log.Printf("Failed to fetch player %d from database: %v", playerID, err)
			continue // Skip this player if we can't fetch their data
		}

		agentSymbol := playerEntity.AgentSymbol

		// player_credits_balance is intentionally NOT written here (sp-m1n2).
		// playerRepo.FindByID never populates Credits from the DB - the
		// credits column isn't persisted there; GormPlayerRepository.modelToPlayer
		// always returns Credits: 0 and expects callers who need a real balance
		// to fetch it live from the API and assign it themselves (see
		// purchase_ship.go, register_player.go, get_player.go). This poller
		// makes no such API call, so every 60s tick used to Set the gauge to a
		// hardcoded 0, stomping the accurate value RecordTransaction had just
		// written from the ledger's authoritative running balance - producing
		// the observed 0<->balance oscillation. RecordTransaction (below) is
		// this gauge's single writer; it fires on every ledger entry, far more
		// often than this poll ever could, and never sees a phantom zero.

		// Update revenue metrics by category
		for category, amount := range plResponse.RevenueBreakdown {
			c.totalRevenue.WithLabelValues(playerIDStr, agentSymbol, category).Set(float64(amount))
		}

		// Update expense metrics by category
		for category, amount := range plResponse.ExpenseBreakdown {
			c.totalExpenses.WithLabelValues(playerIDStr, agentSymbol, category).Set(float64(amount))
		}

		// Update net profit
		c.netProfit.WithLabelValues(playerIDStr).Set(float64(plResponse.NetProfit))
	}
}

// RecordTransaction records a transaction event
func (c *FinancialMetricsCollector) RecordTransaction(
	playerID int,
	agentSymbol string,
	transactionType string,
	category string,
	amount int,
	creditsBalance int,
	operationType string,
) {
	playerIDStr := strconv.Itoa(playerID)

	// Update credits balance
	c.creditsBalance.WithLabelValues(playerIDStr, agentSymbol).Set(float64(creditsBalance))

	// Increment transaction counter
	c.transactionsTotal.WithLabelValues(playerIDStr, transactionType, category).Inc()

	// Record transaction amount (use absolute value for histogram)
	absAmount := amount
	if absAmount < 0 {
		absAmount = -absAmount
	}
	c.transactionAmount.WithLabelValues(playerIDStr, transactionType, category).Observe(float64(absAmount))

	// Fan the signed amount into the sign-split ledger-flow counters (sp-miqt).
	c.recordLedgerFlow(operationType, category, playerIDStr, amount)
}

// recordLedgerFlow increments exactly one of the monotonic ledger-flow counters
// by the magnitude of a signed amount (sp-miqt): positive amounts are revenue,
// negative amounts are cost, zero is neither. Split by sign because Prometheus
// counters must be non-negative; PromQL nets the two sides back together.
func (c *FinancialMetricsCollector) recordLedgerFlow(operationType, category, playerIDStr string, amount int) {
	if amount > 0 {
		c.ledgerRevenueTotal.WithLabelValues(operationType, category, playerIDStr).Add(float64(amount))
		return
	}
	if amount < 0 {
		c.ledgerCostTotal.WithLabelValues(operationType, category, playerIDStr).Add(float64(-amount))
	}
}

// RecordTrade records trade profitability metrics
func (c *FinancialMetricsCollector) RecordTrade(
	playerID int,
	goodSymbol string,
	buyPrice int,
	sellPrice int,
	quantity int,
) {
	if buyPrice <= 0 || sellPrice <= 0 || quantity <= 0 {
		return // Invalid data
	}

	playerIDStr := strconv.Itoa(playerID)

	// Calculate profit per unit
	profitPerUnit := sellPrice - buyPrice
	c.tradeProfitPerUnit.WithLabelValues(playerIDStr, goodSymbol).Observe(float64(profitPerUnit))

	// Calculate margin percentage
	marginPercent := float64(profitPerUnit) / float64(buyPrice) * 100
	c.tradeMarginPercent.WithLabelValues(playerIDStr, goodSymbol).Observe(marginPercent)
}
