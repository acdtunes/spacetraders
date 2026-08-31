package persistence

import "time"

// UnreadableHullModel is one open episode of a hull the API will not serialise, and the
// bounds the automatic repair is held to. It is persisted because the attempt bound is
// what stops a doomed repair spending credits forever, and a bound that resets on daemon
// restart is not a bound (RULINGS #2). The row is deleted the moment the hull reads again.
type UnreadableHullModel struct {
	PlayerID      int        `gorm:"column:player_id;primaryKey"`
	ShipSymbol    string     `gorm:"column:ship_symbol;primaryKey;size:50"`
	FirstSeenAt   time.Time  `gorm:"column:first_seen_at;not null"`
	LastSeenAt    time.Time  `gorm:"column:last_seen_at;not null"`
	Attempts      int        `gorm:"column:attempts;not null;default:0"`
	NextAttemptAt time.Time  `gorm:"column:next_attempt_at;not null"`
	EscalatedAt   *time.Time `gorm:"column:escalated_at"`
	LastOutcome   string     `gorm:"column:last_outcome;size:64"`
	LastReason    string     `gorm:"column:last_reason;type:text"`
}

// TableName maps the model to its table.
func (UnreadableHullModel) TableName() string {
	return "unreadable_hulls"
}
