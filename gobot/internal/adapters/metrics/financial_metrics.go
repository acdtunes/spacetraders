package metrics

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FinancialMetricsCollector handles all financial metrics (credits, transactions, P&L)
type FinancialMetricsCollector struct {
	// Dependencies
	playerRepo    player.PlayerRepository         // For fetching player data
	getContainers func() map[string]ContainerInfo // Function to get current containers

	// Balance metrics
	creditsBalance *prometheus.GaugeVec

	// Transaction metrics
	transactionsTotal *prometheus.CounterVec
	transactionAmount *prometheus.HistogramVec

	// Ledger-flow counters: monotonic signed-amount sums split by
	// sign so PromQL rate() can drive the cr/hr financial panels. Labeled by
	// operation_type (contract/tour/arbitrage/...) + category + player_id.
	ledgerRevenueTotal *prometheus.CounterVec // += amount when amount > 0
	ledgerCostTotal    *prometheus.CounterVec // += -amount when amount < 0

	// categoryTotalsMu guards revenueByCategory/expenseByCategory, the in-process
	// running totals updateProfitLoss reads instead of re-deriving all-time sums
	// from the database every tick (sp-<pending>): they're updated in lockstep with
	// ledgerRevenueTotal/ledgerCostTotal above, at zero extra DB cost, and reset on
	// restart exactly like those counters already do.
	categoryTotalsMu  sync.Mutex
	revenueByCategory map[string]map[string]int64 // player_id -> category -> total
	expenseByCategory map[string]map[string]int64

	// P&L metrics
	totalRevenue  *prometheus.GaugeVec
	totalExpenses *prometheus.GaugeVec
	netProfit     *prometheus.GaugeVec

	// Trade profitability metrics (optional)
	tradeProfitPerUnit *prometheus.HistogramVec
	tradeMarginPercent *prometheus.HistogramVec

	// Lifecycle scaffolding (ctx/cancelFunc/wg + Start context + Stop) is shared
	// via the embedded pollingCollector.
	pollingCollector
}

// NewFinancialMetricsCollector creates a new financial metrics collector
func NewFinancialMetricsCollector(
	playerRepo player.PlayerRepository,
	getContainers func() map[string]ContainerInfo,
) *FinancialMetricsCollector {
	return &FinancialMetricsCollector{
		playerRepo:        playerRepo,
		getContainers:     getContainers,
		revenueByCategory: make(map[string]map[string]int64),
		expenseByCategory: make(map[string]map[string]int64),

		// Current credits balance gauge
		creditsBalance: newGaugeVec(
			"player_credits_balance",
			"Current credits balance for each player",
			"player_id",
			"agent",
		),

		// Transaction count by type. category is dropped: it is a
		// deterministic f(type) relabel, so it duplicated `type` here for no
		// added signal. The Operating-vs-Net split lives on ledger_*_total below.
		transactionsTotal: newCounterVec(
			"transactions_total",
			"Total number of transactions by type",
			"player_id",
			"type",
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
			// category dropped: redundant f(type) relabel.
			[]string{"player_id", "type"},
		),

		// Ledger revenue (positive inflow) running total by operation/category
		ledgerRevenueTotal: newCounterVec(
			"ledger_revenue_total",
			"Cumulative positive ledger amount (revenue) by operation_type and category",
			"operation_type",
			"category",
			"player_id",
		),

		// Ledger cost (negative outflow magnitude) running total by operation/category
		ledgerCostTotal: newCounterVec(
			"ledger_cost_total",
			"Cumulative negative ledger amount magnitude (cost) by operation_type and category",
			"operation_type",
			"category",
			"player_id",
		),

		// Total revenue by category
		totalRevenue: newGaugeVec("total_revenue", "Total revenue by category", "player_id", "agent", "category"),

		// Total expenses by category
		totalExpenses: newGaugeVec("total_expenses", "Total expenses by category", "player_id", "agent", "category"),

		// Net profit
		netProfit: newGaugeVec("net_profit", "Net profit (revenue - expenses)", "player_id"),

		// Profit per unit (for trades)
		tradeProfitPerUnit: newHistogramVec(
			"trade_profit_per_unit",
			"Profit per unit from trades",
			[]float64{1, 5, 10, 50, 100, 500, 1000},
			"player_id",
			"good_symbol",
		),

		// Trade margin percentage
		tradeMarginPercent: newHistogramVec(
			"trade_margin_percent",
			"Trade margin percentage ((sell-buy)/buy * 100)",
			[]float64{5, 10, 25, 50, 75, 100, 150, 200},
			"player_id",
			"good_symbol",
		),
	}
}

// Register registers all financial metrics with the Prometheus registry
func (c *FinancialMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
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
	)
}

// Start begins the P&L polling goroutine
func (c *FinancialMetricsCollector) Start(ctx context.Context) {
	c.startContext(ctx)

	// Start P&L polling (every 60 seconds), with an immediate initial poll.
	c.startPolling(60*time.Second, true, c.updateProfitLoss)
}

// updateProfitLoss republishes the in-process running totals (kept current by
// recordLedgerFlow on every transaction) into the P&L gauges. No database read:
// see the categoryTotalsMu field comment for why.
func (c *FinancialMetricsCollector) updateProfitLoss() {
	containers := c.getContainers()
	if len(containers) == 0 {
		// No active containers, skip P&L metrics
		return
	}

	playerIDs := make(map[int]bool)
	for _, containerInfo := range containers {
		playerIDs[containerInfo.PlayerID()] = true
	}

	for playerID := range playerIDs {
		playerIDStr := strconv.Itoa(playerID)

		// Fetch player data from database to get agent symbol
		playerEntity, err := c.playerRepo.FindByID(context.Background(), shared.MustNewPlayerID(playerID))
		if err != nil {
			log.Printf("Failed to fetch player %d from database: %v", playerID, err)
			continue // Skip this player if we can't fetch their data
		}

		agentSymbol := playerEntity.AgentSymbol

		// player_credits_balance is intentionally NOT written here: playerRepo.FindByID
		// never populates Credits from the DB (see GormPlayerRepository.modelToPlayer),
		// so this poller has no accurate balance to set. RecordTransaction (below) is
		// this gauge's sole writer, sourced from the ledger's authoritative running
		// balance.

		c.categoryTotalsMu.Lock()
		revenue := c.revenueByCategory[playerIDStr]
		expense := c.expenseByCategory[playerIDStr]
		var totalRevenue, totalExpense int64
		for category, amount := range revenue {
			c.totalRevenue.WithLabelValues(playerIDStr, agentSymbol, category).Set(float64(amount))
			totalRevenue += amount
		}
		for category, amount := range expense {
			c.totalExpenses.WithLabelValues(playerIDStr, agentSymbol, category).Set(float64(amount))
			totalExpense += amount
		}
		c.categoryTotalsMu.Unlock()

		c.netProfit.WithLabelValues(playerIDStr).Set(float64(totalRevenue - totalExpense))
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

	// Increment transaction counter. category is intentionally NOT a label here:
	// it is a deterministic f(type), so `type` already carries it.
	c.transactionsTotal.WithLabelValues(playerIDStr, transactionType).Inc()

	// Record transaction amount (use absolute value for histogram)
	absAmount := amount
	if absAmount < 0 {
		absAmount = -absAmount
	}
	c.transactionAmount.WithLabelValues(playerIDStr, transactionType).Observe(float64(absAmount))

	// Fan the signed amount into the sign-split ledger-flow counters,
	// which DO keep category for the Operating-vs-Net capex/opex split.
	c.recordLedgerFlow(operationType, category, playerIDStr, amount)
}

// recordLedgerFlow increments exactly one of the monotonic ledger-flow counters
// by the magnitude of a signed amount: positive amounts are revenue,
// negative amounts are cost, zero is neither. Split by sign because Prometheus
// counters must be non-negative; PromQL nets the two sides back together.
func (c *FinancialMetricsCollector) recordLedgerFlow(operationType, category, playerIDStr string, amount int) {
	if amount > 0 {
		c.ledgerRevenueTotal.WithLabelValues(operationType, category, playerIDStr).Add(float64(amount))
		c.addCategoryTotal(c.revenueByCategory, playerIDStr, category, int64(amount))
		return
	}
	if amount < 0 {
		c.ledgerCostTotal.WithLabelValues(operationType, category, playerIDStr).Add(float64(-amount))
		c.addCategoryTotal(c.expenseByCategory, playerIDStr, category, int64(-amount))
	}
}

// addCategoryTotal increments one player+category cell of a running-total map,
// initializing the player's row on first use.
func (c *FinancialMetricsCollector) addCategoryTotal(totals map[string]map[string]int64, playerIDStr, category string, delta int64) {
	c.categoryTotalsMu.Lock()
	defer c.categoryTotalsMu.Unlock()
	if totals[playerIDStr] == nil {
		totals[playerIDStr] = make(map[string]int64)
	}
	totals[playerIDStr][category] += delta
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

// globalFinancialCollector is the singleton financial metrics collector
// Set by SetGlobalFinancialCollector() when metrics are enabled
var globalFinancialCollector FinancialMetricsRecorder

// FinancialMetricsRecorder defines the interface for recording financial metrics
type FinancialMetricsRecorder interface {
	RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int, operationType string)
	RecordTrade(playerID int, goodSymbol string, buyPrice int, sellPrice int, quantity int)
}

// SetGlobalFinancialCollector sets the global financial metrics collector
func SetGlobalFinancialCollector(collector FinancialMetricsRecorder) {
	globalFinancialCollector = collector
}

// RecordTransaction records a transaction event globally. operationType
// (contract/tour/arbitrage/...) drives the ledger-flow counters that
// back the cr/hr financial panels; pass "" when unknown.
func RecordTransaction(playerID int, agentSymbol string, transactionType string, category string, amount int, creditsBalance int, operationType string) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTransaction(playerID, agentSymbol, transactionType, category, amount, creditsBalance, operationType)
	}
}

// RecordTradeMetrics publishes a REALIZED trade's per-unit economics through the global financial
// collector.
//
// The wrapper existed for every other collector in this package and not for this one, which is a
// large part of why RecordTrade was never called: a caller in the application layer has no handle on
// the collector, only on these package-level functions. Declared, registered, correctly implemented,
// and unreachable.
//
// Nil-safe and best-effort like its siblings: metrics are pure observation and a recording miss must
// never alter a trade decision (RULINGS #4).
func RecordTradeMetrics(playerID int, goodSymbol string, buyPrice, sellPrice, quantity int) {
	if globalFinancialCollector != nil {
		globalFinancialCollector.RecordTrade(playerID, goodSymbol, buyPrice, sellPrice, quantity)
	}
}
