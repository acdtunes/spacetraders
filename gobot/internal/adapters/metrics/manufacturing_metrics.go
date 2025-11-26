package metrics

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// ManufacturingMetricsCollector handles all manufacturing pipeline metrics
type ManufacturingMetricsCollector struct {
	// Dependencies
	db *gorm.DB

	// Pipeline Health Metrics (5 metrics)
	pipelineRunningTotal    *prometheus.GaugeVec
	pipelineQueueDepth      *prometheus.GaugeVec
	pipelineCompletedTotal  *prometheus.CounterVec
	pipelineDurationSeconds *prometheus.HistogramVec
	pipelineProfitCredits   *prometheus.HistogramVec

	// Task Execution Metrics (6 metrics)
	tasksPendingTotal   *prometheus.GaugeVec
	tasksReadyTotal     *prometheus.GaugeVec
	tasksExecutingTotal *prometheus.GaugeVec
	tasksCompletedTotal *prometheus.CounterVec
	taskDurationSeconds *prometheus.HistogramVec
	taskRetryTotal      *prometheus.CounterVec

	// Factory Supply Metrics (5 metrics)
	factorySupplyLevel       *prometheus.GaugeVec
	factoryInputsDelivered   *prometheus.GaugeVec
	factoryReadyTotal        *prometheus.GaugeVec
	factoryCyclesTotal       *prometheus.CounterVec
	supplyTransitionsTotal   *prometheus.CounterVec

	// Ship Utilization Metrics (4 metrics)
	shipsAssignedTotal      *prometheus.GaugeVec
	shipsIdleTotal          *prometheus.GaugeVec
	shipTaskDurationSeconds *prometheus.HistogramVec
	shipUtilizationPercent  *prometheus.GaugeVec

	// Economic Metrics (4 metrics)
	costTotal     *prometheus.CounterVec
	revenueTotal  *prometheus.CounterVec
	profitRate    *prometheus.GaugeVec
	marginPercent *prometheus.GaugeVec

	// Lifecycle
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup

	// Configuration
	pollInterval time.Duration
}

// NewManufacturingMetricsCollector creates a new manufacturing metrics collector
func NewManufacturingMetricsCollector(db *gorm.DB) *ManufacturingMetricsCollector {
	return &ManufacturingMetricsCollector{
		db: db,

		// Pipeline Health Metrics
		pipelineRunningTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_pipeline_running_total",
				Help:      "Number of currently running manufacturing pipelines",
			},
			[]string{"player_id", "product_good"},
		),

		pipelineQueueDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_pipeline_queue_depth",
				Help:      "Total pipelines in planning or executing state",
			},
			[]string{"player_id"},
		),

		pipelineCompletedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_pipeline_completed_total",
				Help:      "Total completed manufacturing pipelines by status",
			},
			[]string{"player_id", "product_good", "status"},
		),

		pipelineDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_pipeline_duration_seconds",
				Help:      "Pipeline execution duration in seconds",
				Buckets:   []float64{60, 120, 300, 600, 900, 1200, 1800, 3600},
			},
			[]string{"player_id", "product_good"},
		),

		pipelineProfitCredits: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_pipeline_profit_credits",
				Help:      "Profit per pipeline in credits",
				Buckets:   []float64{1000, 5000, 10000, 50000, 100000, 500000},
			},
			[]string{"player_id", "product_good"},
		),

		// Task Execution Metrics
		tasksPendingTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_tasks_pending_total",
				Help:      "Number of tasks waiting for dependencies",
			},
			[]string{"player_id", "task_type"},
		),

		tasksReadyTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_tasks_ready_total",
				Help:      "Number of tasks ready to execute",
			},
			[]string{"player_id", "task_type"},
		),

		tasksExecutingTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_tasks_executing_total",
				Help:      "Number of tasks currently executing",
			},
			[]string{"player_id", "task_type"},
		),

		tasksCompletedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_tasks_completed_total",
				Help:      "Total completed tasks by type and status",
			},
			[]string{"player_id", "task_type", "status"},
		),

		taskDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_task_duration_seconds",
				Help:      "Task execution duration in seconds",
				Buckets:   []float64{10, 30, 60, 120, 300, 600},
			},
			[]string{"player_id", "task_type"},
		),

		taskRetryTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_task_retry_total",
				Help:      "Total task retries by type",
			},
			[]string{"player_id", "task_type"},
		),

		// Factory Supply Metrics
		factorySupplyLevel: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_factory_supply_level",
				Help:      "Current supply level at factory (1=SCARCE, 2=LIMITED, 3=MODERATE, 4=HIGH, 5=ABUNDANT)",
			},
			[]string{"player_id", "factory_symbol", "output_good"},
		),

		factoryInputsDelivered: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_factory_inputs_delivered",
				Help:      "Input delivery progress (0-1)",
			},
			[]string{"player_id", "factory_symbol", "output_good"},
		),

		factoryReadyTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_factory_ready_total",
				Help:      "Number of factories ready for collection",
			},
			[]string{"player_id"},
		),

		factoryCyclesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_factory_cycles_total",
				Help:      "Total production cycles completed",
			},
			[]string{"player_id", "factory_symbol", "output_good"},
		),

		supplyTransitionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_supply_transitions_total",
				Help:      "Supply level transitions by good",
			},
			[]string{"player_id", "good_symbol", "from_level", "to_level"},
		),

		// Ship Utilization Metrics
		shipsAssignedTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_ships_assigned_total",
				Help:      "Number of ships assigned to manufacturing tasks",
			},
			[]string{"player_id"},
		),

		shipsIdleTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_ships_idle_total",
				Help:      "Number of ships available for manufacturing",
			},
			[]string{"player_id"},
		),

		shipTaskDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_ship_task_duration_seconds",
				Help:      "Ship task execution duration in seconds",
				Buckets:   []float64{10, 30, 60, 120, 300, 600},
			},
			[]string{"player_id", "task_type"},
		),

		shipUtilizationPercent: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_ship_utilization_percent",
				Help:      "Ship utilization percentage (assigned/total * 100)",
			},
			[]string{"player_id"},
		),

		// Economic Metrics
		costTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_cost_total",
				Help:      "Total manufacturing costs by type",
			},
			[]string{"player_id", "cost_type"},
		),

		revenueTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_revenue_total",
				Help:      "Total manufacturing revenue",
			},
			[]string{"player_id"},
		),

		profitRate: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_profit_rate",
				Help:      "Manufacturing profit rate (credits/hour)",
			},
			[]string{"player_id"},
		),

		marginPercent: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "manufacturing_margin_percent",
				Help:      "Profit margin percentage by product",
			},
			[]string{"player_id", "product_good"},
		),

		// Configuration
		pollInterval: 30 * time.Second,
	}
}

// Register registers all manufacturing metrics with the Prometheus registry
func (c *ManufacturingMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		// Pipeline Health
		c.pipelineRunningTotal,
		c.pipelineQueueDepth,
		c.pipelineCompletedTotal,
		c.pipelineDurationSeconds,
		c.pipelineProfitCredits,
		// Task Execution
		c.tasksPendingTotal,
		c.tasksReadyTotal,
		c.tasksExecutingTotal,
		c.tasksCompletedTotal,
		c.taskDurationSeconds,
		c.taskRetryTotal,
		// Factory Supply
		c.factorySupplyLevel,
		c.factoryInputsDelivered,
		c.factoryReadyTotal,
		c.factoryCyclesTotal,
		c.supplyTransitionsTotal,
		// Ship Utilization
		c.shipsAssignedTotal,
		c.shipsIdleTotal,
		c.shipTaskDurationSeconds,
		c.shipUtilizationPercent,
		// Economic
		c.costTotal,
		c.revenueTotal,
		c.profitRate,
		c.marginPercent,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// Start begins the polling goroutine for aggregate metrics
func (c *ManufacturingMetricsCollector) Start(ctx context.Context) {
	c.ctx, c.cancelFunc = context.WithCancel(ctx)

	// Start polling (every 30 seconds)
	c.wg.Add(1)
	go c.pollMetrics(c.pollInterval)
}

// Stop gracefully stops the manufacturing metrics collector
func (c *ManufacturingMetricsCollector) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	c.wg.Wait()
}

// pollMetrics polls manufacturing data periodically
func (c *ManufacturingMetricsCollector) pollMetrics(interval time.Duration) {
	defer c.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do initial poll immediately
	c.updateAllMetrics()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateAllMetrics()
		}
	}
}

// updateAllMetrics updates all polling-based metrics
func (c *ManufacturingMetricsCollector) updateAllMetrics() {
	if c.db == nil {
		return
	}

	// Reset all gauges first to clear stale data
	c.pipelineRunningTotal.Reset()
	c.pipelineQueueDepth.Reset()
	c.tasksPendingTotal.Reset()
	c.tasksReadyTotal.Reset()
	c.tasksExecutingTotal.Reset()
	c.factorySupplyLevel.Reset()
	c.factoryInputsDelivered.Reset()
	c.factoryReadyTotal.Reset()
	c.shipsAssignedTotal.Reset()
	c.shipsIdleTotal.Reset()
	c.shipUtilizationPercent.Reset()
	c.profitRate.Reset()
	c.marginPercent.Reset()

	// Get list of active players
	players := c.getActivePlayers()

	for _, playerID := range players {
		c.updatePipelineMetrics(playerID)
		c.updateTaskMetrics(playerID)
		c.updateFactoryMetrics(playerID)
		c.updateShipMetrics(playerID)
		c.updateEconomicMetrics(playerID)
	}
}

// getActivePlayers retrieves list of players with manufacturing data
func (c *ManufacturingMetricsCollector) getActivePlayers() []int {
	var playerIDs []int

	// Query distinct player_ids from manufacturing_pipelines
	err := c.db.Raw(`
		SELECT DISTINCT player_id FROM manufacturing_pipelines
		UNION
		SELECT DISTINCT player_id FROM manufacturing_tasks
	`).Scan(&playerIDs).Error

	if err != nil {
		log.Printf("Failed to get active manufacturing players: %v", err)
		return nil
	}

	return playerIDs
}

// updatePipelineMetrics updates pipeline health metrics
func (c *ManufacturingMetricsCollector) updatePipelineMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Pipeline status counts by product
	var pipelineCounts []struct {
		ProductGood string
		Status      string
		Count       int64
	}

	err := c.db.Raw(`
		SELECT product_good, status, COUNT(*) as count
		FROM manufacturing_pipelines
		WHERE player_id = ?
		GROUP BY product_good, status
	`, playerID).Scan(&pipelineCounts).Error

	if err != nil {
		log.Printf("Failed to get pipeline counts: %v", err)
		return
	}

	var totalQueueDepth int64
	for _, record := range pipelineCounts {
		if record.Status == "EXECUTING" {
			c.pipelineRunningTotal.WithLabelValues(playerIDStr, record.ProductGood).Set(float64(record.Count))
		}
		if record.Status == "PLANNING" || record.Status == "EXECUTING" {
			totalQueueDepth += record.Count
		}
	}

	c.pipelineQueueDepth.WithLabelValues(playerIDStr).Set(float64(totalQueueDepth))
}

// updateTaskMetrics updates task execution metrics
func (c *ManufacturingMetricsCollector) updateTaskMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Task status counts by type
	var taskCounts []struct {
		TaskType string
		Status   string
		Count    int64
	}

	err := c.db.Raw(`
		SELECT task_type, status, COUNT(*) as count
		FROM manufacturing_tasks
		WHERE player_id = ?
		GROUP BY task_type, status
	`, playerID).Scan(&taskCounts).Error

	if err != nil {
		log.Printf("Failed to get task counts: %v", err)
		return
	}

	for _, record := range taskCounts {
		switch record.Status {
		case "PENDING":
			c.tasksPendingTotal.WithLabelValues(playerIDStr, record.TaskType).Set(float64(record.Count))
		case "READY":
			c.tasksReadyTotal.WithLabelValues(playerIDStr, record.TaskType).Set(float64(record.Count))
		case "ASSIGNED", "EXECUTING":
			c.tasksExecutingTotal.WithLabelValues(playerIDStr, record.TaskType).Add(float64(record.Count))
		}
	}
}

// updateFactoryMetrics updates factory supply metrics
func (c *ManufacturingMetricsCollector) updateFactoryMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Factory states
	var factoryStates []struct {
		FactorySymbol      string
		OutputGood         string
		CurrentSupply      string
		AllInputsDelivered bool
		ReadyForCollection bool
	}

	err := c.db.Raw(`
		SELECT factory_symbol, output_good, current_supply,
		       all_inputs_delivered, ready_for_collection
		FROM manufacturing_factory_states
		WHERE player_id = ?
	`, playerID).Scan(&factoryStates).Error

	if err != nil {
		log.Printf("Failed to get factory states: %v", err)
		return
	}

	var readyCount float64
	for _, state := range factoryStates {
		// Convert supply level to numeric
		supplyValue := c.supplyLevelToValue(state.CurrentSupply)
		c.factorySupplyLevel.WithLabelValues(playerIDStr, state.FactorySymbol, state.OutputGood).Set(supplyValue)

		// Input delivery progress
		var progress float64
		if state.AllInputsDelivered {
			progress = 1.0
		}
		c.factoryInputsDelivered.WithLabelValues(playerIDStr, state.FactorySymbol, state.OutputGood).Set(progress)

		if state.ReadyForCollection {
			readyCount++
		}
	}

	c.factoryReadyTotal.WithLabelValues(playerIDStr).Set(readyCount)
}

// supplyLevelToValue converts supply level string to numeric value
func (c *ManufacturingMetricsCollector) supplyLevelToValue(level string) float64 {
	switch level {
	case "SCARCE":
		return 1
	case "LIMITED":
		return 2
	case "MODERATE":
		return 3
	case "HIGH":
		return 4
	case "ABUNDANT":
		return 5
	default:
		return 0
	}
}

// updateShipMetrics updates ship utilization metrics
func (c *ManufacturingMetricsCollector) updateShipMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Count ships assigned to manufacturing tasks
	var assignedCount int64
	err := c.db.Raw(`
		SELECT COUNT(DISTINCT assigned_ship)
		FROM manufacturing_tasks
		WHERE player_id = ?
		  AND status IN ('ASSIGNED', 'EXECUTING')
		  AND assigned_ship IS NOT NULL
		  AND assigned_ship != ''
	`, playerID).Scan(&assignedCount).Error

	if err != nil {
		log.Printf("Failed to get assigned ship count: %v", err)
		return
	}

	c.shipsAssignedTotal.WithLabelValues(playerIDStr).Set(float64(assignedCount))

	// Note: Ships are fetched from API, not cached in database.
	// Ship utilization metrics require API access which isn't available here.
	// The shipsIdleTotal and shipUtilizationPercent metrics are not populated.
}

// updateEconomicMetrics updates economic metrics
func (c *ManufacturingMetricsCollector) updateEconomicMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Get hourly profit rate from completed pipelines in last hour
	var hourlyStats struct {
		TotalCost    int64
		TotalRevenue int64
		PipelineCount int64
	}

	err := c.db.Raw(`
		SELECT
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_revenue), 0) as total_revenue,
			COUNT(*) as pipeline_count
		FROM manufacturing_pipelines
		WHERE player_id = ?
		  AND status = 'COMPLETED'
		  AND completed_at > NOW() - INTERVAL '1 hour'
	`, playerID).Scan(&hourlyStats).Error

	if err != nil {
		log.Printf("Failed to get hourly economic stats: %v", err)
		return
	}

	// Profit rate (credits per hour) - this is already for the last hour
	profitRate := float64(hourlyStats.TotalRevenue - hourlyStats.TotalCost)
	c.profitRate.WithLabelValues(playerIDStr).Set(profitRate)

	// Margin percentage by product
	var productMargins []struct {
		ProductGood   string
		TotalCost     int64
		TotalRevenue  int64
	}

	err = c.db.Raw(`
		SELECT
			product_good,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_revenue), 0) as total_revenue
		FROM manufacturing_pipelines
		WHERE player_id = ?
		  AND status = 'COMPLETED'
		GROUP BY product_good
	`, playerID).Scan(&productMargins).Error

	if err != nil {
		log.Printf("Failed to get product margins: %v", err)
		return
	}

	for _, margin := range productMargins {
		if margin.TotalRevenue > 0 {
			marginPct := float64(margin.TotalRevenue-margin.TotalCost) / float64(margin.TotalRevenue) * 100
			c.marginPercent.WithLabelValues(playerIDStr, margin.ProductGood).Set(marginPct)
		}
	}
}

// RecordPipelineCompletion records a pipeline completion event
func (c *ManufacturingMetricsCollector) RecordPipelineCompletion(playerID int, productGood, status string, duration time.Duration, profit int) {
	playerIDStr := strconv.Itoa(playerID)

	c.pipelineCompletedTotal.WithLabelValues(playerIDStr, productGood, status).Inc()
	c.pipelineDurationSeconds.WithLabelValues(playerIDStr, productGood).Observe(duration.Seconds())
	c.pipelineProfitCredits.WithLabelValues(playerIDStr, productGood).Observe(float64(profit))
}

// RecordTaskCompletion records a task completion event
func (c *ManufacturingMetricsCollector) RecordTaskCompletion(playerID int, taskType, status string, duration time.Duration) {
	playerIDStr := strconv.Itoa(playerID)

	c.tasksCompletedTotal.WithLabelValues(playerIDStr, taskType, status).Inc()
	c.taskDurationSeconds.WithLabelValues(playerIDStr, taskType).Observe(duration.Seconds())
	c.shipTaskDurationSeconds.WithLabelValues(playerIDStr, taskType).Observe(duration.Seconds())
}

// RecordTaskRetry records a task retry event
func (c *ManufacturingMetricsCollector) RecordTaskRetry(playerID int, taskType string) {
	playerIDStr := strconv.Itoa(playerID)
	c.taskRetryTotal.WithLabelValues(playerIDStr, taskType).Inc()
}

// RecordSupplyTransition records a supply level change event
func (c *ManufacturingMetricsCollector) RecordSupplyTransition(playerID int, good, fromLevel, toLevel string) {
	playerIDStr := strconv.Itoa(playerID)
	c.supplyTransitionsTotal.WithLabelValues(playerIDStr, good, fromLevel, toLevel).Inc()
}

// RecordFactoryCycle records a factory production cycle completion
func (c *ManufacturingMetricsCollector) RecordFactoryCycle(playerID int, factorySymbol, outputGood string) {
	playerIDStr := strconv.Itoa(playerID)
	c.factoryCyclesTotal.WithLabelValues(playerIDStr, factorySymbol, outputGood).Inc()
}

// RecordCost records a manufacturing cost
func (c *ManufacturingMetricsCollector) RecordCost(playerID int, costType string, amount int) {
	playerIDStr := strconv.Itoa(playerID)
	c.costTotal.WithLabelValues(playerIDStr, costType).Add(float64(amount))
}

// RecordRevenue records manufacturing revenue
func (c *ManufacturingMetricsCollector) RecordRevenue(playerID int, amount int) {
	playerIDStr := strconv.Itoa(playerID)
	c.revenueTotal.WithLabelValues(playerIDStr).Add(float64(amount))
}
