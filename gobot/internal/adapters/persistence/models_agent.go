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
