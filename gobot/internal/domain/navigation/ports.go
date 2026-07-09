package navigation

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

	// Sync methods (API -> Database)
	SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error)
	SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*Ship, error)

	// Background updater queries
	FindInTransitWithPastArrival(ctx context.Context) ([]*Ship, error)
	FindInTransitWithFutureArrival(ctx context.Context) ([]*Ship, error)
	FindWithExpiredCooldown(ctx context.Context) ([]*Ship, error)
	FindWithFutureCooldown(ctx context.Context) ([]*Ship, error)
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
	Role               string       // Ship role from registration (e.g., "EXCAVATOR", "COMMAND", "SATELLITE")
	Modules            []ModuleData // Installed ship modules (jump drives, mining equipment, etc.)
	Cargo              *CargoData
}

type ModuleData struct {
	Symbol   string
	Capacity int
	Range    int
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
