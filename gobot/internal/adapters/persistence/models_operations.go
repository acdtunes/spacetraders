package persistence

import (
	"time"
)

// ContractModel represents the contracts table
type ContractModel struct {
	ID                 string       `gorm:"column:id;primaryKey;not null"`
	PlayerID           int          `gorm:"column:player_id;index;not null"`
	Player             *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	FactionSymbol      string       `gorm:"column:faction_symbol;not null"`
	Type               string       `gorm:"column:type;not null"`
	Accepted           bool         `gorm:"column:accepted;not null"`
	Fulfilled          bool         `gorm:"column:fulfilled;not null"`
	DeadlineToAccept   string       `gorm:"column:deadline_to_accept;not null"` // ISO timestamp
	Deadline           string       `gorm:"column:deadline;not null"`           // ISO timestamp
	PaymentOnAccepted  int          `gorm:"column:payment_on_accepted;not null"`
	PaymentOnFulfilled int          `gorm:"column:payment_on_fulfilled;not null"`
	DeliveriesJSON     string       `gorm:"column:deliveries_json;type:text;not null"`
	LastUpdated        string       `gorm:"column:last_updated;not null"` // ISO timestamp
}

func (ContractModel) TableName() string {
	return "contracts"
}

// GasOperationModel represents the gas_operations table
type GasOperationModel struct {
	ID             string       `gorm:"column:id;primaryKey;not null"`
	PlayerID       int          `gorm:"column:player_id;primaryKey;not null"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	GasGiant       string       `gorm:"column:gas_giant;not null"`
	Status         string       `gorm:"column:status;default:'PENDING'"`
	SiphonShips    string       `gorm:"column:siphon_ships;type:text"`    // JSON array
	TransportShips string       `gorm:"column:transport_ships;type:text"` // JSON array
	MaxIterations  int          `gorm:"column:max_iterations;default:-1"`
	LastError      string       `gorm:"column:last_error;type:text"`
	CreatedAt      time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt      time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
	StartedAt      *time.Time   `gorm:"column:started_at"`
	StoppedAt      *time.Time   `gorm:"column:stopped_at"`
}

func (GasOperationModel) TableName() string {
	return "gas_operations"
}

// StorageOperationModel represents the storage_operations table
// This is a generalized model for cargo storage operations (gas, mining, custom)
type StorageOperationModel struct {
	ID             string       `gorm:"column:id;primaryKey;not null"`
	PlayerID       int          `gorm:"column:player_id;primaryKey;not null"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	WaypointSymbol string       `gorm:"column:waypoint_symbol;not null"`
	OperationType  string       `gorm:"column:operation_type;not null"`   // GAS_SIPHON, MINING, CUSTOM
	Status         string       `gorm:"column:status;default:'PENDING'"`  // PENDING, RUNNING, COMPLETED, STOPPED, FAILED
	ExtractorShips string       `gorm:"column:extractor_ships;type:text"` // JSON array
	StorageShips   string       `gorm:"column:storage_ships;type:text"`   // JSON array
	SupportedGoods string       `gorm:"column:supported_goods;type:text"` // JSON array
	LastError      string       `gorm:"column:last_error;type:text"`
	CreatedAt      time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt      time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
	StartedAt      *time.Time   `gorm:"column:started_at"`
	StoppedAt      *time.Time   `gorm:"column:stopped_at"`
	// CostBasis is a JSON map[good]int of the per-good weighted-average unit cost
	// basis of deposited stock. It is managed OUT-OF-BAND from the
	// operation's domain fields by the CostBasisStore (a targeted column update),
	// so the full-row operation Update omits it — see StorageOperationRepository.
	CostBasis string `gorm:"column:cost_basis;type:text"`
}

func (StorageOperationModel) TableName() string {
	return "storage_operations"
}

// GoodsFactoryModel represents the goods_factories table
type GoodsFactoryModel struct {
	ID               string       `gorm:"column:id;primaryKey;not null"`
	PlayerID         int          `gorm:"column:player_id;index;not null"`
	Player           *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	TargetGood       string       `gorm:"column:target_good;not null"`
	SystemSymbol     string       `gorm:"column:system_symbol;not null"`
	DependencyTree   string       `gorm:"column:dependency_tree;type:text;not null"` // JSON-serialized SupplyChainNode
	Status           string       `gorm:"column:status;index;not null"`
	Metadata         string       `gorm:"column:metadata;type:jsonb"`         // JSON metadata
	QuantityAcquired int          `gorm:"column:quantity_acquired;default:0"` // Set on completion
	TotalCost        int          `gorm:"column:total_cost;default:0"`        // Set on completion
	ShipsUsed        int          `gorm:"column:ships_used;default:0"`        // Number of ships utilized
	MarketQueries    int          `gorm:"column:market_queries;default:0"`    // Number of market queries
	ParallelLevels   int          `gorm:"column:parallel_levels;default:0"`   // Number of parallel levels
	EstimatedSpeedup float64      `gorm:"column:estimated_speedup;default:0"` // Estimated speedup factor
	CreatedAt        time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt        time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
	StartedAt        *time.Time   `gorm:"column:started_at"`
	CompletedAt      *time.Time   `gorm:"column:completed_at"`
}

func (GoodsFactoryModel) TableName() string {
	return "goods_factories"
}

// ContractDepotModel represents the contract_depots table: one
// row per contract depot, scoped to a player by the composite (id, player_id)
// primary key exactly like gas_operations / storage_operations. The four element
// classes (destination warehouses, background stockers, pinned delivery hulls, source
// hubs) are each a JSON-encoded array of {Waypoint, ShipSymbol} — the same JSON-array
// idiom StorageOperationModel uses for its ship lists — so a whole depot topology
// is one durable row the restart-safe registry rebuild re-derives from. Born from
// AutoMigrate (no CREATE TABLE migration), like scout_posts.
type ContractDepotModel struct {
	ID            string       `gorm:"column:id;primaryKey;not null"`
	PlayerID      int          `gorm:"column:player_id;primaryKey;not null"`
	Player        *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Warehouses    string       `gorm:"column:warehouses;type:text"`     // JSON array of depot.Element (the routing anchor: >=1)
	Stockers      string       `gorm:"column:stockers;type:text"`       // JSON array of depot.Element
	DeliveryHulls string       `gorm:"column:delivery_hulls;type:text"` // JSON array of depot.Element
	SourceHubs    string       `gorm:"column:source_hubs;type:text"`    // JSON array of depot.Element
	CreatedAt     time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt     time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (ContractDepotModel) TableName() string {
	return "contract_depots"
}

// WarehouseWithdrawalModel represents the warehouse_withdrawals table:
// one row per warehouse→hauler buffer draw. A withdrawal is a NON-monetary cargo
// transfer (zero credits — the goods' basis is sunk at deposit), so it is its own
// economic event rather than a financial-ledger Transaction (a zero-amount
// Transaction violates the ledger's balance invariant). Downstream analysis reads
// this table to measure warehouse ROI (buffer hit-rate, served-from-buffer,
// contract-leg-avoided). Born from AutoMigrate (no CREATE TABLE migration), like
// tour_leg_telemetry.
type WarehouseWithdrawalModel struct {
	ID          uint      `gorm:"column:id;primaryKey;autoIncrement"`
	Good        string    `gorm:"column:good;not null;index:idx_warehouse_withdrawals_good"`
	Units       int       `gorm:"column:units;not null"`
	Waypoint    string    `gorm:"column:waypoint;not null"`
	ShipSymbol  string    `gorm:"column:ship_symbol;not null"`
	ContractID  string    `gorm:"column:contract_id;index:idx_warehouse_withdrawals_contract"` // "" when the draw serves no contract
	PlayerID    int       `gorm:"column:player_id;not null;index:idx_warehouse_withdrawals_player"`
	WithdrawnAt time.Time `gorm:"column:withdrawn_at;not null"`
}

func (WarehouseWithdrawalModel) TableName() string {
	return "warehouse_withdrawals"
}

// WarehouseStockingModel represents the warehouse_stockings table: one row per
// stocker→warehouse buffer DEPOSIT — the stock-IN mirror of WarehouseWithdrawalModel. A
// deposit is a NON-monetary cargo transfer (credits are booked at the buy, in the ledger's
// PURCHASE_CARGO row; the deposit moves credits nowhere), so — exactly like the withdrawal —
// it is its own economic event rather than a financial-ledger Transaction. Downstream
// analysis reads this table to measure depot stock-IN throughput (units-stocked), coverage
// (distinct goods per warehouse), and source-provenance, and — differenced against
// warehouse_withdrawals — an event-sourced view of current fill that does not depend on the
// (stale, for stationary depot hulls) ship cargo sync. Born from AutoMigrate (no CREATE TABLE
// migration), like warehouse_withdrawals and tour_leg_telemetry.
type WarehouseStockingModel struct {
	ID                uint      `gorm:"column:id;primaryKey;autoIncrement"`
	Good              string    `gorm:"column:good;not null;index:idx_warehouse_stockings_good"`
	Units             int       `gorm:"column:units;not null"`
	WarehouseWaypoint string    `gorm:"column:warehouse_waypoint;not null;index:idx_warehouse_stockings_warehouse"`
	SourceWaypoint    string    `gorm:"column:source_waypoint"` // "" when unknown (a resume deposit of prior-run cargo)
	ShipSymbol        string    `gorm:"column:ship_symbol;not null"`
	PlayerID          int       `gorm:"column:player_id;not null;index:idx_warehouse_stockings_player"`
	DepositedAt       time.Time `gorm:"column:deposited_at;not null"`
}

func (WarehouseStockingModel) TableName() string {
	return "warehouse_stockings"
}
