package metrics

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContainerMetricsCollector handles all container and ship metrics
type ContainerMetricsCollector struct {
	// Dependencies
	getContainers func() map[string]ContainerInfo // Function to get current containers
	shipRepo      navigation.ShipRepository       // For ship metrics

	// Container metrics
	containerRunningTotal   *prometheus.GaugeVec
	containerTotal          *prometheus.CounterVec
	containerDuration       *prometheus.HistogramVec
	containerRestarts       *prometheus.CounterVec
	containerIterations     *prometheus.CounterVec

	// Ship metrics
	shipsTotal      *prometheus.GaugeVec
	shipStatusTotal *prometheus.GaugeVec

	// Lifecycle
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
}

// ContainerInfo represents the data needed for metrics collection
// This interface allows us to abstract away the actual container/runner implementation
type ContainerInfo interface {
	PlayerID() int
	Type() container.ContainerType
	Status() container.ContainerStatus
	RestartCount() int
	CurrentIteration() int
	RuntimeDuration() time.Duration
}

// NewContainerMetricsCollector creates a new container metrics collector
func NewContainerMetricsCollector(
	getContainers func() map[string]ContainerInfo,
	shipRepo navigation.ShipRepository,
) *ContainerMetricsCollector {
	collector := &ContainerMetricsCollector{
		getContainers: getContainers,
		shipRepo:      shipRepo,

		// Container running gauge
		containerRunningTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_running_total",
				Help:      "Number of currently running containers by type and player",
			},
			[]string{"player_id", "container_type"},
		),

		// Container lifecycle counter
		containerTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_total",
				Help:      "Total number of container lifecycle events by status",
			},
			[]string{"player_id", "container_type", "status"},
		),

		// Container execution duration histogram
		containerDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_duration_seconds",
				Help:      "Container execution duration distribution",
				Buckets:   []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
			},
			[]string{"player_id", "container_type"},
		),

		// Container restarts counter
		containerRestarts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_restarts_total",
				Help:      "Total number of container restarts",
			},
			[]string{"player_id", "container_type"},
		),

		// Container iterations counter
		containerIterations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_iterations_total",
				Help:      "Total number of container iterations completed",
			},
			[]string{"player_id", "container_type"},
		),

		// Ship count by role/location
		shipsTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "ships_total",
				Help:      "Number of ships by role and location",
			},
			[]string{"player_id", "role", "location"},
		),

		// Ship status distribution
		shipStatusTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "ship_status_total",
				Help:      "Number of ships by navigation status",
			},
			[]string{"player_id", "status"},
		),
	}

	return collector
}

// Register registers all metrics with the Prometheus registry
func (c *ContainerMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.containerRunningTotal,
		c.containerTotal,
		c.containerDuration,
		c.containerRestarts,
		c.containerIterations,
		c.shipsTotal,
		c.shipStatusTotal,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// Start begins the metrics collection goroutines
func (c *ContainerMetricsCollector) Start(ctx context.Context) {
	c.ctx, c.cancelFunc = context.WithCancel(ctx)

	// Start container metrics collector (poll every 10 seconds)
	c.wg.Add(1)
	go c.collectContainerMetrics(10 * time.Second)

	// Start ship metrics collector (poll every 30 seconds)
	if c.shipRepo != nil {
		c.wg.Add(1)
		go c.collectShipMetrics(30 * time.Second)
	}
}

// Stop gracefully stops the metrics collection
func (c *ContainerMetricsCollector) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	c.wg.Wait()
}

// collectContainerMetrics polls container data and updates metrics
func (c *ContainerMetricsCollector) collectContainerMetrics(interval time.Duration) {
	defer c.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateContainerMetrics()
		}
	}
}

// updateContainerMetrics reads current containers and updates Prometheus metrics
func (c *ContainerMetricsCollector) updateContainerMetrics() {
	if c.getContainers == nil {
		return
	}

	containers := c.getContainers()

	// Reset running gauge (to handle removed containers)
	c.containerRunningTotal.Reset()

	// Update metrics for each running container
	for _, containerInfo := range containers {
		playerID := strconv.Itoa(containerInfo.PlayerID())
		containerType := string(containerInfo.Type())
		status := containerInfo.Status()

		// Update running gauge (only for RUNNING containers)
		if status == container.ContainerStatusRunning {
			c.containerRunningTotal.WithLabelValues(playerID, containerType).Set(1)
		}
	}
}

// collectShipMetrics polls ship data and updates metrics
func (c *ContainerMetricsCollector) collectShipMetrics(interval time.Duration) {
	defer c.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateShipMetrics()
		}
	}
}

// updateShipMetrics reads current ships and updates Prometheus metrics
func (c *ContainerMetricsCollector) updateShipMetrics() {
	if c.shipRepo == nil {
		return
	}

	// TODO: Support multiple players - for now hardcode player ID 1
	// This should be configurable or detected from active containers
	playerID := 1

	// Get all ships for the player
	ships, err := c.shipRepo.FindAllByPlayer(c.ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		log.Printf("Failed to list ships for metrics: %v", err)
		return
	}

	// Reset gauges
	c.shipsTotal.Reset()
	c.shipStatusTotal.Reset()

	// Count ships by role, location, and status
	shipsByRole := make(map[string]map[string]int)     // role -> location -> count
	shipsByStatus := make(map[string]int)              // status -> count

	for _, ship := range ships {
		role := ship.Role()
		location := ship.CurrentLocation().Symbol
		status := string(ship.NavStatus())

		// Initialize maps
		if shipsByRole[role] == nil {
			shipsByRole[role] = make(map[string]int)
		}

		// Increment counters
		shipsByRole[role][location]++
		shipsByStatus[status]++
	}

	playerIDStr := strconv.Itoa(playerID)

	// Update ship count by role/location
	for role, locationMap := range shipsByRole {
		for location, count := range locationMap {
			c.shipsTotal.WithLabelValues(playerIDStr, role, location).Set(float64(count))
		}
	}

	// Update ship count by status
	for status, count := range shipsByStatus {
		c.shipStatusTotal.WithLabelValues(playerIDStr, status).Set(float64(count))
	}
}

// RecordContainerCompletion records a container completion event
// This should be called when a container transitions to a terminal state
func (c *ContainerMetricsCollector) RecordContainerCompletion(containerInfo ContainerInfo) {
	playerID := strconv.Itoa(containerInfo.PlayerID())
	containerType := string(containerInfo.Type())
	status := string(containerInfo.Status())

	// Increment completion counter
	c.containerTotal.WithLabelValues(playerID, containerType, status).Inc()

	// Record duration histogram (only for completed/failed, not stopped)
	if containerInfo.Status() == container.ContainerStatusCompleted ||
		containerInfo.Status() == container.ContainerStatusFailed {
		duration := containerInfo.RuntimeDuration().Seconds()
		c.containerDuration.WithLabelValues(playerID, containerType).Observe(duration)
	}
}

// RecordContainerRestart records a container restart event
func (c *ContainerMetricsCollector) RecordContainerRestart(containerInfo ContainerInfo) {
	playerID := strconv.Itoa(containerInfo.PlayerID())
	containerType := string(containerInfo.Type())

	c.containerRestarts.WithLabelValues(playerID, containerType).Inc()
}

// RecordContainerIteration records a container iteration completion
func (c *ContainerMetricsCollector) RecordContainerIteration(containerInfo ContainerInfo) {
	playerID := strconv.Itoa(containerInfo.PlayerID())
	containerType := string(containerInfo.Type())

	c.containerIterations.WithLabelValues(playerID, containerType).Inc()
}
