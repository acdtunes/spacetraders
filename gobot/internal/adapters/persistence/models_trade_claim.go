package persistence

import "time"

// TradeClaimModel mirrors migrations/059_add_trade_claims.up.sql.
type TradeClaimModel struct {
	PlayerID  int        `gorm:"column:player_id;primaryKey"`
	Hull      string     `gorm:"column:hull;primaryKey;size:50"`
	System    string     `gorm:"column:system;size:20;not null"`
	ClaimedAt time.Time  `gorm:"column:claimed_at;not null"`
	ArrivedAt *time.Time `gorm:"column:arrived_at"`
	EraID     *int       `gorm:"column:era_id"`
}

func (TradeClaimModel) TableName() string { return "trade_claims" }
