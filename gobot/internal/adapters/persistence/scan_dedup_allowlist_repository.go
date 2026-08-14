package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ScanDedupAllowlistGORM is the durable, live-settable store behind the
// scan-dedup A/B test's ship-symbol allowlist: IsArmed for the trade-route
// handler, Add/Remove/List for the CLI/gRPC verb (the daemon is the sole
// writer, reached via gRPC, never a CLI-side direct write).
type ScanDedupAllowlistGORM struct {
	db *gorm.DB
}

// NewScanDedupAllowlistGORM creates a GORM-backed scan-dedup allowlist store.
func NewScanDedupAllowlistGORM(db *gorm.DB) *ScanDedupAllowlistGORM {
	return &ScanDedupAllowlistGORM{db: db}
}

// IsArmed reports whether shipSymbol is on the live allowlist for playerID. A
// read error is an error, never a silent false — the caller treats it as
// "not armed" (fail-closed).
func (r *ScanDedupAllowlistGORM) IsArmed(ctx context.Context, playerID int, shipSymbol string) (bool, error) {
	if shipSymbol == "" {
		return false, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&ScanDedupAllowlistModel{}).
		Where("player_id = ? AND ship_symbol = ?", playerID, shipSymbol).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("read scan-dedup allowlist for %s: %w", shipSymbol, err)
	}
	return count > 0, nil
}

// Add arms shipSymbol for playerID, idempotently. changed reports whether a
// new row was actually added.
func (r *ScanDedupAllowlistGORM) Add(ctx context.Context, playerID int, shipSymbol string) (changed bool, err error) {
	if shipSymbol == "" {
		return false, fmt.Errorf("ship symbol is required")
	}
	armed, err := r.IsArmed(ctx, playerID, shipSymbol)
	if err != nil {
		return false, err
	}
	if armed {
		return false, nil
	}
	row := &ScanDedupAllowlistModel{ShipSymbol: shipSymbol, PlayerID: playerID, CreatedAt: time.Now()}
	// OnConflict guards the race between the IsArmed check above and this insert.
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return false, fmt.Errorf("add %s to scan-dedup allowlist: %w", shipSymbol, err)
	}
	return true, nil
}

// Remove disarms shipSymbol for playerID, idempotently. changed reports
// whether a row was actually deleted.
func (r *ScanDedupAllowlistGORM) Remove(ctx context.Context, playerID int, shipSymbol string) (changed bool, err error) {
	if shipSymbol == "" {
		return false, fmt.Errorf("ship symbol is required")
	}
	tx := r.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, shipSymbol).
		Delete(&ScanDedupAllowlistModel{})
	if tx.Error != nil {
		return false, fmt.Errorf("remove %s from scan-dedup allowlist: %w", shipSymbol, tx.Error)
	}
	return tx.RowsAffected > 0, nil
}

// List returns every ship symbol currently armed for playerID, sorted for a
// stable, greppable CLI report.
func (r *ScanDedupAllowlistGORM) List(ctx context.Context, playerID int) ([]string, error) {
	var rows []ScanDedupAllowlistModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list scan-dedup allowlist: %w", err)
	}
	ships := make([]string, 0, len(rows))
	for _, row := range rows {
		ships = append(ships, row.ShipSymbol)
	}
	sort.Strings(ships)
	return ships, nil
}
