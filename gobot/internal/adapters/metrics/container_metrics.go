package metrics

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// containerMetricsPollInterval is how often the container-running gauge is refreshed.
	containerMetricsPollInterval = 10 * time.Second
	// shipMetricsPollInterval is how often the per-player ship role/status gauges are refreshed.
	shipMetricsPollInterval = 30 * time.Second
)

// ContainerMetricsCollector handles all container and ship metrics
type ContainerMetricsCollector struct {
	// Dependencies
	getContainers func() map[string]ContainerInfo // Function to get current containers
	shipRepo      navigation.ShipRepository       // For ship metrics

	// Container metrics
	containerRunningTotal *prometheus.GaugeVec
	containerTotal        *prometheus.CounterVec
	containerDuration     *prometheus.HistogramVec
	containerRestarts     *prometheus.CounterVec
	containerIterations   *prometheus.CounterVec
	containerExitTotal    *prometheus.CounterVec

	// Supervised daemon background component restarts (sp-i01z)
	daemonComponentRestarts *prometheus.CounterVec

	// Ship metrics
	shipsTotal      *prometheus.GaugeVec
	shipStatusTotal *prometheus.GaugeVec

	// shipVersionConflicts counts Save calls whose row version moved past the
	// entity's loaded version (sp-60ff tripwire). Unlabeled — the paired
	// ERROR log (ship_repository.go Save) carries the ship symbol.
	shipVersionConflicts prometheus.Counter

	// Lifecycle scaffolding (ctx/cancelFunc/wg + Start context + Stop) is shared
	// via the embedded pollingCollector.
	pollingCollector
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

		// Container exit counter (sp-dp92 P9): one increment per terminal
		// container exit. Fired from the same 3 call sites as
		// RecordContainerCompletion above (terminalizeClaimFailure,
		// finishCleanExit's completion branch, handleError) so this stays a
		// strict superset labeling of that existing signal rather than
		// diverging semantics between the two families.
		containerExitTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "container_exit_total",
				Help:      "Total number of container terminal exits by command type and status",
			},
			[]string{"player_id", "command_type", "status"},
		),

		// Supervised daemon background component restarts (sp-i01z). Labeled
		// by component only — a small fixed set (ship-state-sweeper,
		// container-recovery, ...), deliberately NOT per-ship.
		daemonComponentRestarts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "daemon",
				Name:      "component_restarts_total",
				Help:      "Restarts of supervised daemon background components",
			},
			[]string{"component"},
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

		// sp-60ff tripwire: ship saves that raced past their loaded version.
		// Unlabeled — the paired ERROR log carries the ship symbol.
		shipVersionConflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "ship",
			Name:      "version_conflicts_total",
			Help:      "Ship row writes that raced past their loaded version (concurrent-writer clobbers)",
		}),
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
		c.containerExitTotal,
		c.daemonComponentRestarts,
		c.shipsTotal,
		c.shipStatusTotal,
		c.shipVersionConflicts,
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
	c.startContext(ctx)

	// Start container metrics collector (tick-only, no initial poll).
	c.startPolling(containerMetricsPollInterval, false, c.updateContainerMetrics)

	// Start ship metrics collector
	if c.shipRepo != nil {
		c.startPolling(shipMetricsPollInterval, false, c.updateShipMetrics)
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

// updateShipMetrics reads current ships and updates Prometheus metrics
func (c *ContainerMetricsCollector) updateShipMetrics() {
	if c.shipRepo == nil {
		return
	}

	// Get unique player IDs from active containers
	containers := c.getContainers()
	if len(containers) == 0 {
		// No active containers, skip ship metrics
		return
	}

	playerIDs := make(map[int]bool)
	for _, containerInfo := range containers {
		playerIDs[containerInfo.PlayerID()] = true
	}

	// Reset gauges
	c.shipsTotal.Reset()
	c.shipStatusTotal.Reset()

	// Collect metrics for each player with active containers
	for playerID := range playerIDs {
		// Get all ships for the player
		ships, err := c.shipRepo.FindAllByPlayer(c.ctx, shared.MustNewPlayerID(playerID))
		if err != nil {
			log.Printf("Failed to list ships for player %d: %v", playerID, err)
			continue // Skip this player but continue with others
		}

		// Count ships by role, location, and status
		shipsByRole := make(map[string]map[string]int) // role -> location -> count
		shipsByStatus := make(map[string]int)          // status -> count

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

// RecordContainerExit records a container terminal exit event (sp-dp92 P9).
// Called from the same 3 sites RecordContainerCompletion already covers
// (terminalizeClaimFailure, finishCleanExit, handleError) so container_exit_total
// tracks exactly what container_total treats as terminal for this container.
// Nil-safe per RULINGS #4 (observation only): a recording miss must never
// panic a container's terminal exit path.
func (c *ContainerMetricsCollector) RecordContainerExit(containerInfo ContainerInfo) {
	if c == nil || c.containerExitTotal == nil {
		return
	}
	playerID := strconv.Itoa(containerInfo.PlayerID())
	commandType := string(containerInfo.Type())
	status := string(containerInfo.Status())

	c.containerExitTotal.WithLabelValues(playerID, commandType, status).Inc()
}

// RecordShipVersionConflict implements ShipWriteConflictRecorder (sp-60ff).
func (c *ContainerMetricsCollector) RecordShipVersionConflict() {
	c.shipVersionConflicts.Inc()
}

// RecordDaemonComponentRestart implements DaemonComponentRecorder (sp-i01z).
// Nil-safe like the other recorders: a metrics miss must never take down the
// supervise restart path that calls it.
func (c *ContainerMetricsCollector) RecordDaemonComponentRestart(component string) {
	if c == nil || c.daemonComponentRestarts == nil {
		return
	}
	c.daemonComponentRestarts.WithLabelValues(component).Inc()
}
