package container

import (
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ErrContainerAlreadyTerminal marks a stop refused because the container has ALREADY reached a
// terminal state (COMPLETED or STOPPED). It is a distinct condition from every other stop failure
// and callers must be able to tell them apart, because the two demand opposite responses: a stop
// that failed against a LIVE container leaves it running and still holding its resources, so the
// caller must back off; a stop refused because the container is already terminal means the work is
// done and the resources are owed back — backing off there strands them (sp-vz8hj).
//
// Wrapped rather than returned bare so the message keeps its "cannot stop container in %s state"
// prefix: an existing caller matches on that substring, and a typed error that silently changed
// the text would break it while every test still passed.
var ErrContainerAlreadyTerminal = errors.New("container is already in a terminal state")

// ContainerStatus represents the lifecycle state of a container
type ContainerStatus string

const (
	// ContainerStatusPending indicates container is queued but not started
	ContainerStatusPending ContainerStatus = "PENDING"

	// ContainerStatusRunning indicates container is actively executing
	ContainerStatusRunning ContainerStatus = "RUNNING"

	// ContainerStatusCompleted indicates container finished successfully
	ContainerStatusCompleted ContainerStatus = "COMPLETED"

	// ContainerStatusFailed indicates container encountered an error
	ContainerStatusFailed ContainerStatus = "FAILED"

	// ContainerStatusStopping indicates container is gracefully shutting down
	ContainerStatusStopping ContainerStatus = "STOPPING"

	// ContainerStatusStopped indicates container was stopped by user
	ContainerStatusStopped ContainerStatus = "STOPPED"

	// ContainerStatusInterrupted indicates container was running when daemon stopped, pending recovery
	ContainerStatusInterrupted ContainerStatus = "INTERRUPTED"
)

// ContainerType categorizes the operation type
type ContainerType string

const (
	ContainerTypeNavigate                 ContainerType = "NAVIGATE"
	ContainerTypeDock                     ContainerType = "DOCK"
	ContainerTypeOrbit                    ContainerType = "ORBIT"
	ContainerTypeRefuel                   ContainerType = "REFUEL"
	ContainerTypeJettison                 ContainerType = "JETTISON"
	ContainerTypeScout                    ContainerType = "SCOUT"
	ContainerTypeContract                 ContainerType = "CONTRACT"
	ContainerTypeContractWorkflow         ContainerType = "CONTRACT_WORKFLOW"
	ContainerTypeContractFleetCoordinator ContainerType = "CONTRACT_FLEET_COORDINATOR"
	ContainerTypeBalancing                ContainerType = "BALANCING"
	ContainerTypeTrading                  ContainerType = "TRADING"
	ContainerTypeScoutFleetAssignment     ContainerType = "SCOUT_FLEET_ASSIGNMENT"
	ContainerTypeScoutPostCoordinator     ContainerType = "SCOUT_POST_COORDINATOR"
	ContainerTypeTradeFleetCoordinator    ContainerType = "TRADE_FLEET_COORDINATOR"
	ContainerTypeScoutReposition          ContainerType = "SCOUT_REPOSITION"
	// ContainerTypeWorkerFerry is the one-shot cross-system ferry worker (twin of
	// ContainerTypeScoutReposition). Its former spawner — the worker_rebalancer_coordinator — was
	// retired with the factory ops; the ferry primitive is retained.
	ContainerTypeWorkerFerry ContainerType = "WORKER_FERRY"
	// ContainerTypeCargoLiquidation is the one-shot cargo-liquidation worker the contract
	// fleet coordinator spawns on a parked-with-cargo hull, twin of
	// ContainerTypeWorkerFerry: coordinator-managed, one iteration, self-clears the strand.
	ContainerTypeCargoLiquidation  ContainerType = "CARGO_LIQUIDATION"
	ContainerTypeFrontierExpansion ContainerType = "FRONTIER_EXPANSION_COORDINATOR"
	// ContainerTypeMarketFreshnessSizer is the standing market-freshness auto-sizer: a
	// per-player coordinator that loops forever inside one Handle() sizing each market-bearing
	// system's standing scout post to a freshness SLA and auto-buying probes behind the shared
	// money-guard stack. Like the frontier/siting/autosizer coordinators it is NOT a
	// CoordinatorOwnsIterations type.
	ContainerTypeMarketFreshnessSizer ContainerType = "MARKET_FRESHNESS_SIZER_COORDINATOR"
	// ContainerTypeProbeSensingCoordinator is the standing probe-sensing coordinator: a
	// per-player coordinator that loops forever inside one Handle() reconciling the
	// whitelist-scoped sensing footprint — standing posts sized by market depth, the
	// pressure-driven dormancy rotation, sweep-once discovery declares, and the budgeted
	// probe buy behind the shared money-guard stack. Successor of the market-freshness
	// sizer + frontier expansion pair. Like the siting/autosizer coordinators it is NOT a
	// CoordinatorOwnsIterations type.
	ContainerTypeProbeSensingCoordinator ContainerType = "PROBE_SENSING_COORDINATOR"
	// ContainerTypeShipyardBackfillCoordinator is the standing shipyard-backfill sweep: a
	// per-player coordinator that loops forever inside one Handle() closing the charted-but-
	// unscanned shipyard blind spot the market-tour-only scan left behind — enumerating known-
	// shipyard systems the depth frontier reached but no market tour toured and declaring deeper-
	// first sweep-once posts the reconciler mans. Like the frontier/siting/autosizer coordinators
	// it is NOT a CoordinatorOwnsIterations type.
	ContainerTypeShipyardBackfillCoordinator ContainerType = "SHIPYARD_BACKFILL_COORDINATOR"
	ContainerTypePurchase                    ContainerType = "PURCHASE"
	// ContainerTypeFleetAutosizer is the standing fleet capacity autosizer: a
	// per-player coordinator that loops forever inside one Handle() sizing the hull pool to
	// demand and auto-buying hulls (lights to factory demand, heavies to trade demand) behind
	// the full money-guard stack. Like the trade-fleet/siting coordinators it is NOT a
	// CoordinatorOwnsIterations type.
	ContainerTypeFleetAutosizer ContainerType = "FLEET_AUTOSIZER_COORDINATOR"
	// ContainerTypeContractScaler is the standing dedicated contract auto-scaler: a per-player
	// coordinator that loops forever inside one Handle() ramping a FIXED, EXCLUSIVE contract fleet to a
	// live-tunable ceiling behind ONE money guard (the 200000-credit cushion). It resolves this era's
	// waypoint roles ONCE at arm (a lookup, not a solve) and drives the kept autosizer buy primitive
	// with the fixed plan. Like the siting/autosizer coordinators it is NOT a CoordinatorOwnsIterations
	// type. Its launch is ARMED (default-off) at the bootstrap early-scaling seam, never boot-standing;
	// once launched it survives restarts via the persisted-container recovery idiom.
	ContainerTypeContractScaler ContainerType = "CONTRACT_SCALER_COORDINATOR"
	// ContainerTypeOpportunityRelocator is the standing OPPORTUNITY RELOCATOR (sp-zvywu Part 2): a
	// per-player reconciler that loops forever inside one Handle() ranking every (trade hull, reachable
	// region) pair by relocation NPV and moving the best-valued hulls onto better-earning ground. It is
	// the rate-floor rescue's trigger INVERTED — that one rescues an under-earner, this one chases
	// upside a perfectly-profitable hull would otherwise never leave for. Like the
	// siting/autosizer/contract-scaler coordinators it is NOT a CoordinatorOwnsIterations type; the
	// container-level budget (-1) is irrelevant. It NEVER SPENDS (movement only, RULINGS #4) and never
	// touches a protected hull (RULINGS #7); the one stop is the shared reposition kill-switch.
	ContainerTypeOpportunityRelocator ContainerType = "OPPORTUNITY_RELOCATOR_COORDINATOR"
	// ContainerTypeBootstrapCoordinator is the standing captain bootstrap coordinator: a
	// per-player reconciler that loops forever inside one Handle() driving a cold agent through
	// the cold-start arc to the jump gate (DATA→INCOME→GATE). Like the siting/autosizer
	// coordinators it is NOT a CoordinatorOwnsIterations type.
	ContainerTypeBootstrapCoordinator ContainerType = "BOOTSTRAP_COORDINATOR"
	// ContainerTypeAutoOutfitCoordinator is the standing guarded auto-outfit coordinator: a
	// per-player coordinator that loops forever inside one Handle() reading
	// per-hull cargo saturation from tour_leg_telemetry, cataloguing available modules,
	// and installing the highest-marginal-value (hull, module) upgrade behind a
	// fail-closed money/ceiling/cap guard stack — the module analogue of the autosizer's
	// hull-buying. Like the siting/autosizer/capacity coordinators it is NOT a
	// CoordinatorOwnsIterations type. DEPLOY-INERT: it is never boot-standing-armed — it
	// runs only when explicitly started, then survives restarts through the
	// persisted-container recovery idiom.
	ContainerTypeAutoOutfitCoordinator ContainerType = "AUTO_OUTFIT_COORDINATOR"
	// ContainerTypeConstructionCoordinator is the standing construction-supply drain: a
	// per-player coordinator that loops forever inside one Handle() sourcing and delivering a
	// gate-construction pipeline's READY DELIVER_TO_CONSTRUCTION tasks on the shared
	// ProductionExecutor engine. Like the siting/autosizer/bootstrap coordinators it is NOT a
	// CoordinatorOwnsIterations type. It is the dedicated executor the bootstrap GATE adoption
	// check looks for (replacing the vestigial MANUFACTURING_COORDINATOR for construction).
	ContainerTypeConstructionCoordinator ContainerType = "CONSTRUCTION_COORDINATOR"
	ContainerTypeParallelManufacturing   ContainerType = "PARALLEL_MANUFACTURING"
	ContainerTypeManufacturingTaskWorker ContainerType = "MANUFACTURING_TASK_WORKER"
	ContainerTypeGasCoordinator          ContainerType = "GAS_COORDINATOR"
	ContainerTypeGasSiphonWorker         ContainerType = "GAS_SIPHON_WORKER"
	ContainerTypeStorageShip             ContainerType = "STORAGE_SHIP"
	ContainerTypeWarehouse               ContainerType = "WAREHOUSE"
	ContainerTypeJump                    ContainerType = "JUMP"
	ContainerTypeOutfitting              ContainerType = "OUTFITTING"
	// ContainerTypeRoute is the one-shot cross-system point-to-point move behind the
	// `ship route` verb. Unlike ContainerTypeNavigate (in-system only) it
	// reuses the trade-route coordinator's multi-jump travel() to cross gates. Like the
	// other one-shot ship ops it is a single-iteration, CoordinatorOwnsIterations type.
	ContainerTypeRoute ContainerType = "ROUTE"
	// ContainerTypeWarp is the one-shot OFF-gate move behind the `ship warp` verb.
	// Unlike ContainerTypeRoute (which crosses jump gates) it warps a warp-drive hull
	// straight to a waypoint in another system, reaching targets the gate network does
	// not connect. Like the other one-shot ship ops it is a single-iteration,
	// CoordinatorOwnsIterations type.
	ContainerTypeWarp ContainerType = "WARP"
	// ContainerTypeLongHaulArbCoordinator is the standing long-haul arb fleet coordinator
	// (sp-mepj): a per-player coordinator that loops forever inside one Handle() launching a
	// per-hull long-haul worker on every idle long-haul-tagged hull and running the SHARED
	// sp-m3122 watchdog. Like the trade-fleet/autosizer coordinators it is NOT a
	// CoordinatorOwnsIterations type; it survives restarts via the persisted-container recovery
	// idiom.
	ContainerTypeLongHaulArbCoordinator ContainerType = "LONGHAUL_ARB_COORDINATOR"
	// ContainerTypeLongHaulArb is the per-hull long-haul WORKER the coordinator spawns: it runs
	// continuous discover->buy->multi-jump->sell->backhaul episodes on ONE long-haul hull,
	// composing the reused arb executor. Coordinator-managed, continuous (-1) until the worker
	// finds no lane; recovery-safe (stateless re-derive from the DB on restart).
	ContainerTypeLongHaulArb ContainerType = "LONGHAUL_ARB"
	// ContainerTypeProbeBuyerCoordinator (PROBE_BUYER_COORDINATOR) was REMOVED with the
	// probe-buyer retirement (Admiral 2026-07-28). Probe supply belongs to the sensing coordinator.
	//
	// Persisted rows carrying the old string are not orphaned: recovery matches on COMMAND type, and
	// "probe_buyer_coordinator" is listed in retiredCommandTypes (daemon_server.go), so a stale row is
	// marked terminated on the first post-retirement boot rather than alarming as an unexplained loss.
	// Nothing validates the container_type column against this list, so removing the constant cannot
	// make an existing row unreadable.
)

const (
	// MaxRestartAttempts defines the maximum number of automatic restart attempts
	// for a failed container. This prevents infinite restart loops while allowing
	// recovery from transient failures.
	MaxRestartAttempts = 3
)

// Container represents a background operation running in the daemon
// Containers are the unit of work orchestration - each runs in its own goroutine
// and can be started, stopped, monitored, and restarted independently.
//
// Lifecycle Integration:
// - Uses LifecycleStateMachine for core state management and timestamps
// - Adds Container-specific states (STOPPING, INTERRUPTED)
// - Adds Container-specific features (iterations, restarts, metadata)
//
// Parent-Child Relationship:
// - parentContainerID tracks which coordinator spawned this container
// - NULL/nil = root container (coordinator, standalone worker)
// - Non-nil = child container spawned by a coordinator
type Container struct {
	id            string
	containerType ContainerType
	playerID      int

	// Core lifecycle managed by state machine
	lifecycle *shared.LifecycleStateMachine

	// Container-specific state extensions
	stopping    bool // Indicates STOPPING state (graceful shutdown)
	interrupted bool // Indicates INTERRUPTED state (daemon crash recovery)

	// Parent-child relationship tracking
	parentContainerID *string // ID of parent coordinator (nil for root containers)

	// Iteration tracking for looping operations
	currentIteration int
	maxIterations    int // -1 for infinite

	// Restart tracking
	restartCount int
	maxRestarts  int

	// Operation-specific metadata (JSON-serializable)
	metadata map[string]interface{}

	// Time provider for testability (delegated to lifecycle)
	clock shared.Clock
}

// NewContainer creates a new container instance
// If clock is nil, uses RealClock (production behavior)
// parentContainerID should be nil for root containers (coordinators, standalone workers)
// and non-nil for child containers spawned by coordinators
func NewContainer(
	id string,
	containerType ContainerType,
	playerID int,
	maxIterations int,
	parentContainerID *string,
	metadata map[string]interface{},
	clock shared.Clock,
) *Container {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &Container{
		id:                id,
		containerType:     containerType,
		playerID:          playerID,
		lifecycle:         shared.NewLifecycleStateMachine(clock),
		stopping:          false,
		interrupted:       false,
		parentContainerID: parentContainerID,
		currentIteration:  0,
		maxIterations:     maxIterations,
		restartCount:      0,
		maxRestarts:       MaxRestartAttempts,
		metadata:          metadata,
		clock:             clock,
	}
}

// Getters

func (c *Container) ID() string                       { return c.id }
func (c *Container) Type() ContainerType              { return c.containerType }
func (c *Container) PlayerID() int                    { return c.playerID }
func (c *Container) ParentContainerID() *string       { return c.parentContainerID }
func (c *Container) CurrentIteration() int            { return c.currentIteration }
func (c *Container) MaxIterations() int               { return c.maxIterations }
func (c *Container) RestartCount() int                { return c.restartCount }
func (c *Container) MaxRestarts() int                 { return c.maxRestarts }
func (c *Container) Metadata() map[string]interface{} { return c.metadata }

// IsRootContainer returns true if this container has no parent (coordinator or standalone worker)
func (c *Container) IsRootContainer() bool {
	return c.parentContainerID == nil
}

// Lifecycle timestamp accessors (delegate to lifecycle machine)

func (c *Container) CreatedAt() time.Time  { return c.lifecycle.CreatedAt() }
func (c *Container) UpdatedAt() time.Time  { return c.lifecycle.UpdatedAt() }
func (c *Container) StartedAt() *time.Time { return c.lifecycle.StartedAt() }
func (c *Container) StoppedAt() *time.Time { return c.lifecycle.StoppedAt() }
func (c *Container) LastError() error      { return c.lifecycle.LastError() }

// containerStatusByLifecycle projects each shared lifecycle state onto the
// Container-facing status. The Container-specific extension states (STOPPING,
// INTERRUPTED) are resolved by Status() BEFORE this table because they are not
// lifecycle states. A lifecycle state absent here falls back to
// ContainerStatusPending.
var containerStatusByLifecycle = map[shared.LifecycleStatus]ContainerStatus{
	shared.LifecycleStatusPending:   ContainerStatusPending,
	shared.LifecycleStatusRunning:   ContainerStatusRunning,
	shared.LifecycleStatusCompleted: ContainerStatusCompleted,
	shared.LifecycleStatusFailed:    ContainerStatusFailed,
	shared.LifecycleStatusStopped:   ContainerStatusStopped,
}

// Status returns the current container status
// Maps LifecycleStatus to ContainerStatus with Container-specific extensions
func (c *Container) Status() ContainerStatus {
	if c.stopping {
		return ContainerStatusStopping
	}
	if c.interrupted {
		return ContainerStatusInterrupted
	}

	return shared.ProjectStatus(c.lifecycle, containerStatusByLifecycle, ContainerStatusPending)
}

// State transition methods

// Start transitions container to RUNNING state
// Delegates to lifecycle state machine
func (c *Container) Start() error {
	status := c.Status()
	if status != ContainerStatusPending && status != ContainerStatusStopped {
		return fmt.Errorf("cannot start container in %s state", status)
	}

	// Clear Container-specific flags
	c.stopping = false
	c.interrupted = false

	return c.lifecycle.Start()
}

// Complete transitions container to COMPLETED state
// Delegates to lifecycle state machine
func (c *Container) Complete() error {
	status := c.Status()
	if status != ContainerStatusRunning {
		return fmt.Errorf("cannot complete container in %s state", status)
	}

	c.stopping = false
	return c.lifecycle.Complete()
}

// Fail transitions container to FAILED state with error
// Delegates to lifecycle state machine with error tracking
func (c *Container) Fail(err error) error {
	status := c.Status()
	if status == ContainerStatusCompleted || status == ContainerStatusStopped {
		return fmt.Errorf("cannot fail container in %s state", status)
	}

	c.stopping = false
	return c.lifecycle.Fail(err)
}

// Stop transitions container to STOPPING then STOPPED state
// STOPPING is a Container-specific state for graceful shutdown
func (c *Container) Stop() error {
	status := c.Status()
	if status == ContainerStatusCompleted || status == ContainerStatusStopped {
		return fmt.Errorf("cannot stop container in %s state: %w", status, ErrContainerAlreadyTerminal)
	}

	// First go to STOPPING to signal graceful shutdown
	if status == ContainerStatusRunning {
		c.stopping = true
		c.lifecycle.UpdateTimestamp()
		return nil
	}

	// Then finalize to STOPPED
	c.stopping = false
	return c.lifecycle.Stop()
}

// MarkStopped finalizes the stop transition
// Transitions from STOPPING to STOPPED
func (c *Container) MarkStopped() error {
	if c.Status() != ContainerStatusStopping {
		return fmt.Errorf("cannot mark stopped when not in stopping state")
	}

	c.stopping = false
	return c.lifecycle.Stop()
}

// Iteration management

func (c *Container) IncrementIteration() error {
	if c.Status() != ContainerStatusRunning {
		return fmt.Errorf("cannot increment iteration in %s state", c.Status())
	}

	c.currentIteration++
	c.lifecycle.UpdateTimestamp()
	return nil
}

func (c *Container) ShouldContinue() bool {
	// Infinite loop: maxIterations = -1
	if c.maxIterations == -1 {
		return true
	}

	// Finite loop: check if more iterations remain
	return c.currentIteration < c.maxIterations
}

// Restart management

func (c *Container) CanRestart() bool {
	if c.Status() != ContainerStatusFailed {
		return false
	}

	return c.restartCount < c.maxRestarts
}

func (c *Container) IncrementRestartCount() {
	c.restartCount++
	c.lifecycle.UpdateTimestamp()
}

// ResetForRestart prepares container for restart attempt
// Delegates to lifecycle state machine for state reset
func (c *Container) ResetForRestart() error {
	if !c.CanRestart() {
		return fmt.Errorf("container cannot be restarted (restarts: %d/%d)",
			c.restartCount, c.maxRestarts)
	}

	c.stopping = false
	c.interrupted = false
	c.lifecycle.ResetForRestart()
	c.IncrementRestartCount()
	return nil
}

// Metadata management

// UpdateMetadata merges new metadata into existing metadata
func (c *Container) UpdateMetadata(updates map[string]interface{}) {
	if c.metadata == nil {
		c.metadata = make(map[string]interface{})
	}

	for key, value := range updates {
		c.metadata[key] = value
	}

	c.lifecycle.UpdateTimestamp()
}

func (c *Container) GetMetadataValue(key string) (interface{}, bool) {
	if c.metadata == nil {
		return nil, false
	}

	value, exists := c.metadata[key]
	return value, exists
}

// State queries

func (c *Container) IsRunning() bool {
	return c.Status() == ContainerStatusRunning
}

func (c *Container) IsFinished() bool {
	status := c.Status()
	return status == ContainerStatusCompleted ||
		status == ContainerStatusFailed ||
		status == ContainerStatusStopped
}

func (c *Container) IsStopping() bool {
	return c.stopping
}

// Runtime calculation

// RuntimeDuration calculates how long the container has been running
// Delegates to lifecycle state machine
func (c *Container) RuntimeDuration() time.Duration {
	return c.lifecycle.RuntimeDuration()
}

func (c *Container) String() string {
	return fmt.Sprintf("Container[%s, type=%s, status=%s, iteration=%d/%d, restarts=%d]",
		c.id, c.containerType, c.Status(), c.currentIteration, c.maxIterations, c.restartCount)
}
