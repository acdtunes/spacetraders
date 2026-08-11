package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// ShipyardInventoryRepositoryGORM implements shipyard.InventoryRepository over
// GORM — the persisted shipyard-inventory store the scout tour's
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

// PriceSupplyMismatches is the SQL half of shipyard.PriceSupplyDisagree, era-agnostic and all writers.
func (r *ShipyardInventoryRepositoryGORM) PriceSupplyMismatches(ctx context.Context, playerID int) ([]shipyard.ShipTypeAvailability, error) {
	var models []ShipyardInventoryModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Where("(purchase_price > 0 AND (supply IS NULL OR TRIM(supply) = '')) OR (purchase_price <= 0 AND supply IS NOT NULL AND TRIM(supply) <> '')").
		Order("waypoint_symbol, ship_type").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to probe shipyard inventory for price/supply mismatches: %w", err)
	}
	return availabilitiesFromModels(models), nil
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
	return availabilitiesFromModels(models), nil
}

// LastScannedAt returns the newest last_scanned stamp across the waypoint's
// era-scoped rows, and whether any such row exists. Era-scoped like every other
// read here, so a scan booked in a DEAD era reads as never-scanned and the yard
// is re-scanned once in the open era. A store error surfaces rather than being
// flattened into "never scanned": the caller decides, and it fails toward
// scanning.
func (r *ShipyardInventoryRepositoryGORM) LastScannedAt(ctx context.Context, playerID int, waypointSymbol string) (time.Time, bool, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []ShipyardInventoryModel
	if err := r.db.WithContext(ctx).Model(&ShipyardInventoryModel{}).
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
		Where(predicate, args...).
		Order("last_scanned DESC").
		Limit(1).
		Find(&models).Error; err != nil {
		return time.Time{}, false, fmt.Errorf("failed to read shipyard scan recency for %s: %w", waypointSymbol, err)
	}
	if len(models) == 0 {
		return time.Time{}, false, nil
	}
	return models[0].LastScanned, true, nil
}

// ScannedSystems returns the DISTINCT systems the player has a live-era shipyard
// scan for — the SCANNED set the backfill sweep excludes when enumerating
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

// ListSavedYards returns every era-scoped row for the player, optionally
// filtered to shipTypes (empty = every saved ship type), ordered by
// purchase_price ASCENDING — the `shipyard yards --type` CLI query.
// Unlike ListByTypes (waypoint/type order for deterministic downstream
// ranking, and a no-op on empty shipTypes), this orders for an operator
// scanning for the cheapest yard and treats an empty filter as "every type".
func (r *ShipyardInventoryRepositoryGORM) ListSavedYards(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	query := r.db.WithContext(ctx).Model(&ShipyardInventoryModel{}).
		Where("player_id = ?", playerID).
		Where(predicate, args...)
	if len(shipTypes) > 0 {
		query = query.Where("ship_type IN ?", shipTypes)
	}
	var models []ShipyardInventoryModel
	if err := query.Order("purchase_price ASC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list saved yards: %w", err)
	}
	return availabilitiesFromModels(models), nil
}

// CheapestPricedYard returns the era-scoped row with the LOWEST POSITIVE
// purchase_price among the player's known yards selling one of shipTypes, and
// whether any such row exists. It is the price half of the derived heavy
// reservation (common.HeavyReserve): found=false means the capability is CLOSED
// (no known yard sells the class at a usable price) and nothing is reserved.
//
// POSITIVE prices only. A purchase_price of 0 means the type was LISTED
// (availability known) but carried no priced listing at scan time; such a row
// proves availability but can never feed a money guard, so including it would
// report a "cheapest" price of zero and collapse the reservation to nothing
// while a real, buyable yard sat one row away. Availability-only questions keep
// using HasAnyOfTypes, which deliberately counts unpriced rows.
//
// Era-scoped like every other read here, so a dead era's yards never leak into a
// live buy signal. Ordered (price, waypoint) so the answer is deterministic when
// two yards ask the same price. Empty shipTypes returns not-found rather than
// scanning every type — a caller that lost its heavy-type set must not silently
// reserve against an unrelated hull.
func (r *ShipyardInventoryRepositoryGORM) CheapestPricedYard(ctx context.Context, playerID int, shipTypes []string) (shipyard.ShipTypeAvailability, bool, error) {
	if len(shipTypes) == 0 {
		return shipyard.ShipTypeAvailability{}, false, nil
	}
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []ShipyardInventoryModel
	if err := r.db.WithContext(ctx).Model(&ShipyardInventoryModel{}).
		Where("player_id = ? AND ship_type IN ?", playerID, shipTypes).
		Where("purchase_price > 0").
		Where(predicate, args...).
		Order("purchase_price ASC, waypoint_symbol ASC").
		Limit(1).
		Find(&models).Error; err != nil {
		return shipyard.ShipTypeAvailability{}, false, fmt.Errorf("failed to read cheapest priced yard: %w", err)
	}
	if len(models) == 0 {
		return shipyard.ShipTypeAvailability{}, false, nil
	}
	m := models[0]
	return shipyard.ShipTypeAvailability{
		SystemSymbol:   m.SystemSymbol,
		WaypointSymbol: m.WaypointSymbol,
		ShipType:       m.ShipType,
		PurchasePrice:  m.PurchasePrice,
		Supply:         m.Supply,
		LastScanned:    m.LastScanned,
	}, true, nil
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

func availabilitiesFromModels(models []ShipyardInventoryModel) []shipyard.ShipTypeAvailability {
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
	return out
}
