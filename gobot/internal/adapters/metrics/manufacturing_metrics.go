package metrics

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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
	factorySupplyLevel     *prometheus.GaugeVec
	factoryInputsDelivered *prometheus.GaugeVec
	factoryReadyTotal      *prometheus.GaugeVec
	factoryCyclesTotal     *prometheus.CounterVec
	supplyTransitionsTotal *prometheus.CounterVec

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

	// Starvation Metrics (3 metrics) - Added to detect task type starvation
	taskStarvationMinutes         *prometheus.GaugeVec
	taskAssignmentsTotal          *prometheus.CounterVec
	taskTypeReservationSkipsTotal *prometheus.CounterVec

	// Lifecycle scaffolding (ctx/cancelFunc/wg + Start context + Stop) is shared
	// via the embedded pollingCollector.
	pollingCollector

	// Configuration
	pollInterval time.Duration
}

// NewManufacturingMetricsCollector creates a new manufacturing metrics collector
func NewManufacturingMetricsCollector(db *gorm.DB) *ManufacturingMetricsCollector {
	return &ManufacturingMetricsCollector{
		db: db,

		// Pipeline Health Metrics
		pipelineRunningTotal: newGaugeVec(
			"manufacturing_pipeline_running_total",
			"Number of currently running manufacturing pipelines",
			"player_id",
			"product_good",
		),

		pipelineQueueDepth: newGaugeVec(
			"manufacturing_pipeline_queue_depth",
			"Total pipelines in planning or executing state",
			"player_id",
		),

		pipelineCompletedTotal: newCounterVec(
			"manufacturing_pipeline_completed_total",
			"Total completed manufacturing pipelines by status",
			"player_id",
			"product_good",
			"status",
		),

		pipelineDurationSeconds: newHistogramVec(
			"manufacturing_pipeline_duration_seconds",
			"Pipeline execution duration in seconds",
			[]float64{60, 120, 300, 600, 900, 1200, 1800, 3600},
			"player_id",
			"product_good",
		),

		pipelineProfitCredits: newHistogramVec(
			"manufacturing_pipeline_profit_credits",
			"Profit per pipeline in credits",
			[]float64{1000, 5000, 10000, 50000, 100000, 500000},
			"player_id",
			"product_good",
		),

		// Task Execution Metrics
		tasksPendingTotal: newGaugeVec(
			"manufacturing_tasks_pending_total",
			"Number of tasks waiting for dependencies",
			"player_id",
			"task_type",
		),

		tasksReadyTotal: newGaugeVec(
			"manufacturing_tasks_ready_total",
			"Number of tasks ready to execute",
			"player_id",
			"task_type",
		),

		tasksExecutingTotal: newGaugeVec(
			"manufacturing_tasks_executing_total",
			"Number of tasks currently executing",
			"player_id",
			"task_type",
		),

		tasksCompletedTotal: newCounterVec(
			"manufacturing_tasks_completed_total",
			"Total completed tasks by type and status",
			"player_id",
			"task_type",
			"status",
		),

		taskDurationSeconds: newHistogramVec(
			"manufacturing_task_duration_seconds",
			"Task execution duration in seconds",
			[]float64{10, 30, 60, 120, 300, 600},
			"player_id",
			"task_type",
		),

		taskRetryTotal: newCounterVec(
			"manufacturing_task_retry_total",
			"Total task retries by type",
			"player_id",
			"task_type",
		),

		// Factory Supply Metrics
		factorySupplyLevel: newGaugeVec(
			"manufacturing_factory_supply_level",
			"Current supply level at factory (1=SCARCE, 2=LIMITED, 3=MODERATE, 4=HIGH, 5=ABUNDANT)",
			"player_id",
			"factory_symbol",
			"output_good",
		),

		factoryInputsDelivered: newGaugeVec(
			"manufacturing_factory_inputs_delivered",
			"Input delivery progress (0-1)",
			"player_id",
			"factory_symbol",
			"output_good",
		),

		factoryReadyTotal: newGaugeVec(
			"manufacturing_factory_ready_total",
			"Number of factories ready for collection",
			"player_id",
		),

		factoryCyclesTotal: newCounterVec(
			"manufacturing_factory_cycles_total",
			"Total production cycles completed",
			"player_id",
			"factory_symbol",
			"output_good",
		),

		supplyTransitionsTotal: newCounterVec(
			"manufacturing_supply_transitions_total",
			"Supply level transitions by good",
			"player_id",
			"good_symbol",
			"from_level",
			"to_level",
		),

		// Ship Utilization Metrics
		shipsAssignedTotal: newGaugeVec(
			"manufacturing_ships_assigned_total",
			"Number of ships assigned to manufacturing tasks",
			"player_id",
		),

		shipsIdleTotal: newGaugeVec(
			"manufacturing_ships_idle_total",
			"Number of ships available for manufacturing",
			"player_id",
		),

		shipTaskDurationSeconds: newHistogramVec(
			"manufacturing_ship_task_duration_seconds",
			"Ship task execution duration in seconds",
			[]float64{10, 30, 60, 120, 300, 600},
			"player_id",
			"task_type",
		),

		shipUtilizationPercent: newGaugeVec(
			"manufacturing_ship_utilization_percent",
			"Ship utilization percentage (assigned/total * 100)",
			"player_id",
		),

		// Economic Metrics
		costTotal: newCounterVec(
			"manufacturing_cost_total",
			"Total manufacturing costs by type",
			"player_id",
			"cost_type",
		),

		revenueTotal: newCounterVec("manufacturing_revenue_total", "Total manufacturing revenue", "player_id"),

		profitRate: newGaugeVec("manufacturing_profit_rate", "Manufacturing profit rate (credits/hour)", "player_id"),

		marginPercent: newGaugeVec(
			"manufacturing_margin_percent",
			"Profit margin percentage by product",
			"player_id",
			"product_good",
		),

		// Starvation Metrics - detect and alert on task type starvation
		taskStarvationMinutes: newGaugeVec(
			"manufacturing_task_starvation_minutes",
			"Minutes since last task assignment by type (alert if > 15)",
			"player_id",
			"task_type",
		),

		taskAssignmentsTotal: newCounterVec(
			"manufacturing_task_assignments_total",
			"Total task assignments by type",
			"player_id",
			"task_type",
		),

		taskTypeReservationSkipsTotal: newCounterVec(
			"manufacturing_task_type_reservation_skips_total",
			"Total tasks skipped due to task type reservation rules",
			"player_id",
			"task_type",
			"reason",
		),

		// Configuration
		pollInterval: 30 * time.Second,
	}
}

// Register registers all manufacturing metrics with the Prometheus registry
func (c *ManufacturingMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.pipelineRunningTotal,
		c.pipelineQueueDepth,
		c.pipelineCompletedTotal,
		c.pipelineDurationSeconds,
		c.pipelineProfitCredits,
		c.tasksPendingTotal,
		c.tasksReadyTotal,
		c.tasksExecutingTotal,
		c.tasksCompletedTotal,
		c.taskDurationSeconds,
		c.taskRetryTotal,
		c.factorySupplyLevel,
		c.factoryInputsDelivered,
		c.factoryReadyTotal,
		c.factoryCyclesTotal,
		c.supplyTransitionsTotal,
		c.shipsAssignedTotal,
		c.shipsIdleTotal,
		c.shipTaskDurationSeconds,
		c.shipUtilizationPercent,
		c.costTotal,
		c.revenueTotal,
		c.profitRate,
		c.marginPercent,
		c.taskStarvationMinutes,
		c.taskAssignmentsTotal,
		c.taskTypeReservationSkipsTotal,
	)
}

// Start begins the polling goroutine for aggregate metrics
func (c *ManufacturingMetricsCollector) Start(ctx context.Context) {
	c.startContext(ctx)

	// Start polling (every 30 seconds), with an immediate initial poll.
	c.startPolling(c.pollInterval, true, c.updateAllMetrics)
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
	c.taskStarvationMinutes.Reset()

	// Get list of active players
	players := c.getActivePlayers()

	for _, playerID := range players {
		c.updatePipelineMetrics(playerID)
		c.updateTaskMetrics(playerID)
		c.updateFactoryMetrics(playerID)
		c.updateShipMetrics(playerID)
		c.updateEconomicMetrics(playerID)
		c.updateStarvationMetrics(playerID)
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
	return float64(manufacturing.SupplyLevel(level).Order())
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

	// Get hourly profit rate from completed COLLECT_SELL tasks in last hour
	// (pipelines stay in PLANNING status, so we calculate from task data instead)
	var hourlyStats struct {
		TotalCost    int64
		TotalRevenue int64
		TaskCount    int64
	}

	err := c.db.Raw(`
		SELECT
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(total_revenue), 0) as total_revenue,
			COUNT(*) as task_count
		FROM manufacturing_tasks
		WHERE player_id = ?
		  AND task_type = 'COLLECT_SELL'
		  AND status = 'COMPLETED'
		  AND completed_at > NOW() - INTERVAL '1 hour'
	`, playerID).Scan(&hourlyStats).Error

	if err != nil {
		log.Printf("Failed to get hourly economic stats: %v", err)
		return
	}

	// Profit rate (credits per hour) - calculated from completed sell tasks
	profitRate := float64(hourlyStats.TotalRevenue - hourlyStats.TotalCost)
	c.profitRate.WithLabelValues(playerIDStr).Set(profitRate)
}

// updateStarvationMetrics updates task starvation metrics
// Tracks minutes since last assignment by task type to detect starvation
func (c *ManufacturingMetricsCollector) updateStarvationMetrics(playerID int) {
	playerIDStr := strconv.Itoa(playerID)

	// Query minutes since last assignment by task type
	// Uses completed_at from the most recently completed task of each type
	var starvationData []struct {
		TaskType           string
		MinutesSinceAssign float64
	}

	err := c.db.Raw(`
		WITH last_assignments AS (
			SELECT task_type,
			       MAX(started_at) as last_assigned
			FROM manufacturing_tasks
			WHERE player_id = ?
			  AND started_at IS NOT NULL
			GROUP BY task_type
		),
		ready_counts AS (
			SELECT task_type, COUNT(*) as ready_count
			FROM manufacturing_tasks
			WHERE player_id = ?
			  AND status = 'READY'
			GROUP BY task_type
		)
		SELECT
			r.task_type,
			COALESCE(EXTRACT(EPOCH FROM (NOW() - la.last_assigned)) / 60, 999) as minutes_since_assign
		FROM ready_counts r
		LEFT JOIN last_assignments la ON r.task_type = la.task_type
		WHERE r.ready_count > 0
	`, playerID, playerID).Scan(&starvationData).Error

	if err != nil {
		log.Printf("Failed to get starvation metrics: %v", err)
		return
	}

	for _, data := range starvationData {
		c.taskStarvationMinutes.WithLabelValues(playerIDStr, data.TaskType).Set(data.MinutesSinceAssign)
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

// RecordTaskAssignment records a task assignment event
func (c *ManufacturingMetricsCollector) RecordTaskAssignment(playerID int, taskType string) {
	playerIDStr := strconv.Itoa(playerID)
	c.taskAssignmentsTotal.WithLabelValues(playerIDStr, taskType).Inc()
}

// RecordTaskTypeReservationSkip records when a task was skipped due to reservation rules
func (c *ManufacturingMetricsCollector) RecordTaskTypeReservationSkip(playerID int, taskType, reason string) {
	playerIDStr := strconv.Itoa(playerID)
	c.taskTypeReservationSkipsTotal.WithLabelValues(playerIDStr, taskType, reason).Inc()
}

// UpdateTaskStarvationMinutes updates the starvation minutes for task types
func (c *ManufacturingMetricsCollector) UpdateTaskStarvationMinutes(playerID int, taskType string, minutes float64) {
	playerIDStr := strconv.Itoa(playerID)
	c.taskStarvationMinutes.WithLabelValues(playerIDStr, taskType).Set(minutes)
}

// globalManufacturingCollector is the singleton manufacturing metrics collector
// Set by SetGlobalManufacturingCollector() when metrics are enabled
var globalManufacturingCollector *ManufacturingMetricsCollector

// SetGlobalManufacturingCollector sets the global manufacturing metrics collector
func SetGlobalManufacturingCollector(collector *ManufacturingMetricsCollector) {
	globalManufacturingCollector = collector
}

// GetGlobalManufacturingCollector returns the global manufacturing metrics collector
// Returns nil if metrics are not enabled
func GetGlobalManufacturingCollector() *ManufacturingMetricsCollector {
	return globalManufacturingCollector
}

// RecordManufacturingPipelineCompletion records a pipeline completion event globally
func RecordManufacturingPipelineCompletion(playerID int, productGood, status string, duration time.Duration, profit int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordPipelineCompletion(playerID, productGood, status, duration, profit)
	}
}

// RecordManufacturingTaskCompletion records a task completion event globally
func RecordManufacturingTaskCompletion(playerID int, taskType, status string, duration time.Duration) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskCompletion(playerID, taskType, status, duration)
	}
}

// RecordManufacturingTaskRetry records a task retry event globally
func RecordManufacturingTaskRetry(playerID int, taskType string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskRetry(playerID, taskType)
	}
}

// RecordManufacturingSupplyTransition records a supply level change event globally
func RecordManufacturingSupplyTransition(playerID int, good, fromLevel, toLevel string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordSupplyTransition(playerID, good, fromLevel, toLevel)
	}
}

// RecordManufacturingFactoryCycle records a factory production cycle completion globally
func RecordManufacturingFactoryCycle(playerID int, factorySymbol, outputGood string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordFactoryCycle(playerID, factorySymbol, outputGood)
	}
}

// RecordManufacturingCost records a manufacturing cost globally
func RecordManufacturingCost(playerID int, costType string, amount int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordCost(playerID, costType, amount)
	}
}

// RecordManufacturingRevenue records manufacturing revenue globally
func RecordManufacturingRevenue(playerID int, amount int) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordRevenue(playerID, amount)
	}
}

// RecordManufacturingTaskAssignment records a task assignment event globally
func RecordManufacturingTaskAssignment(playerID int, taskType string) {
	if globalManufacturingCollector != nil {
		globalManufacturingCollector.RecordTaskAssignment(playerID, taskType)
	}
}
