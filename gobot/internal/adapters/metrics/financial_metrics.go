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
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
)

// FinancialMetricsCollector handles all financial metrics (credits, transactions, P&L)
type FinancialMetricsCollector struct {
	// Dependencies
	mediator common.Mediator

	// Balance metrics
	creditsBalance *prometheus.GaugeVec

	// Transaction metrics
	transactionsTotal  *prometheus.CounterVec
	transactionAmount  *prometheus.HistogramVec

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
func NewFinancialMetricsCollector(mediator common.Mediator) *FinancialMetricsCollector {
	return &FinancialMetricsCollector{
		mediator: mediator,

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

		// Total revenue by category
		totalRevenue: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "total_revenue",
				Help:      "Total revenue by category",
			},
			[]string{"player_id", "category"},
		),

		// Total expenses by category
		totalExpenses: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "total_expenses",
				Help:      "Total expenses by category",
			},
			[]string{"player_id", "category"},
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

	// TODO: Support multiple players dynamically
	// For now, use player ID 11 (COOPER) - the typical player in the database
	// Future enhancement: Query database for all active players or make configurable
	playerID := 11

	// Execute GetProfitLossQuery for all-time P&L
	// Use epoch start and far future to capture all transactions
	query := &ledgerQueries.GetProfitLossQuery{
		PlayerID:  playerID,
		StartDate: time.Unix(0, 0),           // Epoch start (1970-01-01)
		EndDate:   time.Now().Add(24 * time.Hour), // Tomorrow to ensure we get everything
	}

	response, err := c.mediator.Send(context.Background(), query)
	if err != nil {
		log.Printf("Failed to fetch profit/loss for player %d: %v", playerID, err)
		return
	}

	plResponse, ok := response.(*ledgerQueries.GetProfitLossResponse)
	if !ok {
		log.Printf("Unexpected response type for P&L query: %T", response)
		return
	}

	playerIDStr := strconv.Itoa(playerID)

	// Fetch current player data (including real-time credits and agent symbol) from API
	getPlayerQuery := &playerQueries.GetPlayerQuery{
		PlayerID: &playerID,
	}

	playerResp, err := c.mediator.Send(context.Background(), getPlayerQuery)
	if err == nil {
		if playerData, ok := playerResp.(*playerQueries.GetPlayerResponse); ok && playerData.Player != nil {
			// Update credits balance with actual agent symbol and current credits
			c.creditsBalance.WithLabelValues(playerIDStr, playerData.Player.AgentSymbol).Set(float64(playerData.Player.Credits))
		} else {
			log.Printf("Unexpected response type for GetPlayer query: %T", playerResp)
		}
	} else {
		log.Printf("Failed to fetch player %d for balance update: %v", playerID, err)
	}

	// Update revenue metrics by category
	for category, amount := range plResponse.RevenueBreakdown {
		c.totalRevenue.WithLabelValues(playerIDStr, category).Set(float64(amount))
	}

	// Update expense metrics by category
	for category, amount := range plResponse.ExpenseBreakdown {
		c.totalExpenses.WithLabelValues(playerIDStr, category).Set(float64(amount))
	}

	// Update net profit
	c.netProfit.WithLabelValues(playerIDStr).Set(float64(plResponse.NetProfit))
}

// RecordTransaction records a transaction event
func (c *FinancialMetricsCollector) RecordTransaction(
	playerID int,
	agentSymbol string,
	transactionType string,
	category string,
	amount int,
	creditsBalance int,
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
