package api

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
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

	// Fetch all ships from API. readReport carries whether any hull came back
	// unparseable — the fleet read survives one poisoned member now, so a
	// successful call no longer implies a COMPLETE fleet.
	shipsData, readReport, err := r.listFleetForSync(ctx, player.Token)
	if err != nil {
		return 0, fmt.Errorf("failed to list ships from API: %w", err)
	}

	r.reportUnreadableHulls(ctx, playerID, shipsData, readReport)

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
	//       paginated, so a successful, non-empty AND COMPLETE response IS the
	//       authoritative fleet: every ships row for this agent
	//       that is not one we just upserted under playerID is stale. At most one
	//       player_id per agent_symbol can hold a live token at a time
	//       (re-registration invalidates the old one), so deleting the agent's
	//       other-era rows is safe. FK-safe: nothing references ships (assignment
	//       data is denormalized into the row).
	//
	//       Completeness is no longer implied by success: the read now drops hulls
	//       it cannot deserialise rather than failing the whole fleet, so
	//       readReport is passed in and a PARTIAL read prunes nothing.
	if err := r.reconcileFleetToLive(ctx, playerID, shipsData, readReport); err != nil {
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
	model.RetiringAt = existingModel.RetiringAt
}

// maxBindParameters is one statement's bind-parameter ceiling — SQLite's, the stricter backend.
const maxBindParameters = 32766

// shipUpsertBatchRows caps a multi-row INSERT: rows * persisted-columns must stay under
// maxBindParameters, or a sync fails outright once the fleet outgrows one statement.
func shipUpsertBatchRows() int {
	cols := shipModelColumnCount()
	if cols <= 0 {
		return 1
	}
	rows := maxBindParameters / cols
	if rows < 1 {
		return 1
	}
	return rows
}

// shipModelColumnCount counts persisted columns — the per-row bind-parameter cost.
func shipModelColumnCount() int {
	t := reflect.TypeOf(persistence.ShipModel{})
	n := 0
	for i := 0; i < t.NumField(); i++ {
		if strings.Contains(t.Field(i).Tag.Get("gorm"), "column:") {
			n++
		}
	}
	return n
}

func (r *ShipRepository) upsertShipModels(ctx context.Context, models []persistence.ShipModel) error {
	if len(models) == 0 {
		return nil
	}
	// CreateInBatches, never Create: it bounds the statement, not the transaction.
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		CreateInBatches(&models, shipUpsertBatchRows()).Error
	if err != nil {
		return fmt.Errorf("failed to upsert ships: %w", err)
	}
	return nil
}

// reportUnreadableHulls names every hull the live read could not deliver, at WARNING
// and on a counter, once per pass. Surviving a poisoned hull is what lets this fail
// QUIETLY, so the counter is the only thing that surfaces it.
func (r *ShipRepository) reportUnreadableHulls(ctx context.Context, playerID shared.PlayerID, live []*navigation.ShipData, read FleetReadReport) {
	if !read.Partial() {
		return
	}
	for _, symbol := range r.unreadableHullNames(ctx, playerID, liveSymbolsOf(live), read) {
		log.Printf("WARNING [fleet_hull_unreadable] player=%d ship=%s: we own this hull and the API would not serve its record this pass; it is PRESENT-BUT-UNKNOWN — its row is kept, it is counted in the fleet, and nothing acts on it",
			playerID.Value(), symbol)
		metrics.RecordHullUnreadable(symbol, strconv.Itoa(playerID.Value()))
	}
}

const unidentifiedHull = "<unidentified>"

// unreadableHullNames names the hulls the live read did not deliver, best evidence
// first: a symbol the payload yielded, then OUR-ROWS-MINUS-READABLE (a 500 carries no
// payload, so a refused page is identifiable only by INDEX and our rows are the only
// other witness), then unidentifiedHull. The row diff is not a claim about WHICH hull
// is poisoned — "unreadable" and "sold" are indistinguishable on a partial read, hence
// the suppressed prune. It never returns empty for a partial read: a hull we hold no
// row for is unnameable, and reporting NOTHING is the invisible failure itself.
// Inventing a row would create the symbol-less row decodeFleetElement refuses.
func (r *ShipRepository) unreadableHullNames(ctx context.Context, playerID shared.PlayerID, readable []string, read FleetReadReport) []string {
	if !read.Partial() {
		return nil
	}
	readableSet := make(map[string]bool, len(readable))
	for _, symbol := range readable {
		readableSet[symbol] = true
	}

	var names []string
	seen := make(map[string]bool)
	add := func(symbol string) {
		if symbol == "" || readableSet[symbol] || seen[symbol] {
			return
		}
		seen[symbol] = true
		names = append(names, symbol)
	}

	for _, u := range read.Unreadable {
		add(u.Symbol)
	}

	var rows []persistence.ShipModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID.Value()).
		Find(&rows).Error; err != nil {
		log.Printf("Warning: could not name the unreadable hulls for player %d: %v", playerID.Value(), err)
	}
	for _, row := range rows {
		add(row.ShipSymbol)
	}

	if len(names) == 0 {
		return []string{unidentifiedHull}
	}
	return names
}

func liveSymbolsOf(live []*navigation.ShipData) []string {
	symbols := make([]string, 0, len(live))
	for _, d := range live {
		symbols = append(symbols, d.Symbol)
	}
	return symbols
}

// fleetReadReporter is the fleet read that also reports what it could NOT read.
// The domain port's ListShips cannot carry that — it is implemented by test
// doubles throughout the tree — so the real client widens it here and the sync
// takes the richer read whenever the client offers one, which in production is
// always: *SpaceTradersClient implements it.
type fleetReadReporter interface {
	ListShipsWithReport(ctx context.Context, token string) ([]*navigation.ShipData, FleetReadReport, error)
}

// listFleetForSync reads the live fleet, preferring the reporting form so a
// partial read stays visible to the prune below. A client that satisfies only
// the port reports nothing unreadable — which is the truth for a double that
// hands back a fixed slice, and matches the old behaviour exactly.
func (r *ShipRepository) listFleetForSync(ctx context.Context, token string) ([]*navigation.ShipData, FleetReadReport, error) {
	if reporter, ok := r.apiClient.(fleetReadReporter); ok {
		return reporter.ListShipsWithReport(ctx, token)
	}
	ships, err := r.apiClient.ListShips(ctx, token)
	return ships, FleetReadReport{}, err
}

// reconcileFleetToLive deletes every ships row belonging to playerID's agent
// that is NOT part of the live fleet just synced under playerID — the durable
// half of the reconcile (see the call-site comment for the full rationale).
// The keep-set is derived from the raw live API response, not the post-convert
// models, so a transient per-ship conversion failure can never delete a
// genuinely-live hull. Guarded to never prune on an empty live fleet: a live
// agent always has >=1 ship, so an empty set signals a bad/partial fetch we
// refuse to act on destructively.
//
// read is the same refusal generalised. The delete's entire justification is
// that absence from the live list means SOLD; once the fleet read is allowed to
// drop a hull it merely failed to parse, absence also means UNREADABLE, and
// pruning would delete the row of the one hull already in trouble — TORWIND-5,
// the hull the 24h outage was about. The guard lives here, next to the DELETE,
// rather than at the call site, so no future caller can reach the destructive
// statement without having answered the question.
func (r *ShipRepository) reconcileFleetToLive(ctx context.Context, playerID shared.PlayerID, live []*navigation.ShipData, read FleetReadReport) error {
	if len(live) == 0 {
		return nil
	}
	if read.Partial() {
		log.Printf("WARNING [fleet_prune_suppressed] player=%d unreadable=%d live_readable=%d: the live fleet read was PARTIAL, so stale-row pruning is skipped this pass rather than deleting a hull that only failed to parse",
			playerID.Value(), len(read.Unreadable), len(live))
		return nil
	}
	liveSymbols := liveSymbolsOf(live)
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
		// Same clobber class: without this a retired hull quietly rejoins service on
		// the next sync and gets planned another tour.
		model.RetiringAt = existingModel.RetiringAt
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
