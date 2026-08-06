package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ContainerStatus returns the lifecycle status of a container and whether the
// container row exists. It backs the ship-refresh stale-claim reconciler
// (internal/application/ship/queries.ContainerStatusReader): found=false lets the
// reconciler treat a ship claim whose container row is gone as orphaned, while a
// live PENDING/RUNNING status lets it distinguish a dead CLI-runner artifact from
// an active daemon worker.
func (r *ContainerRepositoryGORM) ContainerStatus(
	ctx context.Context,
	containerID string,
	playerID shared.PlayerID,
) (string, bool, error) {
	var model ContainerModel
	err := r.db.WithContext(ctx).
		Select("status").
		Where("id = ? AND player_id = ?", containerID, playerID.Value()).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to read container status: %w", err)
	}
	return model.Status, true, nil
}

// ListByStatus lists all containers with a specific status
func (r *ContainerRepositoryGORM) ListByStatus(
	ctx context.Context,
	status container.ContainerStatus,
	playerID *int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx).Where("status = ?", string(status))

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers by status: %w", err)
	}

	return models, nil
}

// FindByIDAcrossPlayers returns the container with this id whichever player owns it, or
// (nil, nil) when there is none — the same absent-is-not-an-error convention Get uses.
//
// It exists because the worker start and recovery paths know a container id but not a player id,
// so they cannot use the player-scoped Get. Before sp-72gmi they called ListAll(ctx, nil) and
// linear-scanned the result in Go: every worker start loaded the ENTIRE containers table into
// memory to find one row. That table has no retention policy, and the sp-20eyn crash loop had
// pushed it to 34,279 FAILED rows alone — so the loop's own wreckage made every subsequent worker
// start more expensive, on precisely the path that was already failing.
//
// A single indexed read replaces it. The primary key is (id, player_id) and id LEADS it, so
// `WHERE id = ?` is a prefix match the primary-key index serves directly — no separate index is
// needed, and the cost stops depending on the size of the table.
func (r *ContainerRepositoryGORM) FindByIDAcrossPlayers(
	ctx context.Context,
	containerID string,
) (*ContainerModel, error) {
	var model ContainerModel

	result := r.db.WithContext(ctx).
		Where("id = ?", containerID).
		First(&model)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find container %s: %w", containerID, result.Error)
	}

	return &model, nil
}

// ContainerRetentionWindow is how long a TERMINAL container row is kept before it is pruned
// (sp-72gmi).
//
// FOURTEEN DAYS, and the number is a forensics budget rather than a storage one. The sp-20eyn
// crash loop was identified from container statuses — 34,279 FAILED rows — so a bound tight
// enough to have swept them would have hidden its own smoking gun. Fourteen days covers an
// incident that begins on a Friday and is not looked at until the following week, with two full
// weekends inside the window.
//
// Tightening this is a forensics decision, not a performance one, and the performance argument
// for tightening it no longer exists. Since the container lookup became an indexed read
// (sp-72gmi, first half), the only reads that still grow with this table are the two boot-time
// recovery scans, measured at 880us against 34,000 rows, twice per boot. Table size is disk and
// clutter now, not latency. Anyone shortening this window should be arguing about how far back
// an incident must stay diagnosable, and should say so here.
//
// WHAT THE ROWS ARE FOR, and what they are not. The COUNT of failures is NOT stored here: it
// lives in the spacetraders_daemon_container_total{status} counter, on Prometheus's own
// retention, and survives any pruning. These rows carry the per-container detail — which
// container, which exit reason, when — which is only useful while an incident is recent. That
// split is what lets this window be bounded at all without losing the evidence.
const ContainerRetentionWindow = 14 * 24 * time.Hour

// terminalContainerStatuses are the states a container never leaves. Only these are ever
// pruned. RUNNING, PENDING, STOPPING and INTERRUPTED are deliberately absent: a live container
// is operational state, and INTERRUPTED is the daemon's own restart-recovery queue — deleting
// any of them by age would destroy work in progress rather than history.
var terminalContainerStatuses = []container.ContainerStatus{
	container.ContainerStatusFailed,
	container.ContainerStatusStopped,
	container.ContainerStatusCompleted,
}

// PruneTerminalContainers deletes terminal container rows that finished before olderThan, and
// returns how many it deleted PER STATUS.
//
// The per-status breakdown is the point of returning a map rather than a total: a silent pruner
// is indistinguishable from a broken one, and someone chasing a container that has vanished needs
// to know whether retention took it and in what state it was taken. It runs one DELETE per status
// for exactly that reason — three cheap statements bought a countable answer.
//
// Age is measured from stopped_at, falling back to started_at for a row whose stop was never
// recorded. A row with NEITHER timestamp is never pruned: it cannot be dated, and refusing to
// delete what we cannot date is the safe direction for an irreversible operation.
//
// That last protection actually comes from SQL's three-valued logic, not from the IS NOT NULL
// clause below — `NULL < cutoff` evaluates to NULL rather than TRUE, so an undatable row fails
// the age predicate on its own. A mutation probe removing the clause changed no behaviour and
// killed no test, which is how that was established rather than assumed. It is kept because it
// states the intent at the query site AND because it becomes load-bearing the moment anyone
// gives the COALESCE a non-NULL final argument — a plausible edit that would otherwise silently
// turn every undatable row into an old one and delete the lot.
func (r *ContainerRepositoryGORM) PruneTerminalContainers(
	ctx context.Context,
	olderThan time.Time,
) (map[container.ContainerStatus]int64, error) {
	deleted := make(map[container.ContainerStatus]int64, len(terminalContainerStatuses))

	for _, status := range terminalContainerStatuses {
		result := r.db.WithContext(ctx).
			Where("status = ?", string(status)).
			Where("COALESCE(stopped_at, started_at) IS NOT NULL").
			Where("COALESCE(stopped_at, started_at) < ?", olderThan).
			Delete(&ContainerModel{})
		if result.Error != nil {
			return deleted, fmt.Errorf("failed to prune %s containers: %w", status, result.Error)
		}
		deleted[status] = result.RowsAffected
	}

	return deleted, nil
}

// ListAll lists all containers, optionally filtered by player
func (r *ContainerRepositoryGORM) ListAll(
	ctx context.Context,
	playerID *int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx)

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	return models, nil
}

// ContainerSummary is an internal query result struct for simplified container lookups.
// For full container data, use the Container domain entity.
// This struct is used by coordinators to check container status efficiently.
type ContainerSummary struct {
	ID            string
	ContainerType string
	Status        string
}

// ScoutWorkerSummary is one RUNNING scout worker container (tour or reposition
// relay) with the coordinator_id its persisted config carries — empty for a
// manually-launched tour, which no reconciler may ever stop.
type ScoutWorkerSummary struct {
	ID            string
	CoordinatorID string
}

// scoutWorkerCommandTypes are the two container command types the scout-post
// coordinator spawns as managed workers. Kept in lockstep with the Add calls in
// PersistScoutTourWorker / PersistScoutRepositionWorker.
var scoutWorkerCommandTypes = []string{"scout_tour", "scout_reposition"}

// ListRunningScoutWorkers returns every RUNNING scout_tour / scout_reposition
// container for the player, each with the coordinator_id parsed from its
// persisted config. It is the scout-post reconciler's container-side view: the
// slot-driven passes can only see workers a post references, so a worker whose
// post was removed needs this read to be found at all. An unparseable config
// reads as "" — the conservative side, since an unidentifiable worker must
// never be stopped.
func (r *ContainerRepositoryGORM) ListRunningScoutWorkers(
	ctx context.Context,
	playerID shared.PlayerID,
) ([]ScoutWorkerSummary, error) {
	var models []*ContainerModel
	err := r.db.WithContext(ctx).
		Where("status = ? AND player_id = ? AND command_type IN ?", containerStatusRunning, playerID.Value(), scoutWorkerCommandTypes).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list running scout workers: %w", err)
	}

	summaries := make([]ScoutWorkerSummary, 0, len(models))
	for _, m := range models {
		var cfg struct {
			CoordinatorID string `json:"coordinator_id"`
		}
		_ = json.Unmarshal([]byte(m.Config), &cfg)
		summaries = append(summaries, ScoutWorkerSummary{ID: m.ID, CoordinatorID: cfg.CoordinatorID})
	}
	return summaries, nil
}

// ListJumpContainersForShip returns the IDs of every JUMP container row the player holds that
// NAMES this hull in its config, in any status. It backs jump_ship's post-claim reap.
//
// Matching is on config["ship_symbol"], never on an ID prefix. Jump IDs are "ship-jump-<symbol>-
// <nonce>", and hull symbols are not prefix-free — "ship-jump-TORWIND-2" is a prefix of
// "ship-jump-TORWIND-23-..." — so a LIKE 'ship-jump-<symbol>%' would reap another hull's live
// claim record. The config field is the hull the row was created FOR, which is exactly the
// question being asked.
//
// STATUS IS DELIBERATELY UNSCOPED. A jump row's status never advances: it is written PENDING and
// deleted on the way out, so status carries no information about whether a jump is in flight —
// the hull's own claim does, and the caller holds it. Filtering by status here would only miss
// rows that a claim-break already terminalized to STOPPED, which leak exactly like the PENDING
// ones.
func (r *ContainerRepositoryGORM) ListJumpContainersForShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) ([]string, error) {
	var models []*ContainerModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND container_type = ?", playerID, string(container.ContainerTypeJump)).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list jump containers for ship %s: %w", shipSymbol, err)
	}

	ids := make([]string, 0, len(models))
	for _, m := range models {
		var cfg struct {
			ShipSymbol string `json:"ship_symbol"`
		}
		// An unparseable config names no hull and is therefore never matched: a row we
		// cannot attribute is left alone rather than reaped against the wrong hull.
		if err := json.Unmarshal([]byte(m.Config), &cfg); err != nil {
			continue
		}
		if cfg.ShipSymbol == shipSymbol {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// HasActiveContainerOfType reports whether any container of the given types is currently RUNNING
// or PENDING for the player. It backs the per-operation capital budget's hasWork sensor
// (common.EngineCapitalWorkSensor, sp-ftqgp): the trade and construction spend guards each ask
// whether the OTHER side is live before sizing their own share of deployable capital.
//
// PENDING counts alongside RUNNING for the same reason the bootstrap gate's containerTypeRunning
// counts it: a container about to start is a side about to spend, so treating it as idle would
// briefly hand the asking side 100% of the treasury during every launch window. An empty type
// list matches nothing (false) rather than everything — a caller that resolved no types must
// never be told the whole fleet is busy.
func (r *ContainerRepositoryGORM) HasActiveContainerOfType(
	ctx context.Context,
	playerID int,
	containerTypes ...string,
) (bool, error) {
	if len(containerTypes) == 0 {
		return false, nil
	}

	var count int64
	err := r.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("player_id = ?", playerID).
		Where("status IN ?", []string{
			string(container.ContainerStatusRunning),
			string(container.ContainerStatusPending),
		}).
		Where("container_type IN ?", containerTypes).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check for active containers by type: %w", err)
	}
	return count > 0, nil
}

// ListActiveByTypeSimple returns every RUNNING-or-PENDING container of the given types for one
// player, in ONE query.
//
// THE SINGLE QUERY IS THE CONTRACT, NOT AN OPTIMISATION. Reading the two live statuses as two
// separate queries leaves a row that transitions PENDING → RUNNING between them invisible to BOTH:
// it is no longer PENDING when the second query runs, and was not yet RUNNING when the first one
// did. Every once-only launch guard in the daemon is built on this read, and a launch is exactly
// when a row makes that transition — so the gap is a window in which a second coordinator of a
// single-instance type starts and, if it is a spender, bids against the first over one treasury.
// One statement sees one snapshot and has no gap.
//
// PENDING counts as live for the same reason HasActiveContainerOfType counts it: a container about
// to start is a spender about to spend. An empty type list matches nothing rather than everything.
func (r *ContainerRepositoryGORM) ListActiveByTypeSimple(
	ctx context.Context,
	playerID int,
	containerTypes ...string,
) ([]ContainerSummary, error) {
	if len(containerTypes) == 0 {
		return nil, nil
	}

	var models []*ContainerModel
	err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Where("status IN ?", []string{
			string(container.ContainerStatusRunning),
			string(container.ContainerStatusPending),
		}).
		Where("container_type IN ?", containerTypes).
		Find(&models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list active containers by type: %w", err)
	}

	result := make([]ContainerSummary, len(models))
	for i, model := range models {
		result[i] = ContainerSummary{
			ID:            model.ID,
			ContainerType: model.ContainerType,
			Status:        model.Status,
		}
	}
	return result, nil
}

// ListByStatusSimple returns simplified container info (for coordinators)
func (r *ContainerRepositoryGORM) ListByStatusSimple(
	ctx context.Context,
	status string,
	playerID *int,
) ([]ContainerSummary, error) {
	var models []*ContainerModel

	query := r.db.WithContext(ctx).Where("status = ?", status)

	if playerID != nil {
		query = query.Where("player_id = ?", *playerID)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list containers by status: %w", err)
	}

	result := make([]ContainerSummary, len(models))
	for i, model := range models {
		result[i] = ContainerSummary{
			ID:            model.ID,
			ContainerType: model.ContainerType,
			Status:        model.Status,
		}
	}

	return result, nil
}

// FindChildContainers retrieves all direct children of a parent container
// Returns empty slice if no children found (not an error)
func (r *ContainerRepositoryGORM) FindChildContainers(
	ctx context.Context,
	parentContainerID string,
	playerID int,
) ([]*ContainerModel, error) {
	var models []*ContainerModel

	err := r.db.WithContext(ctx).
		Where("parent_container_id = ? AND player_id = ?", parentContainerID, playerID).
		Order("started_at ASC"). // Oldest children first for consistent ordering
		Find(&models).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find child containers: %w", err)
	}

	return models, nil
}

// FindActiveCoordinatorByType finds an active (PENDING or RUNNING) coordinator of
// the given type for a player, regardless of system. It deliberately applies no
// system filter — the contract coordinator is not system-scoped, so the live
// `fleet hub` mutation locates it by type alone. Returns nil if none is active.
//
// With >=2 active rows of the same type the result is made deterministic by
// Order("heartbeat_at DESC") — the freshest (most-recently heartbeating)
// coordinator wins, so a live mutation never lands on a stale row on the whim of
// the DB's default ordering.
func (r *ContainerRepositoryGORM) FindActiveCoordinatorByType(
	ctx context.Context,
	containerType string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	result := r.db.WithContext(ctx).
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			containerType, playerID, containerStatusPending, containerStatusRunning).
		Order("heartbeat_at DESC").
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active coordinator by type: %w", result.Error)
	}

	return &model, nil
}

// FindMostRecentByType returns the most-recently-STARTED container of a type for a
// player REGARDLESS of status (STOPPED/INTERRUPTED included), or nil if none exists.
// Unlike FindActiveCoordinatorByType it applies NO PENDING/RUNNING filter — it is the
// source of the last persisted live-config a coordinator (re)start re-applies:
// relaunching a previously-stopped coordinator via `frontier start` must re-adopt its
// live-tuned knobs (the persisted config column) instead of reverting to config-file
// defaults, exactly as the daemon-restart recovery path already does. Ordered by
// started_at DESC (the freshest launch's config wins; StartedAt is set on every Add).
func (r *ContainerRepositoryGORM) FindMostRecentByType(
	ctx context.Context,
	containerType string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	result := r.db.WithContext(ctx).
		Where("container_type = ? AND player_id = ?", containerType, playerID).
		Order("started_at DESC").
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find most recent container by type: %w", result.Error)
	}

	return &model, nil
}

// FindActiveGasCoordinator finds an active (PENDING or RUNNING) gas coordinator
// for the specified gas giant. Returns nil if none found.
// Used to enforce singleton gas coordinators per gas giant.
func (r *ContainerRepositoryGORM) FindActiveGasCoordinator(
	ctx context.Context,
	gasGiant string,
	playerID int,
) (*ContainerModel, error) {
	var model ContainerModel

	result := r.db.WithContext(ctx).
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			"GAS_COORDINATOR", playerID, containerStatusPending, containerStatusRunning).
		Where("config LIKE ?", fmt.Sprintf(`%%"gas_giant":"%s"%%`, gasGiant)).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find active gas coordinator: %w", result.Error)
	}

	return &model, nil
}
