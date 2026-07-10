package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// AbsorptionLedgerGORM implements the domain absorption.Ledger port.
var _ absorption.Ledger = (*AbsorptionLedgerGORM)(nil)

// Absorption-ledger tuning. All three are trade-analyst rulings (sp-78ai Q2) and
// flow in as config (RULINGS #5) — the constants are only the fail-safe defaults
// NewAbsorptionLedger applies when a caller passes the zero value.
const (
	// DefaultExecutedHardCap bounds how long an EXECUTED recovery shadow may block
	// before the sweep wipes it regardless of decay. Trade-analyst Q2: 12h, NOT 24h
	// — 24h is >half the remaining era, so a stuck shadow would embargo a sink for
	// most of the game; 12h ≈ two half-lives of the slowest TAGGED tier (RESTRICTED,
	// ~6.9h) and, with probes re-reading hourly, the cap should almost never be what
	// clears a shadow. It is the belt to the decay curve's suspenders.
	DefaultExecutedHardCap = 12 * time.Hour
	// DefaultShadowFloorFraction is the fraction of one tranche (trade_volume) of
	// still-occupied depth below which a recovering shadow STOPS blocking a new sell.
	// Trade-analyst Q2: 0.5 — at 50% of a tranche recovered a new sell takes the
	// recovered half-tranche near full price with the bp6f 80%-of-quote floor armed
	// downstream; earlier unblocking rebuilds ladders, later strands capital.
	// Expressed as a fraction of DEPTH (not wall-clock), so it composes with the
	// per-tier decay curve.
	DefaultShadowFloorFraction = 0.5

	// absorptionReclaimGrace is the small margin before a PLANNED row whose container
	// is absent from the live set is reclaimed as a dead-container leak. Liveness is
	// the primary signal (design §1: age alone cannot distinguish dead, since a
	// healthy reservation legitimately lives the whole flight), and this grace only
	// guards the snapshot race where liveness was read microseconds before a fresh
	// container row committed.
	absorptionReclaimGrace = 30 * time.Second

	// absorptionAdvisoryNamespace is the fixed first key of the Postgres
	// transaction-scoped advisory lock that serializes concurrent reserves per
	// player, distinct from the spend ledger's "SPND" so the two cannot collide.
	// Value is the ASCII of "ABSB" (fits int4, the advisory-lock key type).
	absorptionAdvisoryNamespace = 0x41425342 // "ABSB"

	absorptionStatePlanned  = "PLANNED"
	absorptionStateExecuted = "EXECUTED"
)

// errAbsorptionBreach is a sentinel returned inside the reserve transaction to roll
// back the just-inserted plan when any sink's decayed outstanding + this plan would
// exceed its cap. It never escapes Reserve — it becomes (ok=false, err=nil), keeping
// a lawful cap breach distinct from a real database error (RULINGS #4: the money
// guard fails CLOSED — a breach parks the plan, it does not error the daemon).
var errAbsorptionBreach = errors.New("absorption reservation would breach a sink's depth cap")

// AbsorptionLedgerConfig carries the trade-analyst-ruled knobs (Q2). Zero values
// take the package defaults (NewAbsorptionLedger fills them), so a caller may set
// only the fields it wants to override.
type AbsorptionLedgerConfig struct {
	ExecutedHardCap     time.Duration
	ShadowFloorFraction float64
}

func (c AbsorptionLedgerConfig) withDefaults() AbsorptionLedgerConfig {
	if c.ExecutedHardCap <= 0 {
		c.ExecutedHardCap = DefaultExecutedHardCap
	}
	if c.ShadowFloorFraction <= 0 {
		c.ShadowFloorFraction = DefaultShadowFloorFraction
	}
	return c
}

// ContainerLivenessProvider reports which container IDs are currently live for a
// player, so the reserve sweep can reclaim a PLANNED row whose owning container has
// died without releasing it (the sp-vjwb orphan-reconcile idiom). Optional: a nil
// provider (or a read error) disables dead-container reclaim for that pass — the
// safe direction, since a hold we cannot confirm dead is left to its TTL rather
// than freed. A narrow port so the ledger stays a pure persistence type, faked
// trivially in tests.
type ContainerLivenessProvider interface {
	LiveContainerIDs(ctx context.Context, playerID int) (map[string]struct{}, error)
}

// laneKeyOf projects an entry onto its pool key.
func laneKeyOf(e absorption.ReserveEntry) absorption.LaneKey {
	return absorption.LaneKey{Waypoint: e.Waypoint, Good: e.Good, Side: e.Side}
}

// AbsorptionLedgerGORM is the DB-backed cross-engine absorption ledger (sp-78ai
// L1). It is the ONLY place cross-container absorption coordination can live: the
// market cache reflects only EXECUTED trades seconds late, and an in-memory
// coordinator loses the recovery shadow on the daemon restart that co-dumps punish
// (design §1). Modeled on the proven SpendReservationLedgerGORM (sp-w3he):
// insert-then-check under a per-player advisory lock, release on every exit, a
// self-cleaning sweep inside Reserve with no background job.
type AbsorptionLedgerGORM struct {
	db       *gorm.DB
	recovery *absorptionRecoveryModel
	cfg      AbsorptionLedgerConfig
	liveness ContainerLivenessProvider
}

// NewAbsorptionLedger builds a ledger that reads recovery half-lives from the
// fitted market artifact at recoveryArtifactPath (empty or unreadable → reads fail
// closed, treating EXECUTED residuals as undecayed until the hard cap). A nil
// liveness provider disables dead-container reclaim (TTL still bounds every row).
func NewAbsorptionLedger(db *gorm.DB, recoveryArtifactPath string, cfg AbsorptionLedgerConfig, liveness ContainerLivenessProvider) *AbsorptionLedgerGORM {
	return &AbsorptionLedgerGORM{
		db:       db,
		recovery: loadAbsorptionRecoveryModel(recoveryArtifactPath),
		cfg:      cfg.withDefaults(),
		liveness: liveness,
	}
}

// Reserve records a plan's PLANNED absorption and reports whether every sink still
// clears its depth cap: for each entry's (waypoint, good, side), decayed outstanding
// across ALL states and containers (including the rows just inserted) + this plan
// ≤ CapUnits. It is all-or-nothing (any breach rolls back the WHOLE plan) and
// serialized per player, so the snapshot→accept race that a co-dump exploits is
// closed exactly as the spend ledger closes check→buy.
//
// On ok==true the returned reservationIDs (one per entry, in order) identify the
// PLANNED rows the caller must Release or Convert. On ok==false the plan is rolled
// back (nothing persisted) and the caller re-plans against a fresh snapshot that now
// shows the contested sink occupied. Dead-container reclaim and TTL/hard-cap expiry
// run first, inside the same transaction, so the ledger is self-cleaning.
func (r *AbsorptionLedgerGORM) Reserve(
	ctx context.Context,
	playerID int,
	containerID string,
	engine string,
	entries []absorption.ReserveEntry,
) (reservationIDs []string, ok bool, err error) {
	if len(entries) == 0 {
		return nil, true, nil
	}

	// Read container liveness BEFORE the transaction so the DB is never held open
	// across a repository call, and so a liveness read error degrades to "skip
	// reclaim" rather than failing an otherwise-lawful reserve (reclaim is hygiene;
	// the cap check below is the money guard).
	live := r.liveContainers(ctx, playerID)

	now := time.Now()
	ids := make([]string, len(entries))
	for i := range entries {
		ids[i] = uuid.NewString()
	}

	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := r.acquireAdvisoryLock(tx, playerID); e != nil {
			return e
		}
		if e := r.sweepTx(tx, playerID, now, live); e != nil {
			return e
		}

		for i, entry := range entries {
			row := &MarketAbsorptionLedgerModel{
				ID:          ids[i],
				PlayerID:    playerID,
				ContainerID: containerID,
				Engine:      engine,
				Waypoint:    entry.Waypoint,
				Good:        entry.Good,
				Side:        entry.Side,
				State:       absorptionStatePlanned,
				Units:       entry.Units,
				TierAtWrite: entry.Tier,
				QuotedPrice: entry.QuotedPrice,
				CreatedAt:   now,
				ExpiresAt:   now.Add(entry.TTL),
			}
			if e := tx.Create(row).Error; e != nil {
				return fmt.Errorf("insert absorption reservation: %w", e)
			}
		}

		// Check each DISTINCT sink once (a plan may name a sink twice; aggregate its
		// requested units). CapUnits is the max across entries for the key — the
		// tightest ceiling the caller asked for.
		caps := map[absorption.LaneKey]int{}
		for _, entry := range entries {
			k := laneKeyOf(entry)
			if entry.CapUnits > caps[k] || caps[k] == 0 {
				caps[k] = entry.CapUnits
			}
		}
		for k, capUnits := range caps {
			occupied, e := r.occupiedDepthTx(tx, playerID, k, now)
			if e != nil {
				return e
			}
			if occupied > float64(capUnits) {
				return errAbsorptionBreach
			}
		}
		ok = true
		return nil
	})

	if errors.Is(txErr, errAbsorptionBreach) {
		return nil, false, nil
	}
	if txErr != nil {
		return nil, false, txErr
	}
	return ids, true, nil
}

// RecordPlanned inserts a single PLANNED row unconditionally — the launch-record
// path for a leg the consult read already cleared (idle-arb, arb-run). It runs the
// same self-cleaning sweep as Reserve (so a dead-container leak is reclaimed on the
// way in) but never rejects: the leg has committed, so this publishes its in-flight
// occupancy rather than gating it a second time (the gate was the batched consult).
func (r *AbsorptionLedgerGORM) RecordPlanned(ctx context.Context, playerID int, containerID, engine string, entry absorption.ReserveEntry) (string, error) {
	live := r.liveContainers(ctx, playerID)
	now := time.Now()
	id := uuid.NewString()

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := r.acquireAdvisoryLock(tx, playerID); e != nil {
			return e
		}
		if e := r.sweepTx(tx, playerID, now, live); e != nil {
			return e
		}
		row := &MarketAbsorptionLedgerModel{
			ID:          id,
			PlayerID:    playerID,
			ContainerID: containerID,
			Engine:      engine,
			Waypoint:    entry.Waypoint,
			Good:        entry.Good,
			Side:        entry.Side,
			State:       absorptionStatePlanned,
			Units:       entry.Units,
			TierAtWrite: entry.Tier,
			QuotedPrice: entry.QuotedPrice,
			CreatedAt:   now,
			ExpiresAt:   now.Add(entry.TTL),
		}
		if e := tx.Create(row).Error; e != nil {
			return fmt.Errorf("record planned absorption: %w", e)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Outstanding returns every player's non-expired absorption pool, keyed by
// (waypoint, good, side), decayed to NOW — the single batched read a consult pass
// nets against market depth (idle-arb per DispatchOnce, trade-route per scanLanes).
// Reads never write: expired rows are filtered here and physically deleted only by
// the sweep inside Reserve.
func (r *AbsorptionLedgerGORM) Outstanding(ctx context.Context, playerID int) (map[absorption.LaneKey]absorption.KeyOccupancy, error) {
	now := time.Now()
	var rows []MarketAbsorptionLedgerModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND expires_at > ?", playerID, now).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read outstanding absorption: %w", err)
	}

	out := make(map[absorption.LaneKey]absorption.KeyOccupancy, len(rows))
	for i := range rows {
		row := &rows[i]
		k := absorption.LaneKey{Waypoint: row.Waypoint, Good: row.Good, Side: row.Side}
		occ := out[k]
		switch row.State {
		case absorptionStatePlanned:
			occ.PlannedUnits += row.Units
		case absorptionStateExecuted:
			occ.RecoveringResidual += r.blockingResidual(row, now)
		}
		out[k] = occ
	}
	return out, nil
}

// ConvertByContainer converts a PLANNED row into an EXECUTED recovery shadow when a
// leg's sale completes, keyed by the container that owns it plus the sink it sold
// into (the arb container knows its own container_id and sell target, not the
// reservationID). realizedUnits is what actually sold, liveTier the sink good's
// activity read live at sale, trancheSize its trade_volume — so the shadow decays
// on the right curve and sizes its own recovery floor.
//
// UNTAGGED sinks or a zero-unit sale leave NO shadow (trade-analyst Q2): the PLANNED
// row is deleted, exactly as if the leg had released it. Idempotent: a retry after
// conversion finds no PLANNED row and is a no-op. A missing row (already swept) is
// not an error.
func (r *AbsorptionLedgerGORM) ConvertByContainer(
	ctx context.Context,
	containerID string,
	playerID int,
	key absorption.LaneKey,
	realizedUnits int,
	liveTier string,
	trancheSize int,
) error {
	now := time.Now()

	if realizedUnits <= 0 || !r.recovery.IsTagged(liveTier) {
		// No lasting shadow: release the in-flight hold. A micro-sale or an unmodelled
		// (untagged) sink records nothing — the model cannot price what it has not fit.
		if err := r.db.WithContext(ctx).
			Where("container_id = ? AND player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND side = ? AND state = ?",
				containerID, playerID, key.Waypoint, key.Good, key.Side, absorptionStatePlanned).
			Delete(&MarketAbsorptionLedgerModel{}).Error; err != nil {
			return fmt.Errorf("release absorption on untagged/empty sale: %w", err)
		}
		return nil
	}

	result := r.db.WithContext(ctx).
		Model(&MarketAbsorptionLedgerModel{}).
		Where("container_id = ? AND player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND side = ? AND state = ?",
			containerID, playerID, key.Waypoint, key.Good, key.Side, absorptionStatePlanned).
		Updates(map[string]interface{}{
			"state":         absorptionStateExecuted,
			"units":         realizedUnits,
			"tier_at_write": liveTier,
			"tranche_size":  trancheSize,
			"executed_at":   now,
			"expires_at":    now.Add(r.cfg.ExecutedHardCap),
		})
	if result.Error != nil {
		return fmt.Errorf("convert absorption to executed shadow: %w", result.Error)
	}
	return nil
}

// Release consumes a PLANNED reservation once its leg exits without a sale (a failed
// launch, a cancel). Deleting a missing row is a no-op — the sweep or a prior
// convert may already have taken it. Release must never fail an otherwise-clean exit.
func (r *AbsorptionLedgerGORM) Release(ctx context.Context, reservationID string) error {
	if reservationID == "" {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Where("id = ?", reservationID).
		Delete(&MarketAbsorptionLedgerModel{}).Error; err != nil {
		return fmt.Errorf("release absorption reservation %s: %w", reservationID, err)
	}
	return nil
}

// Sweep runs the self-cleaning pass (TTL-expired PLANNED, hard-cap-expired EXECUTED,
// dead-container PLANNED reclaim) outside a reserve and returns how many rows it
// reclaimed. Reserve runs the same sweep inside its own transaction on every call,
// so the ledger needs no background job; this is exposed for a dedicated sweep test
// and any external reconcile.
func (r *AbsorptionLedgerGORM) Sweep(ctx context.Context, playerID int) (int, error) {
	live := r.liveContainers(ctx, playerID)
	now := time.Now()
	var reclaimed int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, e := r.sweepReturningTx(tx, playerID, now, live)
		reclaimed = n
		return e
	})
	if err != nil {
		return 0, err
	}
	return reclaimed, nil
}

// --- internals ---

func (r *AbsorptionLedgerGORM) acquireAdvisoryLock(tx *gorm.DB, playerID int) error {
	// Serialize concurrent reserves for this player so insert-then-check is atomic
	// across containers. Transaction-scoped: auto-released on commit or rollback, so
	// a crashing container cannot hold it. SQLite has no analogue and needs none —
	// it serializes all writers globally.
	if tx.Dialector.Name() == "postgres" {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", absorptionAdvisoryNamespace, playerID).Error; e != nil {
			return fmt.Errorf("acquire absorption advisory lock: %w", e)
		}
	}
	return nil
}

// liveContainers reads the live-container set for reclaim, degrading to nil (skip
// reclaim) on a missing provider or a read error — never failing the caller.
func (r *AbsorptionLedgerGORM) liveContainers(ctx context.Context, playerID int) map[string]struct{} {
	if r.liveness == nil {
		return nil
	}
	live, err := r.liveness.LiveContainerIDs(ctx, playerID)
	if err != nil {
		return nil
	}
	return live
}

func (r *AbsorptionLedgerGORM) sweepTx(tx *gorm.DB, playerID int, now time.Time, live map[string]struct{}) error {
	_, err := r.sweepReturningTx(tx, playerID, now, live)
	return err
}

// sweepReturningTx deletes, within tx, every row that no longer holds depth and
// returns the count: TTL-expired PLANNED and hard-cap-expired EXECUTED (both by
// expires_at), then PLANNED rows whose container is absent from the live set past
// the reclaim grace (dead-container leaks). It never touches a live container's
// in-flight hold, nor an EXECUTED shadow before its hard cap.
func (r *AbsorptionLedgerGORM) sweepReturningTx(tx *gorm.DB, playerID int, now time.Time, live map[string]struct{}) (int, error) {
	expired := tx.Where("player_id = ? AND expires_at <= ?", playerID, now).
		Delete(&MarketAbsorptionLedgerModel{})
	if expired.Error != nil {
		return 0, fmt.Errorf("sweep expired absorption rows: %w", expired.Error)
	}
	total := int(expired.RowsAffected)

	// Dead-container reclaim: only when liveness is available (nil → rely on TTL).
	if live == nil {
		return total, nil
	}
	var planned []MarketAbsorptionLedgerModel
	if err := tx.Where("player_id = ? AND state = ? AND created_at <= ?",
		playerID, absorptionStatePlanned, now.Add(-absorptionReclaimGrace)).
		Find(&planned).Error; err != nil {
		return total, fmt.Errorf("scan planned absorption for reclaim: %w", err)
	}
	var deadIDs []string
	for i := range planned {
		if _, ok := live[planned[i].ContainerID]; !ok {
			deadIDs = append(deadIDs, planned[i].ID)
		}
	}
	if len(deadIDs) > 0 {
		reclaim := tx.Where("id IN ?", deadIDs).Delete(&MarketAbsorptionLedgerModel{})
		if reclaim.Error != nil {
			return total, fmt.Errorf("reclaim dead-container absorption rows: %w", reclaim.Error)
		}
		total += int(reclaim.RowsAffected)
	}
	return total, nil
}

// occupiedDepthTx sums a sink's outstanding depth within tx: PLANNED units (full,
// in-flight) plus EXECUTED residual decayed to now and floored (a shadow recovered
// past the floor contributes nothing). This is what the reserve cap check compares
// against CapUnits.
func (r *AbsorptionLedgerGORM) occupiedDepthTx(tx *gorm.DB, playerID int, key absorption.LaneKey, now time.Time) (float64, error) {
	var rows []MarketAbsorptionLedgerModel
	if err := tx.Where(
		"player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND side = ? AND expires_at > ?",
		playerID, key.Waypoint, key.Good, key.Side, now,
	).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("read sink depth for cap check: %w", err)
	}
	var occupied float64
	for i := range rows {
		row := &rows[i]
		switch row.State {
		case absorptionStatePlanned:
			occupied += float64(row.Units)
		case absorptionStateExecuted:
			occupied += r.blockingResidual(row, now)
		}
	}
	return occupied, nil
}

// blockingResidual is an EXECUTED row's decayed occupied depth, but only while it is
// still AT OR ABOVE its own recovery floor (ShadowFloorFraction × trade_volume). A
// shadow that has recovered past the floor no longer blocks — it contributes 0, so a
// new sell may take the recovered depth (trade-analyst Q2). A row with no stored
// tranche size falls back to blocking on any positive residual (fail closed).
func (r *AbsorptionLedgerGORM) blockingResidual(row *MarketAbsorptionLedgerModel, now time.Time) float64 {
	if row.ExecutedAt == nil {
		return 0
	}
	decayed := r.recovery.decayedUnits(row.Units, row.TierAtWrite, now.Sub(*row.ExecutedAt))
	if row.TrancheSize > 0 {
		floor := r.cfg.ShadowFloorFraction * float64(row.TrancheSize)
		if decayed < floor {
			return 0
		}
	}
	return decayed
}
