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

	// Player operations
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)

	// Waypoint operations
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error)

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
	TransferCargo(ctx context.Context, fromShipSymbol, toShipSymbol, goodSymbol string, units int, token string) (*TransferResult, error)

	// Mining operations
	ExtractResources(ctx context.Context, shipSymbol string, token string) (*ExtractionResult, error)

	// Market operations
	GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*MarketData, error)

	// Shipyard operations
	GetShipyard(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ShipyardData, error)
	PurchaseShip(ctx context.Context, shipType, waypointSymbol, token string) (*ShipPurchaseResult, error)
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
}

type SellResult struct {
	TotalRevenue int
	UnitsSold    int
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

// TransferResult contains the result of transferring cargo between ships
type TransferResult struct {
	FromShip         string
	ToShip           string
	GoodSymbol       string
	UnitsTransferred int
	RemainingCargo   *navigation.CargoData // Remaining cargo on source ship
}

// Market DTOs
type MarketData struct {
	Symbol     string
	TradeGoods []TradeGoodData
}

type TradeGoodData struct {
	Symbol        string
	Supply        string
	SellPrice     int
	PurchasePrice int
	TradeVolume   int
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
	Frame         map[string]interface{}
	Reactor       map[string]interface{}
	Engine        map[string]interface{}
	Modules       []map[string]interface{}
	Mounts        []map[string]interface{}
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
