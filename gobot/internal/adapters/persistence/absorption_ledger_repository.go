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

// AbsorptionLedgerGORM also implements the optional absorption.HolderLister capability.
var _ absorption.HolderLister = (*AbsorptionLedgerGORM)(nil)

// Absorption-ledger tuning. All of them flow in as config (RULINGS #5) — the constants
// are only the fail-safe defaults NewAbsorptionLedger applies when a caller passes the
// zero value.
const (
	// DefaultExecutedHardCap bounds how long an EXECUTED recovery shadow may block
	// before the sweep wipes it regardless of decay.
	//
	// 4h, recalibrated from 12h. The 12h was derived as "≈ two half-lives of the
	// slowest TAGGED tier (RESTRICTED, ~6.9h)", with the cap intended as "the belt to the decay
	// curve's suspenders" — a backstop that should almost never be what clears a shadow.
	// sp-ircfy's era-07-19 re-fit raised every half-life 4-6x (RESTRICTED 6.9h → 39.4h, WEAK →
	// 26.6h) and INVERTED that: 12h became well under ONE half-life for every tier, so decay can
	// no longer clear anything first and the cap became the ONLY release mechanism. Measured
	// live: 1042 EXECUTED rows at an average life of 12.4h — pinned at the cap, not decaying out.
	//
	// So this constant is no longer a backstop above the decay curve; it IS the embargo duration,
	// and it is therefore calibrated against MEASURED sink recovery rather than re-derived from
	// half-lives. Those two are not in conflict — a half-life is the decay RATE of a price
	// displacement, and ours are ~1% — so the absolute time to economic recovery is short even
	// though the rate is slow. Live era-5 telemetry (player 5, 2026-07-29):
	//   * repeat sales into the same (sink, good) at MEDIAN 1.15h apart; realized price 99.2% of
	//     the previous sale at <2h, 100.3% at 2-6h, 100.9% at 6-12h — recovery completes by ~2h;
	//   * 255 of 386 revisits went into sinks HOLDING an active EXECUTED shadow and returned
	//     99.7% of the previous price vs 99.6% for unshadowed sinks — the shadow tracks no
	//     economically real depletion. That comparison is the de-confounder: the price evidence
	//     is not just a selection effect of the embargo doing its job;
	//   * realized sell price is 101.6% of the market's currently-quoted bid — no depression.
	// 4h is ~2x the ~2h full-recovery time and ~3.5x the median revisit gap, leaving margin for a
	// genuinely large sale (a 6-11x larger fleet will displace more than today's ~1%) without
	// re-creating the embargo. RETUNE THIS when the half-lives are re-fitted or the fleet grows
	// enough to move realized-vs-quoted price off ~100%; the pinning test names that trigger.
	//
	// NOT removable (RULINGS #4): it is the fail-CLOSED outer bound on a shadow whose decay data
	// is missing or stale — an untagged sink with no pooled fit stays UNDECAYED until this sweep.
	// Shortening frees depth sooner; removing uncaps the failure mode. config.yaml's
	// absorption.executed_hard_cap still overrides without a rebuild.
	DefaultExecutedHardCap = 4 * time.Hour
	// DefaultShadowFloorFraction is the fraction of one tranche (trade_volume) of
	// still-occupied depth below which a recovering shadow STOPS blocking a new sell.
	// 0.5 — at 50% of a tranche recovered a new sell takes the recovered half-tranche
	// near full price with the 80%-of-quote floor armed downstream; earlier unblocking
	// rebuilds ladders, later strands capital. Expressed as a fraction of DEPTH (not
	// wall-clock), so it composes with the per-tier decay curve.
	DefaultShadowFloorFraction = 0.5

	// DefaultBuyShadowLife bounds how long the fleet's own purchases keep occupying a
	// SOURCE's depth — separate from DefaultExecutedHardCap because the two sides recover on
	// different clocks. 60 minutes is the horizon the ask-premium-per-already-taken-tranche
	// fit is stated on, and at two tranches that premium is the whole realized trade margin.
	DefaultBuyShadowLife = time.Hour

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
// back the just-inserted plan when others' decayed outstanding on any of its sinks has
// reached the cap. It never escapes Reserve — it becomes (ok=false, err=nil), keeping
// a lawful cap breach distinct from a real database error (RULINGS #4: the money
// guard fails CLOSED — a breach parks the plan, it does not error the daemon).
var errAbsorptionBreach = errors.New("absorption reservation would breach a sink's depth cap")

// AbsorptionLedgerConfig carries the trade-analyst-ruled knobs (Q2). Zero values
// take the package defaults (NewAbsorptionLedger fills them), so a caller may set
// only the fields it wants to override.
type AbsorptionLedgerConfig struct {
	ExecutedHardCap     time.Duration
	ShadowFloorFraction float64
	BuyShadowLife       time.Duration
}

func (c AbsorptionLedgerConfig) withDefaults() AbsorptionLedgerConfig {
	if c.ExecutedHardCap <= 0 {
		c.ExecutedHardCap = DefaultExecutedHardCap
	}
	if c.ShadowFloorFraction <= 0 {
		c.ShadowFloorFraction = DefaultShadowFloorFraction
	}
	if c.BuyShadowLife <= 0 {
		c.BuyShadowLife = DefaultBuyShadowLife
	}
	return c
}

// executedLifeFor is how long a converted shadow blocks on each side of a market.
func (c AbsorptionLedgerConfig) executedLifeFor(side string) time.Duration {
	if side == absorption.SideBuy {
		return c.BuyShadowLife
	}
	return c.ExecutedHardCap
}

// ContainerLivenessProvider reports which container IDs are currently live for a
// player, so the reserve sweep can reclaim a PLANNED row whose owning container has
// died without releasing it. Optional: a nil provider (or a read error) disables
// dead-container reclaim for that pass — the safe direction, since a hold we cannot
// confirm dead is left to its TTL rather than freed. A narrow port so the ledger
// stays a pure persistence type, faked trivially in tests.
type ContainerLivenessProvider interface {
	LiveContainerIDs(ctx context.Context, playerID int) (map[string]struct{}, error)
}

// laneKeyOf projects an entry onto its pool key.
func laneKeyOf(e absorption.ReserveEntry) absorption.LaneKey {
	return absorption.LaneKey{Waypoint: e.Waypoint, Good: e.Good, Side: e.Side}
}

// AbsorptionLedgerGORM is the DB-backed cross-engine absorption ledger. It is the
// ONLY place cross-container absorption coordination can live: the market cache
// reflects only EXECUTED trades seconds late, and an in-memory coordinator loses the
// recovery shadow on the daemon restart that co-dumps punish (design §1). Modeled on
// the proven SpendReservationLedgerGORM: insert-then-check under a per-player
// advisory lock, release on every exit, a self-cleaning sweep inside Reserve with no
// background job.
type AbsorptionLedgerGORM struct {
	db       *gorm.DB
	recovery *absorptionRecoveryModel
	cfg      AbsorptionLedgerConfig
	liveness ContainerLivenessProvider
	depth    absorption.SinkDepthScaling
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
		depth:    absorption.DefaultSinkDepthScaling(),
	}
}

// Reserve records a plan's PLANNED absorption and reports whether every sink still
// clears its depth cap: for each entry's (waypoint, good, side), decayed outstanding
// across ALL states and containers EXCLUDING the rows this call inserts must be under
// CapUnits — the cap bounds others' depth. It is all-or-nothing (any breach rolls back
// the WHOLE plan) and serialized per player, so the snapshot→accept race that a co-dump
// exploits is closed exactly as the spend ledger closes check→buy.
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

		// Check each DISTINCT sink once against OTHER containers' outstanding depth: this
		// call's own rows are netted back out, so a plan's size never decides its admission
		// (sp-6zqza); the reserver's earlier rows and own shadows still count. Cap = max per key.
		caps := map[absorption.LaneKey]int{}
		own := map[absorption.LaneKey]int{}
		for _, entry := range entries {
			k := laneKeyOf(entry)
			if entry.CapUnits > caps[k] || caps[k] == 0 {
				caps[k] = entry.CapUnits
			}
			own[k] += entry.Units
		}
		for k, capUnits := range caps {
			occupied, e := r.occupiedDepthTx(tx, playerID, k, now)
			if e != nil {
				return e
			}
			if occupied-float64(own[k]) >= float64(capUnits) {
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

	breadth := r.sinkBreadthFor(r.db.WithContext(ctx), playerID, rows)
	out := make(map[absorption.LaneKey]absorption.KeyOccupancy, len(rows))
	executed := make(map[absorption.LaneKey][]*MarketAbsorptionLedgerModel, len(rows))
	for i := range rows {
		row := &rows[i]
		k := absorption.LaneKey{Waypoint: row.Waypoint, Good: row.Good, Side: row.Side}
		switch row.State {
		case absorptionStatePlanned:
			occ := out[k]
			occ.PlannedUnits += row.Units
			out[k] = occ
		case absorptionStateExecuted:
			executed[k] = append(executed[k], row)
		}
	}
	for k, pool := range executed {
		occ := out[k]
		occ.RecoveringResidual = r.executedPoolResidual(k.Side, pool, now, breadth)
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
// A zero-unit sale leaves NO shadow: the PLANNED row is deleted, exactly as if the leg
// had released it. An UNTAGGED sink DOES leave one, decaying on the artifact's pooled
// fit — without it consecutive plans saw virgin depth and rebuilt the whole tranche
// ladder there (the cross-plan A-cap gap). Idempotent: a retry after
// conversion finds no PLANNED row and is a no-op. A missing row (already swept) is not
// an error.
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

	if realizedUnits <= 0 {
		// Nothing sold, so nothing to recover from: release the in-flight hold.
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
			"expires_at":    now.Add(r.cfg.executedLifeFor(key.Side)),
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

// ReleaseByContainer drops every still-PLANNED reservation a container holds in one
// statement (the tour re-plan/restart de-dup seam). EXECUTED recovery shadows are
// deliberately EXCLUDED (state = PLANNED filter): a converted shadow is real market
// damage still recovering, which the container's own next plan must keep avoiding —
// only the in-flight intent is stale. Deleting zero rows is a no-op, so a fresh-launch
// (nothing planned yet) or a double-release is safe. Returns the count dropped.
func (r *AbsorptionLedgerGORM) ReleaseByContainer(ctx context.Context, containerID string, playerID int) (int, error) {
	if containerID == "" {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ? AND state = ?", containerID, playerID, absorptionStatePlanned).
		Delete(&MarketAbsorptionLedgerModel{})
	if result.Error != nil {
		return 0, fmt.Errorf("release planned absorption for container %s: %w", containerID, result.Error)
	}
	return int(result.RowsAffected), nil
}

// ReleaseByContainerExcept drops the container's still-PLANNED rows EXCEPT those whose
// (waypoint, good, side) is in keep — the laden re-plan seam that preserves the sink
// rows backing cargo already in the hold (sp-pcxju). A nil/empty keep is exactly
// ReleaseByContainer. EXECUTED shadows are excluded (state = PLANNED filter). Returns
// the count dropped.
func (r *AbsorptionLedgerGORM) ReleaseByContainerExcept(ctx context.Context, containerID string, playerID int, keep []absorption.LaneKey) (int, error) {
	if containerID == "" {
		return 0, nil
	}
	if len(keep) == 0 {
		return r.ReleaseByContainer(ctx, containerID, playerID)
	}
	keepSet := make(map[absorption.LaneKey]struct{}, len(keep))
	for _, k := range keep {
		keepSet[k] = struct{}{}
	}
	var rows []MarketAbsorptionLedgerModel
	if err := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ? AND state = ?", containerID, playerID, absorptionStatePlanned).
		Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("scan planned absorption for selective release: %w", err)
	}
	var dropIDs []string
	for i := range rows {
		row := &rows[i]
		if _, kept := keepSet[absorption.LaneKey{Waypoint: row.Waypoint, Good: row.Good, Side: row.Side}]; kept {
			continue
		}
		dropIDs = append(dropIDs, row.ID)
	}
	if len(dropIDs) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).Where("id IN ?", dropIDs).Delete(&MarketAbsorptionLedgerModel{})
	if result.Error != nil {
		return 0, fmt.Errorf("selective release planned absorption for container %s: %w", containerID, result.Error)
	}
	return int(result.RowsAffected), nil
}

// HeldByContainer returns one container's own still-PLANNED units per (waypoint, good,
// side), non-expired — the firm sell-depth an executor consults before buying (sp-pcxju)
// and the own-subtraction the laden re-plan nets with. EXECUTED shadows are excluded
// (recovering history, not a live in-flight hold); expired rows are filtered here (the
// sweep inside Reserve physically deletes them). An empty map means no held reservation.
func (r *AbsorptionLedgerGORM) HeldByContainer(ctx context.Context, containerID string, playerID int) (map[absorption.LaneKey]int, error) {
	out := map[absorption.LaneKey]int{}
	if containerID == "" {
		return out, nil
	}
	now := time.Now()
	var rows []MarketAbsorptionLedgerModel
	if err := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ? AND state = ? AND expires_at > ?",
			containerID, playerID, absorptionStatePlanned, now).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read held absorption for container %s: %w", containerID, err)
	}
	for i := range rows {
		row := &rows[i]
		out[absorption.LaneKey{Waypoint: row.Waypoint, Good: row.Good, Side: row.Side}] += row.Units
	}
	return out, nil
}

// HoldersForKeys returns, per requested key, the individual non-expired rows
// backing its occupied depth — a refusal log's attribution read, never the hot
// Reserve loop. keys is expected small (one plan's contended sinks), so this
// runs one indexed query per key rather than one OR-composite across columns.
func (r *AbsorptionLedgerGORM) HoldersForKeys(ctx context.Context, playerID int, keys []absorption.LaneKey) (map[absorption.LaneKey][]absorption.Holder, error) {
	out := make(map[absorption.LaneKey][]absorption.Holder, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	now := time.Now()
	for _, k := range keys {
		var rows []MarketAbsorptionLedgerModel
		if err := r.db.WithContext(ctx).Where(
			"player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND side = ? AND expires_at > ?",
			playerID, k.Waypoint, k.Good, k.Side, now,
		).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read holders for %s/%s/%s: %w", k.Waypoint, k.Good, k.Side, err)
		}
		breadth := r.sinkBreadthFor(r.db.WithContext(ctx), playerID, rows)
		var holders []absorption.Holder
		executed := make([]*MarketAbsorptionLedgerModel, 0, len(rows))
		for i := range rows {
			row := &rows[i]
			switch row.State {
			case absorptionStatePlanned:
				holders = append(holders, absorption.Holder{
					ContainerID:  row.ContainerID,
					Engine:       row.Engine,
					State:        absorptionStatePlanned,
					Units:        row.Units,
					TTLRemaining: row.ExpiresAt.Sub(now),
				})
			case absorptionStateExecuted:
				executed = append(executed, row)
			}
		}
		// The SAME pool arithmetic occupiedDepthTx uses for the cap check, so a holder
		// reported here is exactly one Reserve would have counted — never a phantom
		// "blocker" the real gate already ignores, and never an empty list for a pool
		// that blocks only on its rows' accumulated depth.
		shares, _ := r.executedPoolShares(k.Side, executed, now, breadth)
		for i, row := range executed {
			if shares[i] <= 0 {
				continue
			}
			holders = append(holders, absorption.Holder{
				ContainerID:  row.ContainerID,
				Engine:       row.Engine,
				State:        absorptionStateExecuted,
				Units:        int(shares[i]),
				TTLRemaining: row.ExpiresAt.Sub(now),
			})
		}
		if len(holders) > 0 {
			out[k] = holders
		}
	}
	return out, nil
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
// past the floor contributes nothing). The reserve cap check compares this, less the
// reserving call's own rows, against CapUnits.
func (r *AbsorptionLedgerGORM) occupiedDepthTx(tx *gorm.DB, playerID int, key absorption.LaneKey, now time.Time) (float64, error) {
	var rows []MarketAbsorptionLedgerModel
	if err := tx.Where(
		"player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND side = ? AND expires_at > ?",
		playerID, key.Waypoint, key.Good, key.Side, now,
	).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("read sink depth for cap check: %w", err)
	}
	breadth := r.sinkBreadthFor(tx, playerID, rows)
	var occupied float64
	executed := make([]*MarketAbsorptionLedgerModel, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		switch row.State {
		case absorptionStatePlanned:
			occupied += float64(row.Units)
		case absorptionStateExecuted:
			executed = append(executed, row)
		}
	}
	return occupied + r.executedPoolResidual(key.Side, executed, now, breadth), nil
}

// executedPoolResidual is the depth one pool's EXECUTED shadows still occupy.
func (r *AbsorptionLedgerGORM) executedPoolResidual(
	side string, rows []*MarketAbsorptionLedgerModel, now time.Time, sinkListings map[string]int,
) float64 {
	_, total := r.executedPoolShares(side, rows, now, sinkListings)
	return total
}

// executedPoolShares decays every EXECUTED row in one pool to now and reports each row's
// share of the occupied depth alongside the pool total.
//
// The pool is floored ONCE, against the deepest tranche its rows were written against,
// rather than row by row. Crowding on EITHER side of a market is MANY ordinary legs, each
// lawful alone and each under the per-row floor; judged row by row the pool reports zero
// occupied depth while the fleet's own flow moves the price against it. Flooring once still
// leaves a lone small leg unable to close a market.
//
// The sides differ only in the listing-breadth crush discount. A SALE's claim on a sink
// scales with the sink's breadth, the prior being fitted there; a PURCHASE consumes ONE
// good's supply, which breadth does not replenish, so it takes the uniform prior
// (RULINGS #4). A row with no stored tranche size has no floor to fall under, so it blocks
// on any positive residual and earns no discount either.
//
// A pool under its floor occupies nothing and every share is zero, so the attribution read
// and the cap check can never disagree about who holds depth.
func (r *AbsorptionLedgerGORM) executedPoolShares(
	side string, rows []*MarketAbsorptionLedgerModel, now time.Time, sinkListings map[string]int,
) ([]float64, float64) {
	shares := make([]float64, len(rows))
	var total float64
	floorTranche, unfloorable := 0, false
	for i, row := range rows {
		if row.ExecutedAt == nil {
			continue
		}
		decayed := r.recovery.decayedUnits(row.Units, row.TierAtWrite, now.Sub(*row.ExecutedAt))
		if row.TrancheSize <= 0 {
			shares[i], total, unfloorable = decayed, total+decayed, true
			continue
		}
		if side != absorption.SideBuy {
			decayed *= r.depth.CrushScale(sinkListings[row.Waypoint])
		}
		shares[i], total = decayed, total+decayed
		if row.TrancheSize > floorTranche {
			floorTranche = row.TrancheSize
		}
	}
	if !unfloorable && total < r.cfg.ShadowFloorFraction*float64(floorTranche) {
		return make([]float64, len(rows)), 0
	}
	return shares, total
}
