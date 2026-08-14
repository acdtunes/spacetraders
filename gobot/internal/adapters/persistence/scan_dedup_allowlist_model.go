package persistence

import "time"

// ScanDedupAllowlistModel is one ship's live enrollment in the scan-dedup A/B
// test: an armed ship lets the trade-route circuit's earning-class guards
// reuse a visit's own arrival scan instead of a second live GetMarket call.
// Absence (the default) is the disarmed state, byte-identical to before.
//
// Its own table rather than a per-ship tag column: an explicit allowlist, not
// a claim-filtering tag, so arming never pulls a hull out of
// trade_fleet_coordinator's claim pool. No players foreign key, mirroring the
// other operational-state rows.
type ScanDedupAllowlistModel struct {
	ShipSymbol string    `gorm:"column:ship_symbol;primaryKey;not null"`
	PlayerID   int       `gorm:"column:player_id;primaryKey;not null"`
	CreatedAt  time.Time `gorm:"column:created_at;not null"`
}

func (ScanDedupAllowlistModel) TableName() string {
	return "scan_dedup_allowlist"
}
