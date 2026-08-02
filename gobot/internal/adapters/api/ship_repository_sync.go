package api

import (
	"context"
	"fmt"
	"log"

	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SyncAllFromAPI fetches all ships from API and upserts to database
func (r *ShipRepository) SyncAllFromAPI(ctx context.Context, playerID shared.PlayerID) (int, error) {
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to get player: %w", err)
	}

	// Fetch all ships from API
	shipsData, err := r.apiClient.ListShips(ctx, player.Token)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships from API: %w", err)
	}

	now := r.clock.Now()
	models := make([]persistence.ShipModel, 0, len(shipsData))
	// reanchors accumulates the hulls this pass found in a DIFFERENT system from the one
	// their row claimed. Published only after the batch commits, so nothing is announced
	// that was not actually written.
	var reanchors []PositionReanchor

	for _, data := range shipsData {
		model := r.shipDataToModel(ctx, data, playerID, now)

		existingModel, hadRow := r.loadShipRow(ctx, model.ShipSymbol, model.PlayerID)
		if hadRow {
			preserveLocallyOwnedColumns(model, existingModel)
		}

		// The periodic full-fleet resync is the broadest detector the fleet has: it sees
		// EVERY hull on a fixed cadence without needing a coordinator to happen to
		// re-anchor one.
		if reanchor, diverged := divergedPosition(hadRow,
			shipPosition{system: existingModel.SystemSymbol, waypoint: existingModel.LocationSymbol},
			shipPosition{system: model.SystemSymbol, waypoint: model.LocationSymbol}); diverged {
			reanchor.ShipSymbol = model.ShipSymbol
			reanchor.PlayerID = model.PlayerID
			reanchors = append(reanchors, reanchor)
		}

		models = append(models, *model)
	}

	if err := r.upsertShipModels(ctx, models); err != nil {
		return 0, err
	}

	for _, reanchor := range reanchors {
		r.reportPositionReanchor(ctx, reanchor)
	}

	// reconcile the persisted fleet to the live source of truth. The
	// upsert above only ADDS/UPDATES the ships GET /my/ships returned; it never
	// removes rows the live API no longer reports, so without this step stale rows
	// linger forever:
	//   (1) a hull sold/destroyed within the current era, and
	//   (2) a PRIOR ERA's fleet. The agent re-registers
	//       on every server reset under a NEW players row (new player_id) for the
	//       SAME agent_symbol, and ship symbols are REUSED across eras. A dead-era player
	//       row carries a dead token, so its own SyncAllFromAPI fails and its ship
	//       rows are never revisited — they persist as ghosts. Any read that
	//       aggregates by agent_symbol (not the exact live player_id) then unions
	//       the live fleet with dead-era rows and reads a stale frame_symbol. ListShips is fully
	//       paginated and returns error-or-complete, so a successful, non-empty
	//       response IS the authoritative fleet: every ships row for this agent
	//       that is not one we just upserted under playerID is stale. At most one
	//       player_id per agent_symbol can hold a live token at a time
	//       (re-registration invalidates the old one), so deleting the agent's
	//       other-era rows is safe. FK-safe: nothing references ships (assignment
	//       data is denormalized into the row).
	if err := r.reconcileFleetToLive(ctx, playerID, shipsData); err != nil {
		// Non-fatal: the upsert already persisted the live fleet correctly; a
		// failed prune merely leaves ghosts for the next sync to clear, so it must
		// not fail the whole sync. Logged loudly so a persistent failure surfaces.
		log.Printf("Warning: failed to reconcile stale ships for player %d: %v", playerID.Value(), err)
	}

	// Invalidate cache
	r.shipListCache.Delete(playerID.Value())

	return len(models), nil
}

func (r *ShipRepository) loadShipRow(ctx context.Context, shipSymbol string, playerID int) (persistence.ShipModel, bool) {
	var existingModel persistence.ShipModel
	found := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID).
		First(&existingModel).Error == nil
	return existingModel, found
}

// Carries forward the standing tags the API knows nothing about; without this the
// UpdateAll upsert wipes captain reservations, fleet pins and cargo reservations.
func preserveLocallyOwnedColumns(model *persistence.ShipModel, existingModel persistence.ShipModel) {
	model.ContainerID = existingModel.ContainerID
	model.AssignmentStatus = existingModel.AssignmentStatus
	model.AssignedAt = existingModel.AssignedAt
	model.ReleasedAt = existingModel.ReleasedAt
	model.ReleaseReason = existingModel.ReleaseReason
	model.AssignmentOwner = existingModel.AssignmentOwner
	model.AssignmentReason = existingModel.AssignmentReason
	model.DedicatedFleet = existingModel.DedicatedFleet
	model.ReservationOverrides = existingModel.ReservationOverrides
}

func (r *ShipRepository) upsertShipModels(ctx context.Context, models []persistence.ShipModel) error {
	if len(models) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&models).Error
	if err != nil {
		return fmt.Errorf("failed to upsert ships: %w", err)
	}
	return nil
}

// reconcileFleetToLive deletes every ships row belonging to playerID's agent
// that is NOT part of the live fleet just synced under playerID — the durable
// half of the reconcile (see the call-site comment for the full rationale).
// The keep-set is derived from the raw live API response, not the post-convert
// models, so a transient per-ship conversion failure can never delete a
// genuinely-live hull. Guarded to never prune on an empty live fleet: a live
// agent always has >=1 ship, so an empty set signals a bad/partial fetch we
// refuse to act on destructively.
func (r *ShipRepository) reconcileFleetToLive(ctx context.Context, playerID shared.PlayerID, live []*navigation.ShipData) error {
	if len(live) == 0 {
		return nil
	}
	liveSymbols := make([]string, 0, len(live))
	for _, d := range live {
		liveSymbols = append(liveSymbols, d.Symbol)
	}
	// Delete everything for this agent (all eras) except the live rows we just
	// wrote under playerID. The agent is resolved from the DB, not the caller,
	// so it stays correct even when the player token/struct is supplied by a
	// thin caller that only carries the id.
	return r.db.WithContext(ctx).Exec(
		`DELETE FROM ships
		 WHERE player_id IN (
		     SELECT id FROM players WHERE agent_symbol = (
		         SELECT agent_symbol FROM players WHERE id = ?
		     )
		 )
		 AND NOT (player_id = ? AND ship_symbol IN (?))`,
		playerID.Value(), playerID.Value(), liveSymbols,
	).Error
}

// SyncShipFromAPI fetches a single ship from API and persists to database
func (r *ShipRepository) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// Fetch from API
	shipData, err := r.apiClient.GetShip(ctx, symbol, player.Token)
	if err != nil {
		return nil, err
	}

	// Convert to model and persist
	now := r.clock.Now()
	model := r.shipDataToModel(ctx, shipData, playerID, now)

	// Preserve existing assignment data
	var existingModel persistence.ShipModel
	hadRow := r.db.WithContext(ctx).
		Where("ship_symbol = ? AND player_id = ?", model.ShipSymbol, model.PlayerID).
		First(&existingModel).Error == nil
	if hadRow {
		// Preserve assignment data
		model.ContainerID = existingModel.ContainerID
		model.AssignmentStatus = existingModel.AssignmentStatus
		model.AssignedAt = existingModel.AssignedAt
		model.ReleasedAt = existingModel.ReleasedAt
		model.ReleaseReason = existingModel.ReleaseReason
		// see matching comment in SyncAllFromAPI - without this, a
		// captain reservation's ownership is silently clobbered back to the
		// "container" default the next time this ship is synced from the API.
		model.AssignmentOwner = existingModel.AssignmentOwner
		model.AssignmentReason = existingModel.AssignmentReason
		// see matching comment in SyncAllFromAPI - without this, a
		// `fleet assign` pin is silently wiped back to "" the next time this
		// ship is synced from the API, opening it up to poaching.
		model.DedicatedFleet = existingModel.DedicatedFleet
		// see the matching comment in SyncAllFromAPI — without this a
		// do-not-sell reservation is silently wiped the next time this ship is
		// synced from the API, re-exposing a staged outfitting module.
		model.ReservationOverrides = existingModel.ReservationOverrides
	}

	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(model).Error
	if err != nil {
		return nil, fmt.Errorf("failed to persist ship: %w", err)
	}

	// The write landed. If it CONTRADICTS the row it replaced, we have just discovered
	// that our durable position was wrong — a completed move that was never persisted.
	// Publish it (ship_position_reanchor.go); a silent correction is exactly how the
	// original write-loss survived an entire incident.
	if reanchor, diverged := divergedPosition(hadRow,
		shipPosition{system: existingModel.SystemSymbol, waypoint: existingModel.LocationSymbol},
		shipPosition{system: model.SystemSymbol, waypoint: model.LocationSymbol}); diverged {
		reanchor.ShipSymbol = model.ShipSymbol
		reanchor.PlayerID = model.PlayerID
		r.reportPositionReanchor(ctx, reanchor)
	}

	// Invalidate cache
	r.shipListCache.Delete(playerID.Value())

	domainShip, err := r.modelToDomain(ctx, model, playerID)
	if err != nil {
		return nil, err
	}

	// A hull synced mid-transit needs its arrival timer armed here:
	// ScheduleAllPending only runs at daemon boot, so without this the row
	// would sit IN_TRANSIT with no timer and no ARRIVED event until the next
	// restart, and every waiter would fall back to its slow park path.
	// ScheduleArrival is idempotent (it replaces any existing timer).
	if r.arrivalScheduler != nil &&
		domainShip.NavStatus() == navigation.NavStatusInTransit &&
		domainShip.ArrivalTime() != nil {
		r.arrivalScheduler.ScheduleArrival(domainShip)
	}

	return domainShip, nil
}
