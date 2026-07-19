package ports

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// APIClient defines the domain's interface for interacting with the SpaceTraders API.
//
// This interface is defined in the domain layer (not infrastructure) to follow
// the Dependency Inversion Principle and hexagonal architecture:
//
//	┌─────────────────────────┐
//	│  Application Layer      │
//	│  (commands/queries)     │
//	└───────────┬─────────────┘
//	            │ depends on
//	            ↓
//	┌─────────────────────────┐
//	│  Domain Ports           │  ← This interface
//	│  (interfaces)           │
//	└───────────┬─────────────┘
//	            ↑
//	            │ implements
//	┌─────────────────────────┐
//	│  Infrastructure Layer   │
//	│  (adapters)             │
//	└─────────────────────────┘
//
// The infrastructure layer provides concrete implementations (adapters) that
// implement this interface, allowing the application and domain to remain
// independent of infrastructure details.
type APIClient interface {
	// Ship operations
	GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error)
	ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error)
	NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error)
	OrbitShip(ctx context.Context, symbol, token string) error
	DockShip(ctx context.Context, symbol, token string) error
	RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error)
	SetFlightMode(ctx context.Context, symbol, flightMode, token string) error
	// JumpShip executes a jump through a jump gate. waypointSymbol must be
	// the destination JUMP_GATE waypoint (not a bare system symbol) - the
	// live SpaceTraders API requires "waypointSymbol" in the request body.
	JumpShip(ctx context.Context, shipSymbol, waypointSymbol, token string) (*JumpResult, error)
	// WarpShip warps a ship OFF the jump-gate network to a destination waypoint
	// in another system. Requires a MODULE_WARP_DRIVE_I; fuel is
	// consumed by inter-system distance. Mirrors NavigateShip's fuel + arrival
	// return envelope.
	WarpShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error)

	// Jump gate operations
	GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*JumpGateData, error)

	// Player operations
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)

	// Waypoint operations
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error)
	// GetWaypoint reads a single waypoint's detail, including whether it is still
	// UNDER CONSTRUCTION. The gate graph uses this to learn a jump gate's build
	// state (an unbuilt gate must never be routed through): the API's
	// jump-gate connections list carries symbols only, so construction is resolved
	// with a per-gate waypoint read.
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*WaypointDetail, error)

	// Contract operations
	NegotiateContract(ctx context.Context, shipSymbol, token string) (*ContractNegotiationResult, error)
	GetContract(ctx context.Context, contractID, token string) (*ContractData, error)
	AcceptContract(ctx context.Context, contractID, token string) (*ContractData, error)
	DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*ContractData, error)
	FulfillContract(ctx context.Context, contractID, token string) (*ContractData, error)

	// Cargo operations
	PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*PurchaseResult, error)
	SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*SellResult, error)
	JettisonCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) error

	// Ship outfitting operations (modules). Install requires the module to be
	// in the ship's cargo; remove places the module back into cargo. Both
	// return the ship's post-modification cargo capacity and the shipyard fee.
	InstallShipModule(ctx context.Context, shipSymbol, moduleSymbol, token string) (*ModuleModificationResult, error)
	RemoveShipModule(ctx context.Context, shipSymbol, moduleSymbol, token string) (*ModuleModificationResult, error)
	GetShipModules(ctx context.Context, shipSymbol, token string) ([]ModuleInfo, error)
	TransferCargo(ctx context.Context, fromShipSymbol, toShipSymbol, goodSymbol string, units int, token string) (*TransferResult, error)

	// Mining operations
	ExtractResources(ctx context.Context, shipSymbol string, token string) (*ExtractionResult, error)

	// Gas siphoning operations
	SiphonResources(ctx context.Context, shipSymbol string, token string) (*SiphonResult, error)

	// Market operations
	GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*MarketData, error)

	// Shipyard operations
	GetShipyard(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ShipyardData, error)
	PurchaseShip(ctx context.Context, shipType, waypointSymbol, token string) (*ShipPurchaseResult, error)

	// Construction operations
	GetConstruction(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ConstructionData, error)
	SupplyConstruction(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, token string) (*ConstructionSupplyResponse, error)
}

// Contract DTOs
type ContractNegotiationResult struct {
	Contract           *ContractData
	ErrorCode          int    // For error 4511 handling
	ExistingContractID string // Extracted from error response
}

type ContractData struct {
	ID            string
	FactionSymbol string
	Type          string
	Terms         ContractTermsData
	Accepted      bool
	Fulfilled     bool
	// AgentCredits is the agent's credit balance as reported in-band by an
	// accept/fulfill response (data.agent.credits). Nil for reads or responses
	// that omit it. It is the authoritative post-transaction balance for the
	// ledger, replacing the stale pre-fetched GetAgent snapshot.
	AgentCredits *int
}

type ContractTermsData struct {
	DeadlineToAccept string
	Deadline         string
	Payment          PaymentData
	Deliveries       []DeliveryData
}

type PaymentData struct {
	OnAccepted  int
	OnFulfilled int
}

type DeliveryData struct {
	TradeSymbol       string
	DestinationSymbol string
	UnitsRequired     int
	UnitsFulfilled    int
}

// Cargo DTOs
type PurchaseResult struct {
	TotalCost  int
	UnitsAdded int
	// AgentCredits is the agent's credit balance as reported in-band by the
	// purchase response (data.agent.credits). Nil if the response omitted it.
	// It is the authoritative post-transaction balance for the ledger.
	AgentCredits *int
}

type SellResult struct {
	TotalRevenue int
	UnitsSold    int
	// AgentCredits is the agent's credit balance as reported in-band by the
	// sell response (data.agent.credits). Nil if the response omitted it.
	// It is the authoritative post-transaction balance for the ledger.
	AgentCredits *int
}

// ModuleInfo describes a single ship module (installed or in a modules list).
type ModuleInfo struct {
	Symbol string
	Name   string
	// Capacity is the bonus the module grants (e.g. cargo hold size). 0 for
	// modules that grant no capacity.
	Capacity int
	Range    int
	// Power, Crew, and Slots are the module's own install requirements —
	// what installing it draws from the ship's reactor power budget, crew
	// capacity, and module-slot budget respectively.
	Power int
	Crew  int
	Slots int
}

// ModuleModificationResult is the outcome of installing or removing a ship
// module. Both endpoints return the updated agent, the ship's post-modification
// modules list and cargo, and a transaction carrying the modification fee.
type ModuleModificationResult struct {
	// Fee is the shipyard modification fee charged (transaction.totalPrice).
	Fee int
	// CargoCapacity is the ship's cargo hold capacity AFTER the modification —
	// the load-bearing field for a CARGO_HOLD upgrade.
	CargoCapacity int
	// Modules is the ship's full installed-module list after the modification.
	Modules []ModuleInfo
	// AgentCredits is the agent's authoritative post-transaction balance
	// (data.agent.credits). Nil if the response omitted it.
	AgentCredits *int
}

// ExtractionResult contains the result of extracting resources from an asteroid
type ExtractionResult struct {
	ShipSymbol      string
	YieldSymbol     string
	YieldUnits      int
	CooldownSeconds int
	CooldownExpires string // ISO8601 timestamp
	Cargo           *navigation.CargoData
}

// SiphonResult contains the result of siphoning gas from a gas giant
type SiphonResult struct {
	ShipSymbol      string
	YieldSymbol     string
	YieldUnits      int
	CooldownSeconds int
	CooldownExpires string // ISO8601 timestamp
	Cargo           *navigation.CargoData
}

// TransferResult contains the result of transferring cargo between ships
type TransferResult struct {
	FromShip         string
	ToShip           string
	GoodSymbol       string
	UnitsTransferred int
	RemainingCargo   *navigation.CargoData // Remaining cargo on source ship
}

// Jump DTOs
type JumpResult struct {
	DestinationSystem   string
	DestinationWaypoint string
	CooldownSeconds     int
	TotalPrice          int
}

type JumpGateData struct {
	Symbol      string
	Connections []string
}

// WaypointDetail is the slice of a single waypoint's detail the gate graph needs:
// its symbol and whether it is still under construction. An unbuilt jump gate
// (IsUnderConstruction true) is a dead edge the multi-jump BFS must not traverse
// — a jump INTO it fails at hop time (API error 4262).
type WaypointDetail struct {
	Symbol              string
	IsUnderConstruction bool
}

// Market DTOs
type MarketData struct {
	Symbol     string
	TradeGoods []TradeGoodData
}

type TradeGoodData struct {
	Symbol        string
	Supply        string
	Activity      string
	SellPrice     int
	PurchasePrice int
	TradeVolume   int
	TradeType     string // EXPORT, IMPORT, or EXCHANGE
}

// Shipyard DTOs
type ShipyardData struct {
	Symbol          string
	ShipTypes       []ShipTypeInfo
	Ships           []ShipListingData
	Transactions    []map[string]interface{}
	ModificationFee int
}

type ShipTypeInfo struct {
	Type string
}

type ShipListingData struct {
	Type          string
	Name          string
	Description   string
	PurchasePrice int
	// Supply is the listing's supply tier (e.g. HIGH/MODERATE/LOW) as reported
	// by the shipyard API — persisted by the shipyard-inventory scan.
	Supply  string
	Frame   map[string]interface{}
	Reactor map[string]interface{}
	Engine  map[string]interface{}
	Modules []map[string]interface{}
	Mounts  []map[string]interface{}
}

type ShipPurchaseResult struct {
	Agent       *player.AgentData
	Ship        *navigation.ShipData
	Transaction *ShipPurchaseTransaction
}

type ShipPurchaseTransaction struct {
	WaypointSymbol string
	ShipSymbol     string
	ShipType       string
	Price          int
	AgentSymbol    string
	Timestamp      string
}

// Construction DTOs
type ConstructionData struct {
	Symbol     string
	Materials  []ConstructionMaterialData
	IsComplete bool
}

type ConstructionMaterialData struct {
	TradeSymbol string
	Required    int
	Fulfilled   int
}

type ConstructionSupplyResponse struct {
	Construction *ConstructionData
	Cargo        *navigation.CargoData
}
