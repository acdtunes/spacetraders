package persistence

import "time"

// MVTTransitionModel mirrors migrations/060_add_mvt_transitions.up.sql.
type MVTTransitionModel struct {
	ID              uint      `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID        int       `gorm:"column:player_id;not null;index:idx_mvt_transitions_player_at,priority:1"`
	Hull            string    `gorm:"column:hull;size:50;not null"`
	FromState       string    `gorm:"column:from_state;size:8;not null"`
	ToState         string    `gorm:"column:to_state;size:8;not null"`
	System          string    `gorm:"column:system;size:20;not null"`
	YieldHere       float64   `gorm:"column:yield_here;not null;default:0"`
	BestAlternative float64   `gorm:"column:best_alternative;not null;default:0"`
	TravelCost      float64   `gorm:"column:travel_cost;not null;default:0"`
	Reason          string    `gorm:"column:reason;size:64;not null"`
	At              time.Time `gorm:"column:at;not null;index:idx_mvt_transitions_player_at,priority:2"`
}

func (MVTTransitionModel) TableName() string { return "mvt_transitions" }
