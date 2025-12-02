package persistence

import (
	"time"
)

// PlayerModel represents the players table
// NOTE: Credits are NOT persisted in database - they're always fetched fresh from API
type PlayerModel struct {
	ID          int        `gorm:"column:id;primaryKey;autoIncrement"`
	AgentSymbol string     `gorm:"column:agent_symbol;unique;not null"`
	Token       string     `gorm:"column:token;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null"`
	LastActive  *time.Time `gorm:"column:last_active"`
	Metadata    string     `gorm:"column:metadata;type:jsonb"` // JSON stored as string
}

func (PlayerModel) TableName() string {
	return "players"
}

// WaypointModel represents the waypoints table
type WaypointModel struct {
	WaypointSymbol string  `gorm:"column:waypoint_symbol;primaryKey"`
	SystemSymbol   string  `gorm:"column:system_symbol;not null"`
	Type           string  `gorm:"column:type;not null"`
	X              float64 `gorm:"column:x;not null"`
	Y              float64 `gorm:"column:y;not null"`
	Traits         string  `gorm:"column:traits;type:text"`            // JSON array as text
	HasFuel        int     `gorm:"column:has_fuel;not null;default:0"` // 0 or 1 (SQLite compatible)
	Orbitals       string  `gorm:"column:orbitals;type:text"`          // JSON array as text
	SyncedAt       string  `gorm:"column:synced_at"`                   // ISO timestamp string
}

func (WaypointModel) TableName() string {
	return "waypoints"
}

// ContainerModel represents the containers table
type ContainerModel struct {
	ID                string       `gorm:"column:id;primaryKey;not null"`
	PlayerID          int          `gorm:"column:player_id;primaryKey;not null;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Player            *PlayerModel `gorm:"foreignKey:PlayerID;references:ID"`
	ContainerType     string       `gorm:"column:container_type"`
	CommandType       string       `gorm:"column:command_type"`
	Status            string       `gorm:"column:status"`
	ParentContainerID *string      `gorm:"column:parent_container_id;index:idx_containers_parent_player"` // ID of parent coordinator (NULL for root containers)
	RestartPolicy     string       `gorm:"column:restart_policy"`
	RestartCount      int          `gorm:"column:restart_count;default:0"`
	Config            string       `gorm:"column:config;type:text"` // JSON as text
	StartedAt         *time.Time   `gorm:"column:started_at"`
	StoppedAt         *time.Time   `gorm:"column:stopped_at"`
	ExitCode          *int         `gorm:"column:exit_code"`
	ExitReason        string       `gorm:"column:exit_reason"`
}

func (ContainerModel) TableName() string {
	return "containers"
}

// ContainerLogModel represents the container_logs table
type ContainerLogModel struct {
	ID          int             `gorm:"column:id;primaryKey;autoIncrement"`
	ContainerID string          `gorm:"column:container_id;not null"`
	PlayerID    int             `gorm:"column:player_id;not null"`
	Container   *ContainerModel `gorm:"foreignKey:ContainerID,PlayerID;references:ID,PlayerID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Timestamp   time.Time       `gorm:"column:timestamp;not null"`
	Level       string          `gorm:"column:level;not null;default:'INFO'"`
	Message     string          `gorm:"column:message;type:text;not null"`
	Metadata    *string         `gorm:"column:metadata;type:jsonb"` // JSON metadata (JSONB for PostgreSQL, TEXT for SQLite) - pointer for NULL support
}

func (ContainerLogModel) TableName() string {
	return "container_logs"
}

// ShipModel represents the ships table (renamed from ship_assignments)
// This stores ship assignment state that is merged with API ship data
type ShipModel struct {
	ShipSymbol       string          `gorm:"column:ship_symbol;primaryKey;not null"`
	PlayerID         int             `gorm:"column:player_id;primaryKey;not null"`
	Player           *PlayerModel    `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	ContainerID      *string         `gorm:"column:container_id"` // Pointer to support NULL for idle ships
	Container        *ContainerModel `gorm:"foreignKey:ContainerID,PlayerID;references:ID,PlayerID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	AssignmentStatus string          `gorm:"column:assignment_status;default:'idle'"` // Renamed from status
	AssignedAt       *time.Time      `gorm:"column:assigned_at"`
	ReleasedAt       *time.Time      `gorm:"column:released_at"`
	ReleaseReason    string          `gorm:"column:release_reason"`
}

func (ShipModel) TableName() string {
	return "ships"
}

// SystemGraphModel represents the system_graphs table
type SystemGraphModel struct {
	SystemSymbol string    `gorm:"column:system_symbol;primaryKey"`
	GraphData    string    `gorm:"column:graph_data;type:jsonb;not null"` // Use JSONB for PostgreSQL, falls back to TEXT for SQLite
	CreatedAt    time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (SystemGraphModel) TableName() string {
	return "system_graphs"
}

// MarketData represents the market_data table
// Database schema: one row per (waypoint, good) combination
// Primary key is composite: (waypoint_symbol, good_symbol)
type MarketData struct {
	WaypointSymbol string       `gorm:"primaryKey;size:255;not null"`
	GoodSymbol     string       `gorm:"primaryKey;size:100;not null"`
	Supply         *string      `gorm:"size:50"`
	Activity       *string      `gorm:"size:50"`
	PurchasePrice  int          `gorm:"not null"`
	SellPrice      int          `gorm:"not null"`
	TradeVolume    int          `gorm:"not null"`
	TradeType      *string      `gorm:"size:32"` // EXPORT, IMPORT, or EXCHANGE
	LastUpdated    time.Time    `gorm:"index;not null"`
	PlayerID       int          `gorm:"index;not null"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

func (MarketData) TableName() string {
	return "market_data"
}

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
	OperationType  string       `gorm:"column:operation_type;not null"`     // GAS_SIPHON, MINING, CUSTOM
	Status         string       `gorm:"column:status;default:'PENDING'"`    // PENDING, RUNNING, COMPLETED, STOPPED, FAILED
	ExtractorShips string       `gorm:"column:extractor_ships;type:text"`   // JSON array
	StorageShips   string       `gorm:"column:storage_ships;type:text"`     // JSON array
	SupportedGoods string       `gorm:"column:supported_goods;type:text"`   // JSON array
	LastError      string       `gorm:"column:last_error;type:text"`
	CreatedAt      time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt      time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
	StartedAt      *time.Time   `gorm:"column:started_at"`
	StoppedAt      *time.Time   `gorm:"column:stopped_at"`
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
	Metadata         string       `gorm:"column:metadata;type:jsonb"`           // JSON metadata
	QuantityAcquired int          `gorm:"column:quantity_acquired;default:0"`   // Set on completion
	TotalCost        int          `gorm:"column:total_cost;default:0"`          // Set on completion
	ShipsUsed        int          `gorm:"column:ships_used;default:0"`          // Number of ships utilized
	MarketQueries    int          `gorm:"column:market_queries;default:0"`      // Number of market queries
	ParallelLevels   int          `gorm:"column:parallel_levels;default:0"`     // Number of parallel levels
	EstimatedSpeedup float64      `gorm:"column:estimated_speedup;default:0"`   // Estimated speedup factor
	CreatedAt        time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt        time.Time    `gorm:"column:updated_at;not null;autoUpdateTime"`
	StartedAt        *time.Time   `gorm:"column:started_at"`
	CompletedAt      *time.Time   `gorm:"column:completed_at"`
}

func (GoodsFactoryModel) TableName() string {
	return "goods_factories"
}

// TransactionModel represents the transactions table
type TransactionModel struct {
	ID                string       `gorm:"column:id;primaryKey;size:36;not null"`
	PlayerID          int          `gorm:"column:player_id;index:idx_player_timestamp;not null"`
	Player            *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Timestamp         time.Time    `gorm:"column:timestamp;index:idx_player_timestamp;not null"`
	TransactionType   string       `gorm:"column:transaction_type;index:idx_type;size:50;not null"`
	Category          string       `gorm:"column:category;index:idx_category;size:50;not null"`
	Amount            int          `gorm:"column:amount;not null"` // Positive for income, negative for expenses
	BalanceBefore     int          `gorm:"column:balance_before;not null"`
	BalanceAfter      int          `gorm:"column:balance_after;not null"`
	Description       string       `gorm:"column:description;type:text"`
	Metadata          string       `gorm:"column:metadata;type:jsonb"`                               // JSON metadata
	RelatedEntityType string       `gorm:"column:related_entity_type;index:idx_related;size:50"`    // e.g., "contract", "factory"
	RelatedEntityID   string       `gorm:"column:related_entity_id;index:idx_related;size:100"`     // ID of related entity
	OperationType     string       `gorm:"column:operation_type;size:50"`                            // e.g., "contract", "arbitrage", "rebalancing", "factory"
	CreatedAt         time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
}

func (TransactionModel) TableName() string {
	return "transactions"
}

// MarketPriceHistoryModel represents the market_price_history table
type MarketPriceHistoryModel struct {
	ID             int          `gorm:"column:id;primaryKey;autoIncrement"`
	WaypointSymbol string       `gorm:"column:waypoint_symbol;size:50;not null;index:idx_market_history_waypoint_good_time"`
	GoodSymbol     string       `gorm:"column:good_symbol;size:100;not null;index:idx_market_history_waypoint_good_time,idx_market_history_good_time"`
	PlayerID       int          `gorm:"column:player_id;not null;index:idx_market_history_player"`
	Player         *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	PurchasePrice  int          `gorm:"column:purchase_price;not null"`
	SellPrice      int          `gorm:"column:sell_price;not null"`
	Supply         *string      `gorm:"column:supply;size:20"`
	Activity       *string      `gorm:"column:activity;size:20"`
	TradeVolume    int          `gorm:"column:trade_volume;not null"`
	RecordedAt     time.Time    `gorm:"column:recorded_at;not null;default:now();index:idx_market_history_waypoint_good_time,idx_market_history_good_time,idx_market_history_recorded_at"`
}

func (MarketPriceHistoryModel) TableName() string {
	return "market_price_history"
}

// ManufacturingPipelineModel represents the manufacturing_pipelines table
type ManufacturingPipelineModel struct {
	ID             string     `gorm:"column:id;primaryKey;size:64"`
	SequenceNumber int        `gorm:"column:sequence_number;not null;default:0"`
	PipelineType   string     `gorm:"column:pipeline_type;size:20;not null;default:'FABRICATION';index:idx_pipelines_type"`
	PlayerID       int        `gorm:"column:player_id;not null;index:idx_pipelines_player"`
	ProductGood    string     `gorm:"column:product_good;size:64;not null"`
	SellMarket     string     `gorm:"column:sell_market;size:64;not null"`
	ExpectedPrice  int        `gorm:"column:expected_price;not null"`
	Status         string     `gorm:"column:status;size:32;not null;index:idx_pipelines_status"`
	TotalCost      int        `gorm:"column:total_cost;default:0"`
	TotalRevenue   int        `gorm:"column:total_revenue;default:0"`
	NetProfit      int        `gorm:"column:net_profit;default:0"`
	ErrorMessage   *string    `gorm:"column:error_message;type:text"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;default:now()"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	CompletedAt    *time.Time `gorm:"column:completed_at"`
}

func (ManufacturingPipelineModel) TableName() string {
	return "manufacturing_pipelines"
}

// ManufacturingTaskModel represents the manufacturing_tasks table
type ManufacturingTaskModel struct {
	ID             string     `gorm:"column:id;primaryKey;size:64"`
	PipelineID     *string    `gorm:"column:pipeline_id;size:64;index:idx_tasks_pipeline"` // Nullable for ad-hoc tasks
	PlayerID       int        `gorm:"column:player_id;not null;index:idx_tasks_player_status"`
	TaskType       string     `gorm:"column:task_type;size:32;not null"`
	Status         string     `gorm:"column:status;size:32;not null;index:idx_tasks_status,idx_tasks_player_status"`
	Good           string     `gorm:"column:good;size:64;not null"`
	Quantity       int        `gorm:"column:quantity;default:0"`
	ActualQuantity int        `gorm:"column:actual_quantity;default:0"`
	SourceMarket       *string `gorm:"column:source_market;size:64"`
	TargetMarket       *string `gorm:"column:target_market;size:64"`
	FactorySymbol      *string `gorm:"column:factory_symbol;size:64"`
	StorageOperationID *string `gorm:"column:storage_operation_id;size:64;index:idx_tasks_storage_operation"` // For STORAGE_ACQUIRE_DELIVER tasks
	StorageWaypoint    *string `gorm:"column:storage_waypoint;size:64"`                                       // For STORAGE_ACQUIRE_DELIVER tasks
	AssignedShip       *string `gorm:"column:assigned_ship;size:64;index:idx_tasks_ship"`
	Priority       int        `gorm:"column:priority;default:0"`
	RetryCount     int        `gorm:"column:retry_count;default:0"`
	MaxRetries     int        `gorm:"column:max_retries;default:3"`
	TotalCost      int        `gorm:"column:total_cost;default:0"`
	TotalRevenue   int        `gorm:"column:total_revenue;default:0"`
	ErrorMessage   *string    `gorm:"column:error_message;type:text"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;default:now()"`
	ReadyAt        *time.Time `gorm:"column:ready_at"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	CompletedAt    *time.Time `gorm:"column:completed_at"`
	// BUG FIX #3: Phase tracking fields for daemon restart resilience
	CollectPhaseCompleted bool       `gorm:"column:collect_phase_completed;default:false"`
	AcquirePhaseCompleted bool       `gorm:"column:acquire_phase_completed;default:false"`
	PhaseCompletedAt      *time.Time `gorm:"column:phase_completed_at"`
}

func (ManufacturingTaskModel) TableName() string {
	return "manufacturing_tasks"
}

// ManufacturingTaskDependencyModel represents the manufacturing_task_dependencies table
type ManufacturingTaskDependencyModel struct {
	TaskID      string `gorm:"column:task_id;primaryKey;size:64"`
	DependsOnID string `gorm:"column:depends_on_id;primaryKey;size:64"`
}

func (ManufacturingTaskDependencyModel) TableName() string {
	return "manufacturing_task_dependencies"
}

// ManufacturingFactoryStateModel represents the manufacturing_factory_states table
type ManufacturingFactoryStateModel struct {
	ID                  int        `gorm:"column:id;primaryKey;autoIncrement"`
	FactorySymbol       string     `gorm:"column:factory_symbol;size:64;not null;uniqueIndex:idx_factory_unique,priority:1"`
	OutputGood          string     `gorm:"column:output_good;size:64;not null;uniqueIndex:idx_factory_unique,priority:2"`
	PlayerID            int        `gorm:"column:player_id;not null;index:idx_factory_player"`
	PipelineID          string     `gorm:"column:pipeline_id;size:64;index:idx_factory_pipeline;uniqueIndex:idx_factory_unique,priority:3"`
	RequiredInputs      string     `gorm:"column:required_inputs;type:jsonb;not null"`
	DeliveredInputs     string     `gorm:"column:delivered_inputs;type:jsonb;default:'{}'"`
	AllInputsDelivered  bool       `gorm:"column:all_inputs_delivered;default:false"`
	CurrentSupply       *string    `gorm:"column:current_supply;size:32"`
	PreviousSupply      *string    `gorm:"column:previous_supply;size:32"`
	ReadyForCollection  bool       `gorm:"column:ready_for_collection;default:false"`
	CreatedAt           time.Time  `gorm:"column:created_at;not null;default:now()"`
	InputsCompletedAt   *time.Time `gorm:"column:inputs_completed_at"`
	ReadyAt             *time.Time `gorm:"column:ready_at"`
}

func (ManufacturingFactoryStateModel) TableName() string {
	return "manufacturing_factory_states"
}
