package persistence

import (
	"time"
)

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
	// ContractsGraduated is the durable, per-player, ERA-SCOPED manual contract-graduation flag
	// (sp-difa.1). The era row is one-per-player and a fresh era is a NEW row, so a new era/agent
	// reads the column default (false = UN-graduated) and contracts run as the funding floor. The
	// operator sets it (`contract graduate`) once trades earn enough; while SET, the boot-standing
	// bootstrap + capacity reconciler must NOT (re)start/maintain the contract-delivery op, DURABLY
	// across daemon restarts — the fix for the manual decommission being undone by a restart.
	// AutoMigrate adds the column with default false; existing rows read it as un-graduated.
	ContractsGraduated bool `gorm:"column:contracts_graduated;not null;default:false"`
}

func (EraModel) TableName() string {
	return "eras"
}

// SpendReservationModel is one in-flight factory-input spend intent, the shared-state
// substrate of the cross-container concurrent spend cap. Each factory
// container INSERTs a row before an input buy and the ledger checks that live treasury
// minus the SUM of all active rows stays at/above the working-capital reserve — closing
// the check->buy race the per-buy floor leaves open when N factories buy at once.
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

// PendingScalingReservationModel is the single active "a pending fleet-scaling purchase is
// capital-blocked" signal for a player. ONE row per player (PlayerID is the primary key).
// FRESHNESS, NOT EXPLICIT CLEANUP: see PendingScalingReservationRepository for how a stale
// UpdatedAt reads as absent instead of being deleted.
type PendingScalingReservationModel struct {
	PlayerID     int       `gorm:"column:player_id;primaryKey;not null"`
	TargetAmount int64     `gorm:"column:target_amount;not null"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null"`
}

func (PendingScalingReservationModel) TableName() string {
	return "pending_scaling_reservations"
}
