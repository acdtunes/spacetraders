package persistence

import (
	"time"
)

// PlayerModel represents the players table
// NOTE: Credits are NOT persisted in database - they're always fetched fresh from API
type PlayerModel struct {
	ID int `gorm:"column:id;primaryKey;autoIncrement"`
	// AgentSymbol is intentionally NOT unique: the same agent symbol may be
	// re-registered in a later universe era after a reset, producing
	// multiple player rows that share a symbol (see migration 032).
	AgentSymbol string     `gorm:"column:agent_symbol;index:idx_players_agent_symbol;not null"`
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
	EraID          *int    `gorm:"column:era_id"`
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
	HeartbeatAt       *time.Time   `gorm:"column:heartbeat_at"` // Workers update this to prove they're alive
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

// ShipModel represents the ships table
// This stores complete ship state that is the source of truth after daemon startup
type ShipModel struct {
	// Primary key fields
	ShipSymbol string       `gorm:"column:ship_symbol;primaryKey;not null"`
	PlayerID   int          `gorm:"column:player_id;primaryKey;not null"`
	Player     *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	// Navigation state
	NavStatus   string     `gorm:"column:nav_status;default:'DOCKED'"`
	FlightMode  string     `gorm:"column:flight_mode;default:'CRUISE'"`
	ArrivalTime *time.Time `gorm:"column:arrival_time"`

	// Nav route origin + departure (sp-vp9k): where an IN_TRANSIT ship departed
	// from and when, carried from the API nav.route so DB consumers can compute
	// exact transit progress instead of approximating from poll timing. Empty/zero
	// and NULL respectively when the ship is not in transit. Additive columns with
	// no constraints (mirroring location_symbol/x/y + arrival_time), so AutoMigrate
	// adds them and no CHECK/enum drift gate is involved. Backed by migration 040.
	OriginSymbol  string     `gorm:"column:origin_symbol"`
	OriginX       float64    `gorm:"column:origin_x;default:0"`
	OriginY       float64    `gorm:"column:origin_y;default:0"`
	DepartureTime *time.Time `gorm:"column:departure_time"`

	// Location (denormalized for quick reconstruction)
	LocationSymbol string  `gorm:"column:location_symbol"`
	LocationX      float64 `gorm:"column:location_x;default:0"`
	LocationY      float64 `gorm:"column:location_y;default:0"`
	SystemSymbol   string  `gorm:"column:system_symbol"`

	// Fuel
	FuelCurrent  int `gorm:"column:fuel_current;default:0"`
	FuelCapacity int `gorm:"column:fuel_capacity;default:0"`

	// Cargo (JSONB for full item details)
	CargoCapacity  int    `gorm:"column:cargo_capacity;default:0"`
	CargoUnits     int    `gorm:"column:cargo_units;default:0"`
	CargoInventory string `gorm:"column:cargo_inventory;type:jsonb;default:'[]'"`

	// Ship specifications
	EngineSpeed int    `gorm:"column:engine_speed;default:0"`
	FrameSymbol string `gorm:"column:frame_symbol"`
	Role        string `gorm:"column:role"`
	Modules     string `gorm:"column:modules;type:jsonb;default:'[]'"`

	// Cooldown
	CooldownExpiration *time.Time `gorm:"column:cooldown_expiration"`

	// Assignment (existing)
	ContainerID      *string         `gorm:"column:container_id"` // Pointer to support NULL for idle ships
	Container        *ContainerModel `gorm:"foreignKey:ContainerID,PlayerID;references:ID,PlayerID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	AssignmentStatus string          `gorm:"column:assignment_status;default:'idle'"`
	AssignedAt       *time.Time      `gorm:"column:assigned_at"`
	ReleasedAt       *time.Time      `gorm:"column:released_at"`
	ReleaseReason    string          `gorm:"column:release_reason"`

	// Assignment owner (sp-i1ku): distinguishes a coordinator container claim
	// from a captain reservation. "container" (default) or "captain".
	AssignmentOwner  string `gorm:"column:assignment_owner;default:'container'"`
	AssignmentReason string `gorm:"column:assignment_reason"`

	// DedicatedFleet (sp-snmb): permanent, operator-configured reservation for
	// a specific coordinator (e.g. "contract"). Empty means unreserved. Unlike
	// AssignmentOwner/ContainerID above, this is independent of any transient
	// container claim - it is a standing claim-filter, not a work assignment.
	DedicatedFleet string `gorm:"column:dedicated_fleet;default:''"`

	// ReservationOverrides (sp-1vhv): per-hull cargo do-not-sell override set,
	// stored as a JSON object of good->bool. true force-reserves a good the default
	// would sell; false force-allows a default-reserved module's sale (deliberate
	// resale). A good absent from the object follows the code-level MODULE_*/MOUNT_*
	// classification. Like DedicatedFleet above, this is a standing per-hull tag
	// independent of any container assignment, so it must be preserved across the
	// restart-time API sync (which has no concept of it) or a reservation is
	// silently wiped and a staged module is re-exposed to coordinator liquidation.
	ReservationOverrides string `gorm:"column:reservation_overrides;type:jsonb;default:'{}'"`

	// Power/slot/crew data (sp-el60). Reactor and frame-slot fields are fixed
	// for the life of the hull - reactors/frames have no swap endpoint in the
	// SpaceTraders API. Flattened into columns (not JSON) to mirror the
	// existing single-value fields above (FuelCurrent/EngineSpeed/FrameSymbol
	// etc.); Mounts is a JSON list like Modules since it's a collection.
	// Additive columns only - no CHECK constraints on this model, so
	// AutoMigrate creates them with no manual migration required.
	ReactorSymbol            string `gorm:"column:reactor_symbol"`
	ReactorName              string `gorm:"column:reactor_name"`
	ReactorPowerOutput       int    `gorm:"column:reactor_power_output;default:0"`
	ReactorRequirementsPower int    `gorm:"column:reactor_requirements_power;default:0"`
	ReactorRequirementsCrew  int    `gorm:"column:reactor_requirements_crew;default:0"`
	ReactorRequirementsSlots int    `gorm:"column:reactor_requirements_slots;default:0"`
	ModuleSlots              int    `gorm:"column:module_slots;default:0"`
	MountingPoints           int    `gorm:"column:mounting_points;default:0"`
	Mounts                   string `gorm:"column:mounts;type:jsonb;default:'[]'"`
	CrewCurrent              int    `gorm:"column:crew_current;default:0"`
	CrewRequired             int    `gorm:"column:crew_required;default:0"`
	CrewCapacity             int    `gorm:"column:crew_capacity;default:0"`

	// Sync metadata
	SyncedAt time.Time `gorm:"column:synced_at;autoCreateTime"`
	Version  int       `gorm:"column:version;default:1"`
}

func (ShipModel) TableName() string {
	return "ships"
}

// CargoItemJSON is a JSON helper type for cargo inventory items
type CargoItemJSON struct {
	Symbol      string `json:"symbol"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Units       int    `json:"units"`
}

// ModuleJSON is a JSON helper type for ship modules
type ModuleJSON struct {
	Symbol       string           `json:"symbol"`
	Capacity     int              `json:"capacity"`
	Range        int              `json:"range"`
	Requirements RequirementsJSON `json:"requirements"`
}

// MountJSON is a JSON helper type for installed ship mounts (mining lasers,
// gas siphons, sensor arrays, weapons, etc.) - sp-el60.
type MountJSON struct {
	Symbol       string           `json:"symbol"`
	Name         string           `json:"name"`
	Strength     int              `json:"strength"`
	Deposits     []string         `json:"deposits"`
	Requirements RequirementsJSON `json:"requirements"`
}

// RequirementsJSON is a JSON helper type for the power/crew/slot cost
// declared by a module or mount (SpaceTraders API schema: ShipRequirements) -
// sp-el60.
type RequirementsJSON struct {
	Power int `json:"power"`
	Crew  int `json:"crew"`
	Slots int `json:"slots"`
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
	// basis of deposited stock (C1, sp-64je). It is managed OUT-OF-BAND from the
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

// TransactionModel represents the transactions table
type TransactionModel struct {
	ID                string       `gorm:"column:id;primaryKey;size:36;not null"`
	PlayerID          int          `gorm:"column:player_id;index:idx_player_timestamp;not null"`
	Player            *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Timestamp         time.Time    `gorm:"column:timestamp;index:idx_player_timestamp;not null"`
	TransactionType   string       `gorm:"column:transaction_type;index:idx_type;size:50;not null"`
	Category          string       `gorm:"column:category;size:50;not null"`
	Amount            int          `gorm:"column:amount;not null"` // Positive for income, negative for expenses
	BalanceBefore     int          `gorm:"column:balance_before;not null"`
	BalanceAfter      int          `gorm:"column:balance_after;not null"`
	Description       string       `gorm:"column:description;type:text"`
	Metadata          string       `gorm:"column:metadata;type:jsonb"`                           // JSON metadata
	RelatedEntityType string       `gorm:"column:related_entity_type;index:idx_related;size:50"` // e.g., "contract", "factory"
	RelatedEntityID   string       `gorm:"column:related_entity_id;index:idx_related;size:100"`  // ID of related entity
	OperationType     string       `gorm:"column:operation_type;size:50"`                        // e.g., "contract", "arbitrage", "rebalancing", "factory"
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
	RecordedAt     time.Time    `gorm:"column:recorded_at;not null;default:CURRENT_TIMESTAMP;index:idx_market_history_waypoint_good_time,idx_market_history_good_time,idx_market_history_recorded_at"`
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
	CreatedAt      time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	CompletedAt    *time.Time `gorm:"column:completed_at"`

	// Construction-specific fields (only used when PipelineType == CONSTRUCTION)
	ConstructionSite *string `gorm:"column:construction_site;size:64;index:idx_pipelines_construction_site"`
	Materials        string  `gorm:"column:materials;type:jsonb;default:'[]'"`
	SupplyChainDepth int     `gorm:"column:supply_chain_depth;default:0"`
	MaxWorkers       int     `gorm:"column:max_workers;default:5"`
	MinSupply        string  `gorm:"column:min_supply;size:20;default:''"`
	GoodOverrides    string  `gorm:"column:good_overrides;type:text;default:''"` // sp-sdyo: per-good buy-gating overrides (JSON), persisted for restart-resilience (RULINGS #2)
}

func (ManufacturingPipelineModel) TableName() string {
	return "manufacturing_pipelines"
}

// ManufacturingTaskModel represents the manufacturing_tasks table
type ManufacturingTaskModel struct {
	ID                 string     `gorm:"column:id;primaryKey;size:64"`
	PipelineID         *string    `gorm:"column:pipeline_id;size:64;index:idx_tasks_pipeline"` // Nullable for ad-hoc tasks
	PlayerID           int        `gorm:"column:player_id;not null;index:idx_tasks_player_status"`
	TaskType           string     `gorm:"column:task_type;size:32;not null"`
	Status             string     `gorm:"column:status;size:32;not null;index:idx_tasks_status,idx_tasks_player_status"`
	Good               string     `gorm:"column:good;size:64;not null"`
	Quantity           int        `gorm:"column:quantity;default:0"`
	ActualQuantity     int        `gorm:"column:actual_quantity;default:0"`
	SourceMarket       *string    `gorm:"column:source_market;size:64"`
	TargetMarket       *string    `gorm:"column:target_market;size:64"`
	FactorySymbol      *string    `gorm:"column:factory_symbol;size:64"`
	StorageOperationID *string    `gorm:"column:storage_operation_id;size:64;index:idx_tasks_storage_operation"` // For STORAGE_ACQUIRE_DELIVER tasks
	StorageWaypoint    *string    `gorm:"column:storage_waypoint;size:64"`                                       // For STORAGE_ACQUIRE_DELIVER tasks
	ConstructionSite   *string    `gorm:"column:construction_site;size:64"`                                      // For DELIVER_TO_CONSTRUCTION tasks
	AssignedShip       *string    `gorm:"column:assigned_ship;size:64;index:idx_tasks_ship"`
	Priority           int        `gorm:"column:priority;default:0"`
	RetryCount         int        `gorm:"column:retry_count;default:0"`
	MaxRetries         int        `gorm:"column:max_retries;default:3"`
	TotalCost          int        `gorm:"column:total_cost;default:0"`
	TotalRevenue       int        `gorm:"column:total_revenue;default:0"`
	ErrorMessage       *string    `gorm:"column:error_message;type:text"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	ReadyAt            *time.Time `gorm:"column:ready_at"`
	StartedAt          *time.Time `gorm:"column:started_at"`
	CompletedAt        *time.Time `gorm:"column:completed_at"`
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
	ID                 int        `gorm:"column:id;primaryKey;autoIncrement"`
	FactorySymbol      string     `gorm:"column:factory_symbol;size:64;not null;uniqueIndex:idx_factory_unique,priority:1"`
	OutputGood         string     `gorm:"column:output_good;size:64;not null;uniqueIndex:idx_factory_unique,priority:2"`
	PlayerID           int        `gorm:"column:player_id;not null;index:idx_factory_player"`
	PipelineID         string     `gorm:"column:pipeline_id;size:64;index:idx_factory_pipeline;uniqueIndex:idx_factory_unique,priority:3"`
	RequiredInputs     string     `gorm:"column:required_inputs;type:jsonb;not null"`
	DeliveredInputs    string     `gorm:"column:delivered_inputs;type:jsonb;default:'{}'"`
	AllInputsDelivered bool       `gorm:"column:all_inputs_delivered;default:false"`
	CurrentSupply      *string    `gorm:"column:current_supply;size:32"`
	PreviousSupply     *string    `gorm:"column:previous_supply;size:32"`
	ReadyForCollection bool       `gorm:"column:ready_for_collection;default:false"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	InputsCompletedAt  *time.Time `gorm:"column:inputs_completed_at"`
	ReadyAt            *time.Time `gorm:"column:ready_at"`
}

func (ManufacturingFactoryStateModel) TableName() string {
	return "manufacturing_factory_states"
}

// CaptainEventModel represents the captain_events strategic-event outbox
type CaptainEventModel struct {
	ID          int64        `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID    int          `gorm:"column:player_id;index:idx_captain_events_player;not null"`
	Player      *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Type        string       `gorm:"column:type;size:50;not null"`
	Ship        string       `gorm:"column:ship;size:100;not null;default:''"`
	Payload     string       `gorm:"column:payload;type:jsonb"`
	CreatedAt   time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	ProcessedAt *time.Time   `gorm:"column:processed_at"`
}

func (CaptainEventModel) TableName() string {
	return "captain_events"
}

type EraModel struct {
	EraID             int        `gorm:"column:era_id;primaryKey;autoIncrement"`
	Name              string     `gorm:"column:name;unique;not null"`
	AgentSymbol       string     `gorm:"column:agent_symbol;not null"`
	Faction           *string    `gorm:"column:faction"`
	PlayerID          int        `gorm:"column:player_id;not null"`
	UniverseResetDate *time.Time `gorm:"column:universe_reset_date;type:date"`
	RegisteredAt      *time.Time `gorm:"column:registered_at"`
	ClosedAt          *time.Time `gorm:"column:closed_at"`
	FinalCredits      *int64     `gorm:"column:final_credits"`
	Notes             *string    `gorm:"column:notes"`
}

func (EraModel) TableName() string {
	return "eras"
}

// SpendReservationModel is one in-flight factory-input spend intent, the shared-state
// substrate of the cross-container concurrent spend cap (sp-w3he). Each factory
// container INSERTs a row before an input buy and the ledger checks that live treasury
// minus the SUM of all active rows stays at/above the working-capital reserve — closing
// the check->buy race the per-buy floor (sp-9aoc) leaves open when N factories buy at once.
// Rows are deleted after each buy (success or failure) and swept on staleness.
//
// Deliberately NO players foreign key / association: these are ephemeral operational rows
// (a row lives only for the seconds of one buy dispatch, then is deleted), so referential
// integrity buys nothing and a hard FK would only add fixture friction. player_id is a
// plain indexed column — the ledger scopes its SUM to it; created_at is indexed for the
// staleness sweep.
type SpendReservationModel struct {
	ID            string    `gorm:"column:id;primaryKey;not null"`
	PlayerID      int       `gorm:"column:player_id;not null;index:idx_spend_reservations_player"`
	ContainerID   string    `gorm:"column:container_id;not null"`
	ProjectedCost int       `gorm:"column:projected_cost;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;index:idx_spend_reservations_created"`
}

func (SpendReservationModel) TableName() string {
	return "factory_spend_reservations"
}

// GateEdgeModel is one directed cross-system jump-gate connection — the persisted
// substrate of the gate-graph adjacency store (sp-7gr2). travel()'s multi-jump BFS
// and the routability-check-before-spend guard both read this table instead of the
// broken single-edge assumption that crashed a laden frigate at the home gate
// (KA42→JP61 is 3 jumps: PA3→UQ16→JP61, not one). GateWaypoint carries the
// CONNECTED system's own gate waypoint (the raw API connection symbol), so an
// uncharted neighbor can be expanded without first charting its system graph.
//
// EraID + SyncedAt mirror WaypointModel exactly: reads are era-scoped
// (eraScopePredicate) so dead-era rows (sp-vapw) never leak into live routing, and
// SyncedAt (RFC3339) drives the lazy 24h refresh. The (system_symbol,
// connected_system) pair is the primary key — a system's whole edge set is
// REPLACED on each sync (delete-then-insert), so a since-severed connection cannot
// linger and a re-sync also purges any dead-era row for that system.
//
// MARKER ROWS (sp-ikx1): a row whose ConnectedSystem is "" is NOT an edge — it is the
// persisted negative-result backoff marker for an UNREADABLE system (a frontier gate
// whose live fetch 400s, "no ship present"). At most one per (system, era). Its
// UnreadableSince/AttemptCount carry the backoff state; its "" connected_system is the
// structural sentinel that distinguishes it from a real edge (ExtractSystemSymbol never
// yields "", so an edge's ConnectedSystem is always non-empty). Edges/Adjacency EXCLUDE
// marker rows (connected_system <> ”); UnreadableState/MarkUnreadable read/write them.
type GateEdgeModel struct {
	SystemSymbol    string `gorm:"column:system_symbol;primaryKey"`
	ConnectedSystem string `gorm:"column:connected_system;primaryKey"`
	GateWaypoint    string `gorm:"column:gate_waypoint;not null"`
	EraID           *int   `gorm:"column:era_id;index:idx_gate_edges_era"`
	SyncedAt        string `gorm:"column:synced_at"` // ISO timestamp string
	// UnderConstruction records whether the CONNECTED system's own jump gate was
	// still being built at sync time (sp-8qhu). The routing BFS never traverses an
	// under-construction edge, and such an edge refreshes on a SHORTER TTL than a
	// healthy one so a completed build is noticed within the same era.
	UnderConstruction bool `gorm:"column:under_construction;not null;default:false"`
	// UnreadableSince is the RFC3339 timestamp of the LAST failed live gate probe, set
	// only on a marker row (connected_system = ""). Empty on every real edge row. With
	// AttemptCount it is the persisted negative-result cache (sp-ikx1): an unreadable
	// gate is not re-probed every 30s tick — the service backs it off 5m→30m→2h.
	// Persisted, not in-memory, so a restart resumes the backoff (RULINGS #2).
	UnreadableSince string `gorm:"column:unreadable_since"`
	// AttemptCount is the consecutive-failed-probe count on a marker row; it drives the
	// backoff schedule. 0 on every real edge row.
	AttemptCount int `gorm:"column:attempt_count;not null;default:0"`
}

func (GateEdgeModel) TableName() string {
	return "gate_edges"
}

// TourLegTelemetryModel is one planned-vs-realized record for a single trade at a
// single leg of a multi-hop trade tour (sp-1ek0 P1b). The tour_run executor writes
// one row per executed (or explicitly skipped) trade: the planner's projection
// (PlannedUnits/PlannedUnitPrice, PlannedAt) alongside what the market actually gave
// (RealizedUnits/RealizedUnitPrice, RealizedAt). These rows feed the graduation-gate
// report (median |planned−realized|/planned price error — the gate metric that proves
// the model, not just profit) and future model recalibration.
//
// Follows the SpendReservationModel idiom: NO players foreign key. player_id is a
// plain indexed column the report scopes its reads to; tour_id (the container id)
// groups a tour's legs. Rows are durable history (unlike the ephemeral spend
// reservations) but referential integrity to players buys nothing here and a hard FK
// would only add fixture friction to the executor tests that write these rows.
type TourLegTelemetryModel struct {
	ID                uint      `gorm:"column:id;primaryKey;autoIncrement"`
	TourID            string    `gorm:"column:tour_id;not null;index:idx_tour_leg_telemetry_tour"`
	ShipSymbol        string    `gorm:"column:ship_symbol;not null"`
	LegIndex          int       `gorm:"column:leg_index;not null"`
	Waypoint          string    `gorm:"column:waypoint;not null"`
	Good              string    `gorm:"column:good;not null"`
	IsBuy             bool      `gorm:"column:is_buy"`
	PlannedUnits      int       `gorm:"column:planned_units"`
	RealizedUnits     int       `gorm:"column:realized_units"`
	PlannedUnitPrice  int       `gorm:"column:planned_unit_price"`
	RealizedUnitPrice int       `gorm:"column:realized_unit_price"`
	PlannedAt         time.Time `gorm:"column:planned_at"`
	RealizedAt        time.Time `gorm:"column:realized_at"`
	PlayerID          int       `gorm:"column:player_id;not null;index:idx_tour_leg_telemetry_player"`
}

func (TourLegTelemetryModel) TableName() string {
	return "tour_leg_telemetry"
}

// ScoutPostModel is one desired-state scout post (sp-cxpq): a per-system
// market-freshness assignment the scout_post_coordinator keeps manned, the way
// the contract fleet coordinator keeps its dedicated fleet working. AssignedHull
// (nullable) is the satellite currently manning the post and TourContainerID the
// worker container scanning it — both persisted so a daemon restart re-adopts the
// same hull onto the same post (RULINGS #2). Kind is "standing" (infinite tour)
// or "sweep_once" (single tour, then auto-removed).
//
// EraID mirrors WaypointModel/GateEdgeModel exactly: reads are era-scoped so a
// universe reset never resurrects dead-era posts (sp-njpu). The unique index on
// (player_id, system_symbol) enforces one post per system per player; a re-add in
// a new era reuses the row (Upsert restamps era_id). No players foreign key —
// like the other operational-state rows (spend reservations, tour telemetry),
// player_id is a plain indexed column the reads scope to, and a hard FK would only
// add fixture friction to the coordinator tests that write these rows.
type ScoutPostModel struct {
	ID                     int     `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID               int     `gorm:"column:player_id;not null;uniqueIndex:idx_scout_posts_player_system,priority:1;index:idx_scout_posts_player"`
	SystemSymbol           string  `gorm:"column:system_symbol;not null;uniqueIndex:idx_scout_posts_player_system,priority:2"`
	FreshnessTargetSeconds int     `gorm:"column:freshness_target_seconds;not null"`
	Kind                   string  `gorm:"column:kind;not null"`
	AssignedHull           *string `gorm:"column:assigned_hull"`
	TourContainerID        *string `gorm:"column:tour_container_id"`
	// RepositionContainerID (sp-s232) is the in-flight cross-gate relay jump-routing
	// a satellite toward this post. Nullable — set only while a relay is airborne,
	// cleared when it lands (the next tick mans the post in-system) or dies. GORM
	// AutoMigrate adds the column in place; existing rows read it as NULL → "".
	RepositionContainerID *string `gorm:"column:reposition_container_id"`

	// Hulls is the probe budget N for a multi-probe post (sp-enry): the system is
	// toured by N probes over N disjoint market partitions. Defaults to 1 (single
	// hull, the pre-enry behavior). AutoMigrate adds the column with default 1, so
	// every existing post reads as single-hull. RULINGS #5: a DB value, not a const.
	Hulls int `gorm:"column:hulls;not null;default:1"`

	// PrimaryPartition is the JSON-encoded frozen market tour of the PRIMARY slot
	// when Hulls>1 (sp-enry). NULL/empty ⇒ the primary tours ALL markets (single-hull
	// behavior), so a single-hull row never carries one and stays byte-identical.
	// ExtraSlots is the JSON-encoded slots 1..N-1 (hull, tour/relay container, and
	// each slot's frozen partition). Persisting the partitions is what makes a daemon
	// restart re-adopt each probe onto the SAME partition without a mass re-tour
	// (RULINGS #2) — the reconciler re-partitions ONLY on a hull-budget change.
	PrimaryPartition *string `gorm:"column:primary_partition"`
	ExtraSlots       *string `gorm:"column:extra_slots"`

	// RespawnAttempts and RespawnParkedUntil back the general per-post respawn-loop cap
	// (sp-py4n): the consecutive dead-tour respawn count and the backoff-window deadline
	// the reconciler parks a persistently-crashing post under. AutoMigrate adds both in
	// place — respawn_attempts defaults 0 and reposition_parked_until is nullable, so
	// every existing row reads as "never capped, not parked". Persisting them is what
	// makes the cap survive a daemon restart rather than the crash-loop resuming at tick
	// cadence (RULINGS #2).
	RespawnAttempts    int        `gorm:"column:respawn_attempts;not null;default:0"`
	RespawnParkedUntil *time.Time `gorm:"column:respawn_parked_until"`

	EraID     *int      `gorm:"column:era_id;index:idx_scout_posts_era"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (ScoutPostModel) TableName() string {
	return "scout_posts"
}

// MarketAbsorptionLedgerModel is one outstanding claim on a market's depth — the
// shared-state substrate of the cross-engine absorption ledger (sp-78ai). Five
// engines (tours, arb-run, idle-arb, trade-route circuits, pre-positioning) all
// absorb the SAME (waypoint, good, side) depth with no cross-container signal but
// the market cache, which only reflects EXECUTED trades seconds later. This table
// carries the two invisible windows: PLANNED rows (in-flight intent — a leg
// dispatched but not yet landed, so the cache still quotes pre-absorption prices)
// and EXECUTED rows (the recovery shadow — depth a completed dump still occupies
// while it regrows on the model's fitted per-tier half-life). A reader nets the
// decayed outstanding against a market's depth so nobody, including the absorber's
// own next plan, steps into a hole the model says has not regrown (sp-lbbm was two
// hulls co-dumping the same bid, −80k; the lane mutex + flat hold are the tactical
// patch this ledger generalizes cross-engine).
//
// Deliberately NO players foreign key and NO era_id (the SpendReservationModel
// idiom, sp-w3he): these are ephemeral operational rows living minutes (a PLANNED
// leg) to hours (an EXECUTED shadow, hard-capped at 12h — trade-analyst Q2), so
// referential integrity buys nothing and an era reset kills the owning containers
// (PLANNED rows swept by dead-container reclaim) while EXECUTED rows age out on
// their hard cap and key on (waypoint, good) quotes that reset anyway. player_id +
// (waypoint_symbol, good_symbol, side) is the composite the outstanding query
// scopes to; container_id is indexed for dead-container reclaim and the arb
// container's convert-at-sale; expires_at is indexed for the read filter and sweep.
//
// TierAtWrite is the sink good's activity (WEAK/GROWING/STRONG/RESTRICTED) stamped
// at the EXECUTED write; readers resolve the recovery half-life from the fitted
// artifact. UNTAGGED sinks (empty activity) get NO EXECUTED shadow at all
// (trade-analyst Q2: the depth model cannot price what it has not fit — a shadow
// there is either wrong or effectively eternal). TrancheSize is the sink good's
// trade_volume at write, so a reader can size the 50%-of-a-tranche recovery floor
// without a live market lookup. QuotedPrice is telemetry only.
type MarketAbsorptionLedgerModel struct {
	ID          string `gorm:"column:id;primaryKey;not null"`
	PlayerID    int    `gorm:"column:player_id;not null;index:idx_absorption_player_key,priority:1"`
	ContainerID string `gorm:"column:container_id;not null;index:idx_absorption_container"`
	Engine      string `gorm:"column:engine;not null"` // tour | arb | idle-arb — telemetry + reclaim attribution
	Waypoint    string `gorm:"column:waypoint_symbol;not null;index:idx_absorption_player_key,priority:2"`
	Good        string `gorm:"column:good_symbol;not null;index:idx_absorption_player_key,priority:3"`
	Side        string `gorm:"column:side;not null;index:idx_absorption_player_key,priority:4"` // sell | buy
	State       string `gorm:"column:state;not null"`                                           // PLANNED | EXECUTED
	Units       int    `gorm:"column:units;not null"`                                           // planned absorption / realized absorbed units
	TrancheSize int    `gorm:"column:tranche_size;not null;default:0"`                          // sink trade_volume at write (recovery-floor sizing)
	TierAtWrite string `gorm:"column:tier_at_write;not null;default:''"`                        // activity tier; readers resolve half-life from the artifact
	QuotedPrice int    `gorm:"column:quoted_price;not null;default:0"`                          // telemetry only
	// CreatedAt is set on the PLANNED insert; ExecutedAt is stamped when a leg's
	// sale converts the row to EXECUTED (nil while PLANNED). ExpiresAt is the
	// lifecycle bound the sweep and the read filter both use: a PLANNED row's
	// per-plan TTL (2× projected flight + slack) or an EXECUTED row's 12h hard cap.
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
	ExecutedAt *time.Time `gorm:"column:executed_at"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null;index:idx_absorption_expires"`
}

func (MarketAbsorptionLedgerModel) TableName() string {
	return "market_absorption_ledger"
}

// ContractDepotModel represents the contract_depots table (bead sp-u9xa): one
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

// WarehouseWithdrawalModel represents the warehouse_withdrawals table (sp-kqxe):
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

// WarehouseStockingModel represents the warehouse_stockings table (sp-j6uz): one row per
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

// ShipyardInventoryModel is one scanned shipyard listing fact (sp-42ow): at
// last_scanned, the (player, waypoint) shipyard offered ship_type at
// purchase_price with the listing's supply tier. Written by the scout tour's
// piggybacked shipyard scan (ReplaceScan swaps a waypoint's whole row set —
// the market_data delete-then-insert idiom — so re-scans refresh price and
// last_scanned without duplicate rows, and a delisted type disappears). Read
// by the reachable-yard ranking that feeds the fleet autosizer's heavy-hull
// yard-price signal.
//
// EraID mirrors GateEdgeModel/ScoutPostModel: reads are era-scoped
// (eraScopePredicate) so a universe reset never leaks dead-era yards into a
// live buy signal; ReplaceScan purges the waypoint's rows across ALL eras
// before inserting, so dead-era rows self-clean on re-scan. Composite primary
// key (player_id, waypoint_symbol, ship_type) makes duplicates structurally
// impossible. PurchasePrice 0 = type listed but unpriced at scan time (proves
// availability, never feeds a price guard). No players foreign key — like the
// other operational-state rows, player_id is a plain scoped column. Unlike
// most cache tables this one IS CREATE'd by migration 041, so the column-drift
// gate holds its model and migration in lockstep.
type ShipyardInventoryModel struct {
	PlayerID       int       `gorm:"column:player_id;primaryKey"`
	SystemSymbol   string    `gorm:"column:system_symbol;not null;index:idx_shipyard_inventory_system"`
	WaypointSymbol string    `gorm:"column:waypoint_symbol;primaryKey"`
	ShipType       string    `gorm:"column:ship_type;primaryKey"`
	PurchasePrice  int       `gorm:"column:purchase_price;not null;default:0"`
	Supply         string    `gorm:"column:supply;not null;default:''"`
	LastScanned    time.Time `gorm:"column:last_scanned;not null"`
	EraID          *int      `gorm:"column:era_id;index:idx_shipyard_inventory_era"`
}

func (ShipyardInventoryModel) TableName() string {
	return "shipyard_inventory"
}

// AllModels is the single canonical registry of every persisted model struct.
// AutoMigrate and any test/tooling that needs the full model set must consume
// this slice instead of maintaining a parallel hand-written list, so newly
// added *Model structs cannot silently skip migration.
func AllModels() []any {
	return []any{
		&PlayerModel{},
		&WaypointModel{},
		&ContainerModel{},
		&ContainerLogModel{},
		&ShipModel{},
		&SystemGraphModel{},
		&MarketData{},
		&ContractModel{},
		&GasOperationModel{},
		&StorageOperationModel{},
		&GoodsFactoryModel{},
		&TransactionModel{},
		&MarketPriceHistoryModel{},
		&CaptainEventModel{},
		&ManufacturingPipelineModel{},
		&ManufacturingTaskModel{},
		&ManufacturingTaskDependencyModel{},
		&ManufacturingFactoryStateModel{},
		&EraModel{},
		&SpendReservationModel{},
		&GateEdgeModel{},
		&TourLegTelemetryModel{},
		&ScoutPostModel{},
		&MarketAbsorptionLedgerModel{},
		&ContractDepotModel{},
		&WarehouseWithdrawalModel{},
		&WarehouseStockingModel{},
		&ShipyardInventoryModel{},
	}
}
