package persistence

import (
	"time"
)

// PlayerModel represents the players table
// NOTE: Credits are NOT persisted in database - they're always fetched fresh from API
type PlayerModel struct {
	PlayerID    int       `gorm:"column:player_id;primaryKey;autoIncrement"`
	AgentSymbol string    `gorm:"column:agent_symbol;unique;not null"`
	Token       string    `gorm:"column:token;not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null"`
	LastActive  *time.Time `gorm:"column:last_active"`
	Metadata    string    `gorm:"column:metadata;type:jsonb"` // JSON stored as string
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
	Traits         string  `gorm:"column:traits;type:text"` // JSON array as text
	HasFuel        int     `gorm:"column:has_fuel;not null;default:0"` // 0 or 1 (SQLite compatible)
	Orbitals       string  `gorm:"column:orbitals;type:text"` // JSON array as text
	SyncedAt       string  `gorm:"column:synced_at"` // ISO timestamp string
}

func (WaypointModel) TableName() string {
	return "waypoints"
}

// ContainerModel represents the containers table
type ContainerModel struct {
	ContainerID   string     `gorm:"column:container_id;primaryKey;not null"`
	PlayerID      int        `gorm:"column:player_id;primaryKey;not null"`
	ContainerType string     `gorm:"column:container_type"`
	CommandType   string     `gorm:"column:command_type"`
	Status        string     `gorm:"column:status"`
	RestartPolicy string     `gorm:"column:restart_policy"`
	RestartCount  int        `gorm:"column:restart_count;default:0"`
	Config        string     `gorm:"column:config;type:text"` // JSON as text
	StartedAt     *time.Time `gorm:"column:started_at"`
	StoppedAt     *time.Time `gorm:"column:stopped_at"`
	ExitCode      *int       `gorm:"column:exit_code"`
	ExitReason    string     `gorm:"column:exit_reason"`
}

func (ContainerModel) TableName() string {
	return "containers"
}

// ContainerLogModel represents the container_logs table
type ContainerLogModel struct {
	LogID       int       `gorm:"column:log_id;primaryKey;autoIncrement"`
	ContainerID string    `gorm:"column:container_id;not null"`
	PlayerID    int       `gorm:"column:player_id;not null"`
	Timestamp   time.Time `gorm:"column:timestamp;not null"`
	Level       string    `gorm:"column:level;not null;default:'INFO'"`
	Message     string    `gorm:"column:message;type:text;not null"`
}

func (ContainerLogModel) TableName() string {
	return "container_logs"
}

// ShipAssignmentModel represents the ship_assignments table
type ShipAssignmentModel struct {
	ShipSymbol    string     `gorm:"column:ship_symbol;primaryKey;not null"`
	PlayerID      int        `gorm:"column:player_id;primaryKey;not null"`
	ContainerID   string     `gorm:"column:container_id"`
	Operation     string     `gorm:"column:operation"`
	Status        string     `gorm:"column:status;default:'idle'"`
	AssignedAt    *time.Time `gorm:"column:assigned_at"`
	ReleasedAt    *time.Time `gorm:"column:released_at"`
	ReleaseReason string     `gorm:"column:release_reason"`
}

func (ShipAssignmentModel) TableName() string {
	return "ship_assignments"
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

// MarketDataModel represents the market_data table
type MarketDataModel struct {
	WaypointSymbol string `gorm:"column:waypoint_symbol;primaryKey;not null"`
	GoodSymbol     string `gorm:"column:good_symbol;primaryKey;not null"`
	Supply         string `gorm:"column:supply"`
	Activity       string `gorm:"column:activity"`
	PurchasePrice  int    `gorm:"column:purchase_price;not null"`
	SellPrice      int    `gorm:"column:sell_price;not null"`
	TradeVolume    int    `gorm:"column:trade_volume;not null"`
	LastUpdated    string `gorm:"column:last_updated;not null"` // ISO timestamp string
	PlayerID       int    `gorm:"column:player_id;not null"`
}

func (MarketDataModel) TableName() string {
	return "market_data"
}

// ContractModel represents the contracts table
type ContractModel struct {
	ContractID         string `gorm:"column:contract_id;primaryKey;not null"`
	PlayerID           int    `gorm:"column:player_id;primaryKey;not null"`
	FactionSymbol      string `gorm:"column:faction_symbol;not null"`
	Type               string `gorm:"column:type;not null"`
	Accepted           bool   `gorm:"column:accepted;not null"`
	Fulfilled          bool   `gorm:"column:fulfilled;not null"`
	DeadlineToAccept   string `gorm:"column:deadline_to_accept;not null"` // ISO timestamp
	Deadline           string `gorm:"column:deadline;not null"` // ISO timestamp
	PaymentOnAccepted  int    `gorm:"column:payment_on_accepted;not null"`
	PaymentOnFulfilled int    `gorm:"column:payment_on_fulfilled;not null"`
	DeliveriesJSON     string `gorm:"column:deliveries_json;type:text;not null"`
	LastUpdated        string `gorm:"column:last_updated;not null"` // ISO timestamp
}

func (ContractModel) TableName() string {
	return "contracts"
}
