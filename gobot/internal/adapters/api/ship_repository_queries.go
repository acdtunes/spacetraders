package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// FindBySymbol retrieves a ship by symbol and player ID from database.
// If not found in DB, syncs from API first.
// Database is the source of truth after daemon startup.
func (r *ShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	var model persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", symbol, playerID.Value()).
		First(&model).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Ship not in DB - might be newly purchased, sync from API
		return r.SyncShipFromAPI(ctx, symbol, playerID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query ship: %w", err)
	}

	return r.modelToDomain(ctx, &model, playerID)
}

// GetShipData retrieves raw ship data from API (includes arrival time for IN_TRANSIT ships)
func (r *ShipRepository) GetShipData(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.ShipData, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Fetch ship data from API (includes ArrivalTime for IN_TRANSIT ships)
	shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship from API: %w", err)
	}

	return shipData, nil
}

// FindAllByPlayer retrieves all ships for a player from database with short-lived caching.
// Database is the source of truth after daemon startup.
//
// Caching: Returns cached ship list if within 15 seconds of last fetch.
// This prevents redundant DB reads when multiple coordinators call this method.
func (r *ShipRepository) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	cacheKey := playerID.Value()

	// Check cache first
	if cached, ok := r.shipListCache.Load(cacheKey); ok {
		cachedList := cached.(*cachedShipList)
		if time.Since(cachedList.fetchedAt) < shipListCacheTTL {
			// Return a copy to prevent mutation of cached data
			shipsCopy := make([]*navigation.Ship, len(cachedList.ships))
			copy(shipsCopy, cachedList.ships)
			return shipsCopy, nil
		}
	}

	// Fetch all ships from database
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID.Value()).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query ships: %w", err)
	}

	// Convert DB models to domain entities
	ships := make([]*navigation.Ship, 0, len(models))
	for _, model := range models {
		ship, err := r.modelToDomain(ctx, &model, playerID)
		if err != nil {
			log.Printf("Warning: failed to convert ship %s: %v", model.ShipSymbol, err)
			continue
		}
		ships = append(ships, ship)
	}

	// Cache the result
	r.shipListCache.Store(cacheKey, &cachedShipList{
		ships:     ships,
		fetchedAt: r.clock.Now(),
	})

	return ships, nil
}

// FindBySymbolCached retrieves a ship from the cached ship list if available,
// otherwise falls back to a direct DB query.
//
// OPTIMIZATION: When selecting ships from a known list (e.g., idle haulers),
// use this method to avoid N individual DB queries. The cached list is refreshed
// every 15 seconds via FindAllByPlayer.
//
// Use cases:
//   - Ship selection loops (SelectClosestShip, RebalanceFleet)
//   - Any code that iterates through ship symbols to load ship data
//
// Falls back to FindBySymbol (direct DB query) if:
//   - Ship not found in cache
//   - Cache is stale or empty
func (r *ShipRepository) FindBySymbolCached(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	// First try to find in cached ship list
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		// Cache miss/error - fall back to direct API call
		return r.FindBySymbol(ctx, symbol, playerID)
	}

	// Search for ship in cached list
	for _, ship := range allShips {
		if ship.ShipSymbol() == symbol {
			return ship, nil
		}
	}

	// Not found in cache - this could mean:
	// 1. Ship was just purchased and cache is stale
	// 2. Ship symbol is wrong
	// Fall back to direct API call for definitive answer
	return r.FindBySymbol(ctx, symbol, playerID)
}

// FindManyBySymbolsCached retrieves multiple ships from the cached ship list.
//
// OPTIMIZATION: Replaces loops that call FindBySymbol for each ship.
// Instead of N API calls, this uses a single cached FindAllByPlayer call
// and filters in memory.
//
// Returns ships in the same order as requested symbols.
// Missing ships are omitted from the result (no error).
func (r *ShipRepository) FindManyBySymbolsCached(ctx context.Context, symbols []string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	// Get all ships from cache
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	// Build lookup map for efficient searching
	shipMap := make(map[string]*navigation.Ship, len(allShips))
	for _, ship := range allShips {
		shipMap[ship.ShipSymbol()] = ship
	}

	// Collect ships in requested order
	result := make([]*navigation.Ship, 0, len(symbols))
	for _, symbol := range symbols {
		if ship, found := shipMap[symbol]; found {
			result = append(result, ship)
		}
	}

	return result, nil
}

// FindByContainer retrieves all ships assigned to a specific container
func (r *ShipRepository) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter by container
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.ContainerID() == containerID {
			result = append(result, ship)
		}
	}

	return result, nil
}

// FindIdleByPlayer retrieves all idle (unassigned) ships for a player
func (r *ShipRepository) FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter idle ships
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.IsIdle() {
			result = append(result, ship)
		}
	}

	return result, nil
}

// FindActiveByPlayer retrieves all actively assigned ships for a player
func (r *ShipRepository) FindActiveByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	// Get all ships for player
	allShips, err := r.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Filter assigned ships
	var result []*navigation.Ship
	for _, ship := range allShips {
		if ship.IsAssigned() {
			result = append(result, ship)
		}
	}

	return result, nil
}

// CountHeavyHulls counts the player's owned HEAVY hulls — the census behind both
// the heavy_cap guard and the derived heavy reservation (common.HeavyReserve).
//
// Counting is DELIBERATELY BROAD: every owned heavy, regardless of which fleet it
// is tagged to, whether it is idle, assigned, or in transit. The cap this feeds
// bounds CAPITAL EXPOSURE, not trade-fleet size, and under-counting is the
// dangerous direction — it is what would authorise buying a hull we already own.
// Do not narrow this with a dedicated_fleet or assignment_status filter; the
// autosizer's own tag-scoped trade-pool count (DedicatedFleet=="trade", which
// backs the heavy class's DEMAND signal) is a SEPARATE question with a separate
// method — and since sp-r7eiu removed class_ceiling, this census is the ONLY
// count that can refuse a heavy purchase.
//
// A hull is heavy when shipyard.IsHeavyHull says so: its frame is in the known
// heavy list, OR its cargo capacity is at/above the heavy threshold. The frame
// list is primary; the capacity net is the fallback, because the ships table has
// NO ship_type column and the heavy frame symbols are INFERRED from SpaceTraders'
// naming symmetry rather than observed (the fleet owns no heavy to read one from).
// A wrong frame symbol would under-count, so the net catches large hulls whatever
// frame they report — over-counting instead, which buys FEWER heavies.
//
// A hull matched only by the net (unrecognised frame) is logged at WARNING: that
// line is the only signal available that the frame list is incomplete, and the
// list gets corrected from it. shipyard.DefaultHeavyFrameSymbols is the frame
// projection of the same table the yard query's ship types come from, so the two
// sides cannot drift (see TestHeavyHullClassPairing).
//
// Ships are live API state and carry no era_id, so this is player-scoped only —
// unlike the era-scoped shipyard_inventory reads.
func (r *ShipRepository) CountHeavyHulls(ctx context.Context, playerID shared.PlayerID) (int, error) {
	if r.db == nil {
		// NEVER a silent zero: zero reads as "no heavies owned" and would authorise a
		// buy against an unreadable fleet (RULINGS #4 fail-closed).
		return 0, fmt.Errorf("database not configured")
	}

	// Narrow the scan to plausible candidates in SQL, then classify in Go so the
	// capacity net's firing can be detected and logged. The OR is what makes the
	// frame list non-authoritative for exclusion: a large hull is a candidate even
	// when its frame is unknown to us.
	var rows []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("player_id = ?", playerID.Value()).
		Where("frame_symbol IN ? OR cargo_capacity >= ?",
			shipyard.DefaultHeavyFrameSymbols, shipyard.HeavyCargoCapacityThreshold).
		Find(&rows).Error
	if err != nil {
		return 0, fmt.Errorf("failed to read hulls for the heavy census: %w", err)
	}

	logger := logging.LoggerFromContext(ctx)
	count := 0
	for _, row := range rows {
		heavy, unrecognisedFrame := shipyard.IsHeavyHull(row.FrameSymbol, row.CargoCapacity)
		if !heavy {
			continue
		}
		count++
		if unrecognisedFrame {
			// The ONLY signal available that the inferred frame list is incomplete: this
			// hull is large enough to be a heavy but its frame is not in the known list,
			// and the fleet owns no heavy to check the list against. Treat the first such
			// line in production as the frame list asking to be corrected.
			logger.Log("WARNING", fmt.Sprintf(
				"Heavy census: hull %s has UNRECOGNISED frame %s with cargo capacity %d (>= heavy threshold %d) — counted as heavy by the capacity safety net. The known-heavy frame list is likely incomplete; add this frame to heavyHullClasses.",
				row.ShipSymbol, row.FrameSymbol, row.CargoCapacity, shipyard.HeavyCargoCapacityThreshold,
			), map[string]interface{}{
				"action":         "heavy_census_unrecognised_frame",
				"ship_symbol":    row.ShipSymbol,
				"frame_symbol":   row.FrameSymbol,
				"cargo_capacity": row.CargoCapacity,
				"threshold":      shipyard.HeavyCargoCapacityThreshold,
			})
		}
	}

	return count, nil
}

// CountByContainerPrefix counts active assignments where container ID starts with prefix
func (r *ShipRepository) CountByContainerPrefix(ctx context.Context, prefix string, playerID shared.PlayerID) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	var count int64
	err := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("container_id LIKE ?", prefix+"%").
		Where("player_id = ?", playerID.Value()).
		Where("assignment_status = ?", "active").
		Count(&count).Error

	if err != nil {
		return 0, fmt.Errorf("failed to count assignments by prefix: %w", err)
	}

	return int(count), nil
}

// FindInTransitWithPastArrival finds ships that should have arrived (IN_TRANSIT with arrival_time in the past)
func (r *ShipRepository) FindInTransitWithPastArrival(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("nav_status = ?", "IN_TRANSIT").
		Where("arrival_time IS NOT NULL").
		Where("arrival_time <= ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindInTransitWithFutureArrival finds ships that will arrive in the future (for scheduling)
func (r *ShipRepository) FindInTransitWithFutureArrival(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("nav_status = ?", "IN_TRANSIT").
		Where("arrival_time IS NOT NULL").
		Where("arrival_time > ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindWithExpiredCooldown finds ships with past cooldowns
func (r *ShipRepository) FindWithExpiredCooldown(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("cooldown_expiration IS NOT NULL").
		Where("cooldown_expiration <= ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindWithFutureCooldown finds ships with cooldowns expiring in the future (for scheduling)
func (r *ShipRepository) FindWithFutureCooldown(ctx context.Context) ([]*navigation.Ship, error) {
	var models []persistence.ShipModel
	err := r.db.WithContext(ctx).
		Where("cooldown_expiration IS NOT NULL").
		Where("cooldown_expiration > ?", r.clock.Now()).
		Find(&models).Error
	if err != nil {
		return nil, err
	}

	return r.modelsToShips(ctx, models), nil
}

// FindModuleRequirements resolves symbol's own power/crew/slot requirements
// by scanning every ship's installed module list for a match. There is no catalog of unowned module specs anywhere in
// this codebase or the SpaceTraders API, so a candidate's requirements can
// only come from having been observed installed somewhere - the same module
// symbol has identical requirements on every hull that carries it. Unscoped
// by player, mirroring FindInTransitWithPastArrival and the other
// background-updater queries above.
//
// Scans and unmarshals modules JSON in Go rather than using a Postgres
// jsonb operator (e.g. @>) because the modules column is queried against
// both SQLite (test harness, database.NewTestConnection) and Postgres
// (production) - a jsonb-only operator would work in production but break
// every test using the real repository. A row with corrupt/unparseable
// modules JSON is skipped, not treated as a fatal error, so one bad row
// cannot hide a real match on another ship.
//
// The bool return is false only when no ship anywhere has ever carried
// symbol; callers must treat that as "requirements unknown" (see
// UnknownRequirementsFeasibility), never substitute a zero-valued
// ShipRequirements for a real one.
func (r *ShipRepository) FindModuleRequirements(ctx context.Context, symbol string) (navigation.ShipRequirements, bool, error) {
	var zero navigation.ShipRequirements

	var models []persistence.ShipModel
	if err := r.db.WithContext(ctx).Find(&models).Error; err != nil {
		return zero, false, err
	}

	for _, model := range models {
		if model.Modules == "" || model.Modules == "[]" {
			continue
		}
		var modulesJSON []persistence.ModuleJSON
		if err := json.Unmarshal([]byte(model.Modules), &modulesJSON); err != nil {
			continue // corrupt row - skip, don't fail the whole lookup
		}
		for _, mod := range modulesJSON {
			if mod.Symbol == symbol {
				return navigation.NewShipRequirements(mod.Requirements.Power, mod.Requirements.Crew, mod.Requirements.Slots), true, nil
			}
		}
	}

	return zero, false, nil
}
