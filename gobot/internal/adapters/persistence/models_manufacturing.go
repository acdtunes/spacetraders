package persistence

import (
	"time"
)

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
	GoodOverrides    string  `gorm:"column:good_overrides;type:text;default:''"` // Per-good buy-gating overrides (JSON), persisted for restart-resilience (RULINGS #2)
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
	// Phase tracking fields for daemon restart resilience
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
