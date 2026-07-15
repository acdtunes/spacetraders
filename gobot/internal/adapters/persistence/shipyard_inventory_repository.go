package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// ShipyardInventoryRepositoryGORM implements shipyard.InventoryRepository over
// GORM (sp-42ow) — the persisted shipyard-inventory store the scout tour's
// piggybacked shipyard scan writes and the reachable-yard ranking reads. Reads
// are era-scoped exactly like GormGateEdgeRepository (openEraID +
// eraScopePredicate) so dead-era yards never leak into a live buy signal; a
// waypoint's row set is REPLACED atomically on each scan (the market_data
// delete-then-insert idiom) so re-scans refresh price/last_scanned without
// duplicate rows and a delisted type disappears.
type ShipyardInventoryRepositoryGORM struct {
	db *gorm.DB
}

// NewShipyardInventoryRepository creates the GORM-backed shipyard inventory store.
func NewShipyardInventoryRepository(db *gorm.DB) *ShipyardInventoryRepositoryGORM {
	return &ShipyardInventoryRepositoryGORM{db: db}
}

// ReplaceScan atomically swaps the (player, waypoint) row set for the fresh
// scan result, stamped with the open era and scannedAt. The delete spans ALL
// eras (mirroring GateEdgeRepository.Replace) so a re-scan also purges any
// dead-era rows for the waypoint.
func (r *ShipyardInventoryRepositoryGORM) ReplaceScan(
	ctx context.Context,
	playerID int,
	systemSymbol, waypointSymbol string,
	availabilities []shipyard.ShipTypeAvailability,
	scannedAt time.Time,
) error {
	eraID := r.openEraID(ctx)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
			Delete(&ShipyardInventoryModel{}).Error; err != nil {
			return fmt.Errorf("failed to clear shipyard inventory for %s: %w", waypointSymbol, err)
		}
		if len(availabilities) == 0 {
			return nil
		}
		rows := make([]ShipyardInventoryModel, 0, len(availabilities))
		for _, a := range availabilities {
			rows = append(rows, ShipyardInventoryModel{
				PlayerID:       playerID,
				SystemSymbol:   systemSymbol,
				WaypointSymbol: waypointSymbol,
				ShipType:       a.ShipType,
				PurchasePrice:  a.PurchasePrice,
				Supply:         a.Supply,
				LastScanned:    scannedAt,
				EraID:          eraID,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("failed to insert shipyard inventory for %s: %w", waypointSymbol, err)
		}
		return nil
	})
}

// HasAnyOfTypes reports whether ANY era-scoped row for the player carries one
// of shipTypes — the "first heavy yard this era" milestone predicate.
func (r *ShipyardInventoryRepositoryGORM) HasAnyOfTypes(ctx context.Context, playerID int, shipTypes []string) (bool, error) {
	if len(shipTypes) == 0 {
		return false, nil
	}
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var count int64
	if err := r.db.WithContext(ctx).Model(&ShipyardInventoryModel{}).
		Where("player_id = ? AND ship_type IN ?", playerID, shipTypes).
		Where(predicate, args...).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to probe shipyard inventory for types: %w", err)
	}
	return count > 0, nil
}

// ListByTypes returns every era-scoped row for the player whose ship_type is
// in shipTypes, ordered deterministically (waypoint, ship_type) for stable
// downstream ranking.
func (r *ShipyardInventoryRepositoryGORM) ListByTypes(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error) {
	if len(shipTypes) == 0 {
		return nil, nil
	}
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []ShipyardInventoryModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND ship_type IN ?", playerID, shipTypes).
		Where(predicate, args...).
		Order("waypoint_symbol, ship_type").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list shipyard inventory by types: %w", err)
	}
	out := make([]shipyard.ShipTypeAvailability, 0, len(models))
	for _, m := range models {
		out = append(out, shipyard.ShipTypeAvailability{
			SystemSymbol:   m.SystemSymbol,
			WaypointSymbol: m.WaypointSymbol,
			ShipType:       m.ShipType,
			PurchasePrice:  m.PurchasePrice,
			Supply:         m.Supply,
			LastScanned:    m.LastScanned,
		})
	}
	return out, nil
}

// ScannedSystems returns the DISTINCT systems the player has a live-era shipyard
// scan for — the SCANNED set the sp-rhju backfill sweep excludes when enumerating
// the charted-but-unscanned blind spot. Era-SCOPED (mirroring the other reads): a
// scan booked in a DEAD era does not count as scanned in the open era, so a universe
// reset correctly re-backfills every shipyard this era (the scanned set is empty
// under the new era until re-scanned). A read error surfaces (fail closed) rather
// than a spuriously-empty scanned set that would re-declare every already-known yard.
func (r *ShipyardInventoryRepositoryGORM) ScannedSystems(ctx context.Context, playerID int) ([]string, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var systems []string
	if err := r.db.WithContext(ctx).Model(&ShipyardInventoryModel{}).
		Where("player_id = ?", playerID).
		Where(predicate, args...).
		Distinct().
		Pluck("system_symbol", &systems).Error; err != nil {
		return nil, fmt.Errorf("failed to list scanned shipyard systems: %w", err)
	}
	return systems, nil
}

// openEraID mirrors GormGateEdgeRepository.openEraID: the open era is the
// highest era_id with no closed_at. nil (no open era yet) scopes reads/writes
// to NULL era_id rows, matching the pre-close transition window.
func (r *ShipyardInventoryRepositoryGORM) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}
