package navigation

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipMutation re-applies an intended change onto a freshly-loaded ship for the
// CAS-retry save path (SaveWithRetry, sp-01wc). Because it runs again on the
// fresh row after every concurrent-writer conflict, it must fold in its own
// applicability guard and be idempotent: it reports changed=false when the ship
// is already in the desired state (e.g. another writer already transitioned it),
// so the caller skips the write with no spurious version bump. A returned error
// aborts the whole operation.
type ShipMutation func(ship *Ship) (changed bool, err error)

// ShipQueryRepository handles ship data queries.
//
// This interface follows the Interface Segregation Principle (ISP) by focusing
// exclusively on read operations. Implementations that only need to query ship
// data don't need to implement command operations.
type ShipQueryRepository interface {
	// FindBySymbol retrieves a ship (from API with waypoint reconstruction)
	FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*Ship, error)

	// GetShipData retrieves raw ship data from API (includes arrival time for IN_TRANSIT ships)
	GetShipData(ctx context.Context, symbol string, playerID shared.PlayerID) (*ShipData, error)

	// FindAllByPlayer retrieves all ships for a player (from API with waypoint reconstruction)
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
}

// ShipCommandRepository handles ship actions and state changes.
//
// This interface follows ISP by focusing on write operations that modify ship state.
// Separating commands from queries enables CQRS pattern adoption in the future.
type ShipCommandRepository interface {
	// Navigate executes ship navigation (updates via API)
	// Returns navigation result with arrival time from API
	Navigate(ctx context.Context, ship *Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*Result, error)

	// Dock docks the ship (updates via API)
	Dock(ctx context.Context, ship *Ship, playerID shared.PlayerID) error

	// Orbit puts ship in orbit (updates via API)
	Orbit(ctx context.Context, ship *Ship, playerID shared.PlayerID) error

	// Refuel refuels the ship (updates via API)
	// Returns RefuelResult with actual cost from API
	Refuel(ctx context.Context, ship *Ship, playerID shared.PlayerID, units *int) (*RefuelResult, error)

	// SetFlightMode sets the ship's flight mode (updates via API)
	SetFlightMode(ctx context.Context, ship *Ship, playerID shared.PlayerID, mode string) error
}

// ShipCargoRepository handles cargo operations.
//
// This interface follows ISP by isolating cargo-specific operations.
// Implementations that only need cargo management don't need navigation capabilities.
type ShipCargoRepository interface {
	// JettisonCargo jettisons cargo from the ship (updates via API)
	JettisonCargo(ctx context.Context, ship *Ship, playerID shared.PlayerID, goodSymbol string, units int) error
}

// ShipRepository combines all ship repository interfaces for convenience.
//
// This composite interface maintains backward compatibility while enabling
// ISP-compliant implementations. Use this when you need full ship repository
// capabilities, or use the focused interfaces (ShipQueryRepository,
// ShipCommandRepository, ShipCargoRepository) when you only need specific operations.
//
// Following hexagonal architecture: After daemon startup, the database is the source
// of truth for ship state. Ships are synced from API on startup, and all queries
// read from the database. API calls are only made for state-changing operations.
type ShipRepository interface {
	ShipQueryRepository
	ShipCommandRepository
	ShipCargoRepository

	// Assignment query methods (ships enriched with DB assignment state)
	FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*Ship, error)
	FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
	FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*Ship, error)
	CountByContainerPrefix(ctx context.Context, prefix string, playerID shared.PlayerID) (int, error)

	// Persistence methods (save ship aggregate including full state)
	Save(ctx context.Context, ship *Ship) error
	SaveAll(ctx context.Context, ships []*Ship) error

	// SaveWithRetry loads the ship fresh, applies mutate, and persists it under
	// the ships.version CAS guard (sp-60ff). On a concurrent-writer conflict it
	// RE-loads the fresh row, RE-applies mutate on top of that fresh state, and
	// retries the CAS save — bounded by the repository's max-CAS-retries knob —
	// so both the concurrent writer's mutation AND this one survive instead of
	// the loser being last-write-wins clobbered (sp-01wc). On retry exhaustion,
	// or when the knob disables retry, it falls back to the legacy
	// last-write-wins upsert, so behavior never regresses below sp-60ff.
	//
	// Returns the persisted ship (for post-save side effects such as event
	// publication) and whether a write actually occurred — false when mutate
	// reports the ship is already in the desired state (a concurrent writer got
	// there first), in which case no write and no spurious version bump happen.
	SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate ShipMutation) (*Ship, bool, error)

	// ReleaseAllActive releases all active ship assignments for the given player
	// (used for daemon startup cleanup). Scoped to playerID so that multiple
	// players' rows (e.g. a dead closed-era player and the live open-era player)
	// never cross-contaminate each other's assignment state.
	ReleaseAllActive(ctx context.Context, playerID shared.PlayerID, reason string) (int, error)

	// ClaimShip exclusively assigns an idle ship to a container.
	// Returns ErrShipAlreadyAssigned if ship is already assigned to another container.
	// This method handles concurrency internally - multiple callers competing for the same
	// ship will not cause race conditions.
	//
	// operation identifies the claiming coordinator's fleet name (e.g.
	// "contract", "manufacturing"). A ship whose persisted DedicatedFleet tag
	// is non-empty and differs from operation is rejected with
	// ShipDedicatedToOtherFleetError — checked inside the same row-locked
	// transaction as the other assignment guards, so dedication can never be
	// raced past by a claim that read stale discovery data (sp-l7h2).
	ClaimShip(ctx context.Context, shipSymbol string, containerID string, playerID shared.PlayerID, operation string) error

	// AssignFleet atomically sets the ship's DedicatedFleet tag — the single
	// write path for fleet dedication (sp-l7h2). fleet == "" clears the
	// dedication, returning the ship to the general pool. Idempotent: writing
	// the value already persisted performs zero DB writes, so reconciliation
	// on every coordinator restart stays cheap. Dedication is ownership, not
	// occupancy: assigning a currently-claimed or captain-reserved ship
	// succeeds — the tag governs who may claim it NEXT, it does not evict the
	// current holder.
	AssignFleet(ctx context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error

	// SetCargoReservation atomically sets (reserved=true) or releases to sellable
	// (reserved=false) a single cargo do-not-sell override on a hull (sp-1vhv),
	// row-locked like AssignFleet. reserved=false is the deliberate-resale escape
	// hatch that releases a default-reserved module (MODULE_*/MOUNT_*) for sale.
	// The persisted override is reloaded on boot and honored at every coordinator
	// sell leg via Ship.IsCargoReserved.
	SetCargoReservation(ctx context.Context, shipSymbol, good string, reserved bool, playerID shared.PlayerID) error

	// ReserveForCaptain atomically reserves an idle ship for the captain's direct,
	// manual use, hiding it from coordinator discovery (sp-i1ku). Uses the same
	// row-level locking as ClaimShip so a concurrent coordinator claim can never
	// be silently overwritten by a captain reservation, or vice versa. Returns
	// ShipAlreadyAssignedError if a container already holds the claim.
	ReserveForCaptain(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error

	// ReleaseCaptainReservation atomically clears a captain reservation, returning
	// the ship to idle so normal coordinator discovery can claim it again
	// (sp-i1ku). Returns ShipNotReservedError if the ship is not currently
	// reserved by the captain.
	ReleaseCaptainReservation(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error

	// PreemptForCaptain atomically REVOKES a coordinator's live container claim
	// and transfers the hull to the captain — the operator-authority preempt
	// behind `ship reserve --force` (sp-w3yd). Unlike ReserveForCaptain, a live
	// container claim is transferred (not rejected) in a single row-locked swap
	// (RULING #7), so a coordinator re-grab cannot race a lost update; the
	// coordinator's per-tick FindByContainer then drops the hull and it re-plans.
	// Returns the container id the claim was revoked from, or "" if the hull was
	// idle. A hull the captain already holds is rejected like ReserveForCaptain.
	PreemptForCaptain(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) (string, error)

	// ReleaseContainerClaim atomically breaks a hull's LIVE coordinator
	// work-claim, returning it to idle so the coordinator stops routing it — the
	// extra step `fleet unassign` performs beyond clearing the DedicatedFleet tag
	// (sp-w3yd). Scoped to a container claim: a captain reservation is left
	// untouched (that is `ship release`'s job) and an idle hull is a no-op.
	// Returns whether a live claim was actually broken.
	ReleaseContainerClaim(ctx context.Context, shipSymbol string, playerID shared.PlayerID, reason string) (bool, error)

	// Sync methods (API -> Database)
	SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error)
	SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*Ship, error)

	// Background updater queries
	FindInTransitWithPastArrival(ctx context.Context) ([]*Ship, error)
	FindInTransitWithFutureArrival(ctx context.Context) ([]*Ship, error)
	FindWithExpiredCooldown(ctx context.Context) ([]*Ship, error)
	FindWithFutureCooldown(ctx context.Context) ([]*Ship, error)

	// FindModuleRequirements resolves a not-yet-installed module's own
	// power/crew/slot requirements by searching every ship's installed
	// module list for symbol (sp-el60 acceptance fix). There is no catalog
	// of unowned module specs anywhere in this codebase or the SpaceTraders
	// API, so a candidate's requirements can only come from having been
	// observed installed somewhere - the same module symbol has identical
	// requirements on every hull that carries it, and this query is
	// unscoped by player for that reason, mirroring the unscoped background
	// updater queries above. The bool return is false only when no ship
	// anywhere has ever carried symbol; callers must treat that as
	// "requirements unknown" (see UnknownRequirementsFeasibility), never
	// substitute a zero-valued ShipRequirements.
	FindModuleRequirements(ctx context.Context, symbol string) (ShipRequirements, bool, error)
}

// ArrivalScheduler schedules ship arrival transitions.
// When a ship starts navigation, this scheduler is notified to set up
// a timer that will transition the ship from IN_TRANSIT to IN_ORBIT
// at the API-provided arrival time.
type ArrivalScheduler interface {
	// ScheduleArrival schedules a timer to transition ship from IN_TRANSIT to IN_ORBIT
	ScheduleArrival(ship *Ship)
}

// ShipEventPublisher publishes ship state change events.
// Implemented by the scheduler to notify waiting containers when ship state changes.
type ShipEventPublisher interface {
	// PublishArrived publishes an ARRIVED event when a ship transitions out of IN_TRANSIT
	PublishArrived(shipSymbol string, playerID shared.PlayerID, location string, status NavStatus)

	// PublishWorkerCompleted publishes a worker completion event.
	// Used by ContainerRunner to notify coordinators when a worker finishes.
	PublishWorkerCompleted(event WorkerCompletedEvent)

	// PublishTasksBecameReady publishes a tasks ready event.
	// Used by SupplyMonitor to notify coordinators when tasks are ready for assignment.
	PublishTasksBecameReady(event TasksBecameReadyEvent)

	// PublishTransportRequested publishes a transport request event.
	// Used by siphon workers to request transport assignment.
	PublishTransportRequested(event TransportRequestedEvent)

	// PublishTransferCompleted publishes a transfer completion event.
	// Used by transport workers to notify when cargo transfer is done.
	PublishTransferCompleted(event TransferCompletedEvent)
}

// ShipEventSubscriber subscribes to ship state change events.
// Used by containers to wait for ship arrivals without polling.
type ShipEventSubscriber interface {
	// SubscribeArrived subscribes to ARRIVED events for a specific ship.
	// Returns a channel that receives events. Caller must Unsubscribe when done.
	SubscribeArrived(shipSymbol string) <-chan ShipArrivedEvent

	// UnsubscribeArrived removes a subscription. Closes the channel.
	UnsubscribeArrived(shipSymbol string, ch <-chan ShipArrivedEvent)

	// SubscribeWorkerCompleted subscribes to worker completion events for a coordinator.
	// coordinatorID is the container ID of the coordinator waiting for worker completions.
	SubscribeWorkerCompleted(coordinatorID string) <-chan WorkerCompletedEvent

	// UnsubscribeWorkerCompleted removes a worker completion subscription.
	UnsubscribeWorkerCompleted(coordinatorID string, ch <-chan WorkerCompletedEvent)

	// SubscribeTasksBecameReady subscribes to task ready events for a player.
	SubscribeTasksBecameReady(playerID int) <-chan TasksBecameReadyEvent

	// UnsubscribeTasksBecameReady removes a task ready subscription.
	UnsubscribeTasksBecameReady(playerID int, ch <-chan TasksBecameReadyEvent)

	// SubscribeTransportRequested subscribes to transport request events for a player.
	SubscribeTransportRequested(playerID int) <-chan TransportRequestedEvent

	// UnsubscribeTransportRequested removes a transport request subscription.
	UnsubscribeTransportRequested(playerID int, ch <-chan TransportRequestedEvent)

	// SubscribeTransferCompleted subscribes to transfer completion events for a player.
	SubscribeTransferCompleted(playerID int) <-chan TransferCompletedEvent

	// UnsubscribeTransferCompleted removes a transfer completion subscription.
	UnsubscribeTransferCompleted(playerID int, ch <-chan TransferCompletedEvent)
}

// ShipArrivedEvent is published when a ship transitions from IN_TRANSIT to IN_ORBIT
type ShipArrivedEvent struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
	Location   string
	Status     NavStatus
}

// DTOs for ship operations

type ShipData struct {
	Symbol             string
	Location           string
	NavStatus          string
	FlightMode         string // CRUISE, DRIFT, BURN, or STEALTH
	ArrivalTime        string // ISO8601 timestamp when IN_TRANSIT (e.g., "2024-01-01T12:00:00Z"), empty otherwise
	CooldownExpiration string // ISO8601 timestamp when cooldown expires (e.g., "2024-01-01T12:00:00Z"), empty if no cooldown
	FuelCurrent        int
	FuelCapacity       int
	CargoCapacity      int
	CargoUnits         int
	EngineSpeed        int
	FrameSymbol        string       // Frame type (e.g., "FRAME_PROBE", "FRAME_DRONE", "FRAME_MINER")
	ModuleSlots        int          // Frame's total module slot capacity - fixed for the life of the hull
	MountingPoints     int          // Frame's total mounting point capacity - fixed for the life of the hull
	Role               string       // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	Modules            []ModuleData // Installed ship modules (jump drives, mining equipment, etc.)
	Mounts             []MountData  // Installed ship mounts (mining lasers, gas siphons, sensor arrays, etc.)
	// Reactor* fields describe the hull's fixed power budget. Reactors have no
	// swap/upgrade endpoint in the SpaceTraders API - ReactorPowerOutput is
	// permanent for the life of the ship (sp-el60).
	ReactorSymbol       string
	ReactorName         string
	ReactorPowerOutput  int
	ReactorRequirements RequirementsData
	CrewCurrent         int
	CrewRequired        int
	CrewCapacity        int
	Cargo               *CargoData
}

type ModuleData struct {
	Symbol       string
	Capacity     int
	Range        int
	Requirements RequirementsData
}

// MountData represents an installed mount (mining lasers, gas siphons,
// sensor arrays, weapons, etc.).
type MountData struct {
	Symbol       string
	Name         string
	Strength     int
	Deposits     []string
	Requirements RequirementsData
}

// RequirementsData captures the power/crew/slot cost declared by a module,
// mount, or reactor (SpaceTraders API schema: ShipRequirements).
type RequirementsData struct {
	Power int
	Crew  int
	Slots int
}

type CargoData struct {
	Capacity  int
	Units     int
	Inventory []shared.CargoItem
}

type Result struct {
	Destination    string
	ArrivalTime    int    // Calculated seconds
	ArrivalTimeStr string // ISO8601 timestamp from API (e.g., "2024-01-01T12:00:00Z")
	FuelConsumed   int
	FlightMode     string // Flight mode used for this navigation
	// Fuel state from API response (avoids separate GetShip call)
	FuelCurrent  int
	FuelCapacity int
}

type RefuelResult struct {
	FuelAdded   int
	CreditsCost int
	// Fuel state from API response (avoids separate GetShip call)
	FuelCurrent  int
	FuelCapacity int
	// AgentCredits is the agent's credit balance as reported in-band by the
	// refuel response (data.agent.credits). Nil if the response omitted it.
	// It is the authoritative post-transaction balance for the ledger.
	AgentCredits *int
}
