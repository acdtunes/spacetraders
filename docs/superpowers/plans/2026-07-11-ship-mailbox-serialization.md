# Per-Ship Mailbox Serialization Implementation Plan (probe: sp-60ff → gate → mailbox: sp-eum3; epic sp-7b79)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Stop at the Task 1 decision gate** — Tasks 2-7 only execute after the gate passes.

**Goal:** Every write to a ship's operational state flows through that ship's mailbox — a lazily-spawned per-`(playerID, shipSymbol)` goroutine that executes turns FIFO — so the owning container, the async arrival/cooldown scheduler, and the stuck-ship sweeper can no longer clobber each other's rows. Before building that, a one-day **probe** turns the vestigial `ships.version` column into conflict-detection telemetry and measures how often the race class actually fires in production; the mailbox proceeds only if the counter says it is worth it.

**Architecture:** Ship state lives in the DB (`ships` table); `api.ShipRepository.Save` (`ship_repository.go:826`) is a full-row `UpdateAll` upsert with no locking — last write wins. The confirmed race class (sp-n7yp dockrace; regression tests in `run_trade_route_coordinator_dockrace_test.go`) is the owning container's verb (`Dock`/`Navigate`/...) racing the `ShipStateScheduler`'s async `handleArrival`/`handleCooldownClear`/`sweepStuckShips` writes; a stale full-row `Save` can even clobber a concurrent column-scoped claim/release. Today's mitigation is per-call-site resync-and-retry at the application layer — which masks frequency, hence the probe. Phase 1 puts the mailbox INSIDE `api.ShipRepository` (all five mutating verbs serialized with zero call-site changes), adds a narrow `navigation.ShipMutator` port for external read-modify-write flows, and re-finds fresh row state inside every turn (serialization without re-find would still write stale snapshots). Assignment writes (`ClaimShip`/`ReserveForCaptain`/etc.) stay outside — already column-scoped `Updates` under `SELECT ... FOR UPDATE` (`ship_repository.go:1002`).

**Tech Stack:** Go 1.24, stdlib channels/goroutines, GORM (postgres + sqlite), `supervise.CapturePanic` from sp-i01z (Phase 1 only), existing sqlite test harness (`database.NewTestConnection()`).

## Global Constraints

- Module: `github.com/andrescamacho/spacetraders-go`, Go 1.24. Repo root for paths: `gobot/`.
- **Task 1 (the probe, bead sp-60ff) is standalone** — it needs neither sp-i01z nor any mailbox code, and ships alone.
- **Tasks 2-7 (the mailbox, bead sp-eum3) are double-gated**: (a) the sp-i01z supervision plan must be merged (`internal/infrastructure/supervise` — the mailbox actor loop uses `CapturePanic`, and the scheduler file was already touched there), and (b) the Task 1 decision gate must have read PROCEED.
- Prefix every shell command with `rtk`.
- No new external dependencies. No new config.
- Run the mailbox/Mutate concurrency tests with `-race` (they use real time, never `MockClock`).
- Commit messages reference the phase's bead: `(sp-60ff)` for Task 1, `(sp-eum3)` for Tasks 2-7.
- One goroutine per ship that has ever been mutated this process lifetime; NO idle reaping (fleet is O(hundreds), a parked goroutine is ~4KB — YAGNI, documented).
- Behavior preservation rule: the probe NEVER rejects a write (detection-only, last-write-wins fallback); verbs keep their exact signatures and their "Save failure is a logged warning, not an error" semantics; `ClaimShip`/assignment methods are untouched.

## Sequencing: probe → gate → mailbox

```
Task 1  (sp-60ff, ~1 day)   version tripwire lands alone, deploys with the next daemon roll
   ↓            ≥7 days of normal fleet operation
DECISION GATE   read spacetraders_ship_version_conflicts_total
   ├─ sustained nonzero rate → Tasks 2-7 (sp-eum3) proceed; the counter also gives the
   │                           BEFORE baseline that Task 7 verifies trends to ~0
   └─ flat zero              → bd defer sp-eum3 (and sp-wa7c stays blocked behind it);
                               close sp-60ff recording the reading — the tripwire stays
                               in production as a permanent regression alarm either way
```

Gate mechanics (record the outcome in the bead, whichever way it goes):

- Read the counter after ≥7 days: `rtk curl -s http://localhost:<metrics.port>/metrics | grep ship_version_conflicts_total` (or Grafana/PromQL: `increase(spacetraders_ship_version_conflicts_total[7d])`), and grep the daemon log for the paired `save conflict` ERROR lines to see WHICH ships/paths conflicted.
- **PROCEED** (any sustained nonzero rate — even ~1/day means silent clobbers are happening in steady state): `bd update sp-60ff --append-notes="GATE: <N> conflicts over <days>d — PROCEED" && bd close sp-60ff` then execute Tasks 2-7 under sp-eum3.
- **DEFER** (flat zero for the whole window): `bd update sp-60ff --append-notes="GATE: 0 conflicts over <days>d — DEFER" && bd close sp-60ff && bd defer sp-eum3 --until="+90d"`. Caveat recorded here deliberately: the app layer's resync-and-retry mitigations (the sp-n7yp dockrace family) can mask conflicts that DO occur but get retried over, and races need contention timing to fire — so a zero reading lowers priority, it does not prove absence. The tripwire remains live permanently; if it ever starts ticking, un-defer.

## Race classes this plan closes

| Race | Evidence | Closed by |
|---|---|---|
| Verb (Dock) vs async arrival timer writing the same row | sp-n7yp; `run_trade_route_coordinator_dockrace_test.go:20-27`; `ship_state_scheduler.go:95` TOCTOU | both sides become serialized turns (Tasks 4-6) |
| Stale full-row verb `Save` clobbering a concurrent column-scoped claim/release | `Save` writes ALL columns from the caller's snapshot (`shipToModel` includes assignment fields) | turns re-find fresh state; the transition is applied to the fresh row (Tasks 4-5) |
| Sweeper vs owning container (same TOCTOU as arrival) | `sweepStuckShips` (`ship_state_scheduler.go:275`) does Find→Arrive→Save on ships a container may be flying | sweeper writes become `Mutate` turns (Task 6) |
| Any not-yet-migrated writer still doing Find→mutate→Save | ~20 application files call `shipRepo.Save` (rollout bead sp-wa7c) | Task 1 tripwire makes every remaining clobber loud + counted, immediately |

## File Structure

Phase 0 — probe (Task 1, sp-60ff):
- Modify: `internal/domain/navigation/ship.go` — `PersistedVersion`/`SetPersistedVersion`
- Modify: `internal/adapters/api/ship_repository.go` — version threading in `modelToDomain`/`shipToModel`, guarded `Save`, package conflict counter
- Modify: `internal/adapters/metrics/prometheus_collector.go` + `container_metrics.go` — `ship_version_conflicts_total`
- Create: `internal/adapters/api/ship_repository_version_test.go` — probe tests + the shared sqlite harness helpers (`stubWaypoints`, `newShipWriteTestRepo`, `seedShip`) that Tasks 3-5 reuse

Phase 1 — mailbox (Tasks 2-7, sp-eum3):
- Create: `internal/adapters/api/ship_mailbox.go` — mailbox registry + actor loop (package-internal)
- Create: `internal/adapters/api/ship_mailbox_test.go`
- Create: `internal/adapters/api/ship_repository_mutate_test.go`
- Create: `internal/adapters/api/ship_repository_serialized_verbs_test.go`
- Modify: `internal/domain/navigation/ports.go` — `ShipMutator` port + `ErrSkipSave` sentinel
- Modify: `internal/domain/navigation/ship.go` — `AdoptState`
- Modify: `internal/adapters/api/ship_repository.go` — `mailboxes` field, `Mutate`, the 5 verbs
- Modify: `internal/adapters/grpc/ship_state_scheduler.go` — ctor gains `navigation.ShipMutator`; handlers become turns
- Modify: `internal/adapters/grpc/daemon_server.go:187` — scheduler wiring

---

### Task 1 (PROBE, sp-60ff): `ships.version` conflict tripwire — detection-only CAS

**Files:**
- Modify: `internal/domain/navigation/ship.go` (`persistedVersion` field + accessors, near `Arrive` at line ~416)
- Modify: `internal/adapters/api/ship_repository.go` (`modelToDomain` line ~1301; `shipToModel` line 669; `Save` line 826; package counter)
- Modify: `internal/adapters/metrics/prometheus_collector.go` + `container_metrics.go`
- Test: `internal/adapters/api/ship_repository_version_test.go`

**Interfaces:**
- Consumes: existing `Version` column (`models.go:170`, today hardcoded `1` at `ship_repository.go:675` and `:1458` — vestigial: never read, never checked)
- Produces: `ship.PersistedVersion() int` / `ship.SetPersistedVersion(v int)`; metric `spacetraders_ship_version_conflicts_total`; package counter `var shipVersionConflicts atomic.Int64` (test/introspection hook); shared test helpers `stubWaypoints`, `newShipWriteTestRepo(t) (*ShipRepository, *gorm.DB, shared.PlayerID)`, `seedShip(t, db, playerID, symbol, navStatus, fuelCurrent)`. Tasks 3-5 build on all of these.

**Behavior is preserved**: a detected conflict logs ERROR + counts + falls back to today's last-write-wins upsert. Nothing can start failing because of this task.

- [ ] **Step 1: Write the failing test (including the shared harness helpers)**

```go
// internal/adapters/api/ship_repository_version_test.go
package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// stubWaypoints forces modelToDomain's fallback branch (denormalized model
// coordinates) so tests need no waypoint rows. Embeds the interface: only
// GetWaypoint is overridden. Shared by the mailbox-phase tests too.
type stubWaypoints struct{ system.IWaypointProvider }

func (stubWaypoints) GetWaypoint(_ context.Context, _ string, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// newShipWriteTestRepo mirrors newDedicationTestRepo
// (ship_repository_claim_dedication_test.go:19) but with a waypoint-provider
// stub because these tests exercise FindBySymbol → modelToDomain. Shared by
// the mailbox-phase tests (Tasks 3-5 of the sp-eum3 plan).
func newShipWriteTestRepo(t *testing.T) (*ShipRepository, *gorm.DB, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return NewShipRepository(nil, nil, nil, stubWaypoints{}, db, nil), db, shared.MustNewPlayerID(player.ID)
}

func seedShip(t *testing.T, db *gorm.DB, playerID int, symbol, navStatus string, fuelCurrent int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		NavStatus:        navStatus,
		LocationSymbol:   "X1-KN67-A1",
		SystemSymbol:     "X1-KN67",
		FuelCurrent:      fuelCurrent,
		FuelCapacity:     1000,
		CargoCapacity:    40,
		Version:          1,
	}).Error)
}

// Two entities loaded at the same version: the first Save wins and bumps the
// version; the second is a DETECTED conflict — counted, logged, and then
// applied via the legacy last-write-wins fallback (behavior preserved,
// visibility added). This is the probe: in production this counter measures
// how often the sp-n7yp race class actually fires (sp-60ff).
func TestSave_DetectsVersionConflictAndFallsBack(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-10", "IN_ORBIT", 100)

	a, err := repo.FindBySymbol(context.Background(), "TORWIND-10", pid)
	require.NoError(t, err)
	b, err := repo.FindBySymbol(context.Background(), "TORWIND-10", pid)
	require.NoError(t, err)
	require.Equal(t, 1, a.PersistedVersion())

	before := shipVersionConflicts.Load()

	require.NoError(t, a.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), a))
	require.Equal(t, before, shipVersionConflicts.Load(), "first save is conflict-free")
	require.Equal(t, 2, a.PersistedVersion(), "committed save advances the entity's version")

	require.NoError(t, b.Refuel(1))
	require.NoError(t, repo.Save(context.Background(), b), "conflict is telemetry, never an error")
	require.Equal(t, before+1, shipVersionConflicts.Load(), "stale-version save must be counted")

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-10").First(&row).Error)
	require.Equal(t, 101, row.FuelCurrent, "fallback preserves today's last-write-wins outcome")
}

// An API-born entity (PersistedVersion 0 — never loaded from a row) uses the
// legacy unconditional upsert: inserts and first-sync writes never count as
// conflicts.
func TestSave_UnknownVersionUsesLegacyUpsert(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-12", "IN_ORBIT", 5)

	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-12", pid)
	require.NoError(t, err)
	ship.SetPersistedVersion(0) // simulate API-born reconstruction

	before := shipVersionConflicts.Load()
	require.NoError(t, repo.Save(context.Background(), ship))
	require.Equal(t, before, shipVersionConflicts.Load())
}

// Back-to-back saves of the SAME entity never conflict: each committed save
// advances the entity's PersistedVersion in lockstep with the row.
func TestSave_SequentialSavesOfSameEntityNeverConflict(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-13", "IN_ORBIT", 0)

	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-13", pid)
	require.NoError(t, err)

	before := shipVersionConflicts.Load()
	for i := 0; i < 5; i++ {
		require.NoError(t, ship.Refuel(1))
		require.NoError(t, repo.Save(context.Background(), ship))
	}
	require.Equal(t, before, shipVersionConflicts.Load())
	require.Equal(t, 6, ship.PersistedVersion())
}
```

Note: `system.IWaypointProvider`'s import path/name is used at `ship_repository.go:34-36` (fields) — mirror the exact interface name and `GetWaypoint` signature from `internal/domain/system` when writing the stub; the call under test is `modelToDomain` at `ship_repository.go:1304`.

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/api/ -run 'TestSave_Detects|TestSave_Unknown|TestSave_Sequential' -v`
Expected: FAIL (PersistedVersion undefined)

- [ ] **Step 3: Implement**

`internal/domain/navigation/ship.go` — field (with the other struct fields) + accessors (near `Arrive`, line ~416):

```go
	// persistedVersion is the ships.version value this entity was loaded at
	// (0 = never loaded from a row, e.g. API-born). Infrastructure carries it
	// for the Save CAS tripwire (sp-60ff): it is NOT domain state and has no
	// behavior here.
	persistedVersion int
```

```go
// PersistedVersion reports the row version this entity was reconstructed at
// (0 = unknown/API-born). See sp-60ff conflict telemetry.
func (s *Ship) PersistedVersion() int { return s.persistedVersion }

// SetPersistedVersion is called by the persistence layer at reconstruction
// and after a committed save.
func (s *Ship) SetPersistedVersion(v int) { s.persistedVersion = v }
```

`internal/adapters/api/ship_repository.go`:
1. `modelToDomain` (line ~1301): after the `ReconstructShip` call succeeds and before enrichment returns, add `ship.SetPersistedVersion(model.Version)`.
2. `shipToModel` (line 669): change `Version: 1,` to `Version: ship.PersistedVersion() + 1,` (an unknown 0 writes 1, exactly today's value).
3. Package counter at file scope (import `sync/atomic`):

```go
// shipVersionConflicts counts Save calls whose row version moved past the
// entity's loaded version — i.e. another writer committed in between and is
// about to be last-write-wins clobbered (sp-60ff probe; the mailbox, sp-eum3,
// is gated on this reading). Mirrored to prometheus; kept as a package
// atomic so tests and debuggers can read it without a registry.
var shipVersionConflicts atomic.Int64
```

4. Replace `Save` (line 826) body:

```go
// Save persists ship aggregate state (including full state) to DB.
// When the entity carries a known row version, the upsert is guarded with
// `DO UPDATE ... WHERE ships.version = <loaded>` (postgres and sqlite both
// support upsert-where): RowsAffected == 0 means another writer committed
// since this entity was loaded. That is DETECTION-ONLY telemetry (sp-60ff):
// we count + log, then apply the legacy last-write-wins upsert so behavior
// is unchanged. The reading gates the mailbox (sp-eum3), which then migrates
// the remaining writers (sp-wa7c).
func (r *ShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	model := r.shipToModel(ship)

	if loaded := ship.PersistedVersion(); loaded > 0 {
		res := r.db.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
				Where: clause.Where{Exprs: []clause.Expression{
					clause.Eq{Column: clause.Column{Table: "ships", Name: "version"}, Value: loaded},
				}},
				UpdateAll: true,
			}).
			Create(&model)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			ship.SetPersistedVersion(loaded + 1)
			r.shipListCache.Delete(ship.PlayerID().Value())
			return nil
		}
		// Conflict: the row moved past our loaded version.
		shipVersionConflicts.Add(1)
		metrics.RecordShipVersionConflict()
		log.Printf("ERROR: ship %s save conflict — row version moved past %d (concurrent writer; sp-60ff probe); applying last-write-wins fallback",
			ship.ShipSymbol(), loaded)
	}

	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&model).Error

	if err == nil {
		ship.SetPersistedVersion(model.Version)
		// Invalidate cache to ensure assignment changes are immediately visible
		// This prevents stale assignment data from causing ships to be incorrectly
		// seen as idle when they've been assigned to containers (e.g., storage ships)
		r.shipListCache.Delete(ship.PlayerID().Value())
	}

	return err
}
```

Contingency (document in the commit if hit): if the sqlite driver rejects `clause.OnConflict.Where`, replace the guarded branch with an explicit `UPDATE ships SET ... WHERE ship_symbol=? AND player_id=? AND version=?` via `.Model(&persistence.ShipModel{}).Where(...).Select("*").Updates(&model)`, falling back to the upsert when `RowsAffected == 0` and distinguishing missing-row via a `First` probe. The tests in Step 1 define the contract either way.

5. Metrics — same global-shim pattern as the existing `RecordContainerRestart` (`prometheus_collector.go:152`), via a type-asserted single-method interface so existing `MetricsRecorder` fakes keep compiling. In `prometheus_collector.go`:

```go
// ShipWriteConflictRecorder is implemented by collectors that track the
// sp-60ff ship-row version tripwire. Separate single-method interface so
// existing MetricsRecorder implementations keep compiling.
type ShipWriteConflictRecorder interface {
	RecordShipVersionConflict()
}

// RecordShipVersionConflict records a ship save whose row version moved past
// the entity's loaded version (a concurrent-writer clobber, sp-60ff).
func RecordShipVersionConflict() {
	if globalCollector == nil {
		return
	}
	if rec, ok := globalCollector.(ShipWriteConflictRecorder); ok {
		rec.RecordShipVersionConflict()
	}
}
```

In `container_metrics.go`: add field `shipVersionConflicts prometheus.Counter`; build it in the constructor's field-init list:

```go
		// sp-60ff tripwire: ship saves that raced past their loaded version.
		// Unlabeled — the paired ERROR log carries the ship symbol.
		shipVersionConflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "ship",
			Name:      "version_conflicts_total",
			Help:      "Ship row writes that raced past their loaded version (concurrent-writer clobbers)",
		}),
```

Add `c.shipVersionConflicts,` to the `Register()` metric slice and implement:

```go
// RecordShipVersionConflict implements ShipWriteConflictRecorder (sp-60ff).
func (c *ContainerMetricsCollector) RecordShipVersionConflict() {
	c.shipVersionConflicts.Inc()
}
```

Import `metrics` in `ship_repository.go` (check for an import cycle: `internal/adapters/metrics` must not import `internal/adapters/api` — it doesn't today; if the compiler disagrees, call the shim through a package-level `var onVersionConflict = func(){}` hook set from main instead).

- [ ] **Step 4: Run tests**

Run: `rtk go test ./internal/adapters/api/ && rtk go test ./internal/adapters/metrics/`
Expected: PASS — including all pre-existing repo tests. The CAS branch only activates for entities with a known version, and every DB-load path now sets one; if a pre-existing test trips the counter or the guard, fix the SEED data or the test's stale-entity reuse, not the guard.

- [ ] **Step 5: Full suite + build**

Run: `rtk go vet ./... && rtk go test ./... && rtk go build -o /tmp/st-probe ./cmd/spacetraders-daemon && rm /tmp/st-probe`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/domain/navigation/ship.go internal/adapters/api/ internal/adapters/metrics/
rtk git commit -m "feat(daemon): ships.version CAS tripwire — concurrent-writer clobbers counted+logged, behavior preserved (sp-60ff)"
```

- [ ] **Step 7: Deploy, measure, GATE**

1. Ship it with the next daemon roll (normal deploy path; no config needed).
2. Immediately after deploy, confirm the metric is registered: `rtk curl -s http://localhost:<metrics.port>/metrics | grep ship_version_conflicts_total` → the series exists at 0.
3. `bd update sp-60ff --append-notes="tripwire live in production as of <date>; gate reading due <date+7d>"`.
4. After ≥7 days of normal fleet operation, execute the **decision gate** exactly as specified in the "Sequencing" section above (record the reading + PROCEED/DEFER in sp-60ff, then close it; on DEFER also `bd defer sp-eum3`).

**Tasks 2-7 below run only on a PROCEED reading.**

---

### Task 2: Mailbox core (`shipMailboxes`)

**Files:**
- Create: `internal/adapters/api/ship_mailbox.go`
- Test: `internal/adapters/api/ship_mailbox_test.go`

**Interfaces:**
- Consumes: `supervise.CapturePanic` (sp-i01z), `shared.PlayerID`
- Produces (package-internal; Tasks 3-5 call it): `newShipMailboxes() *shipMailboxes`; `func (m *shipMailboxes) run(ctx context.Context, playerID shared.PlayerID, symbol, op string, fn func(ctx context.Context) error) error`. Guarantees: turns for one `(playerID, symbol)` execute strictly FIFO on one goroutine; turns for different ships run independently; a panicking turn returns `*supervise.PanicError` to its caller and the actor survives; re-entrant `run` for the same ship inside a turn fails fast with `errMailboxReentrant`; a caller whose ctx dies while queued gets `ctx.Err()` and its turn is skipped.

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/api/ship_mailbox_test.go
package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

func testPlayer(t *testing.T, n int) shared.PlayerID {
	t.Helper()
	return shared.MustNewPlayerID(n)
}

// Turns for ONE ship are mutually exclusive and FIFO: 100 concurrent
// read-modify-write turns on a plain int must not lose a single update.
// Run with -race.
func TestShipMailbox_SerializesTurnsPerShip(t *testing.T) {
	m := newShipMailboxes()
	pid := testPlayer(t, 1)

	counter := 0 // deliberately unsynchronized — the mailbox IS the lock
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := m.run(context.Background(), pid, "TORWIND-1", "incr", func(ctx context.Context) error {
				v := counter
				time.Sleep(50 * time.Microsecond) // widen any interleave window
				counter = v + 1
				return nil
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 100, counter, "a lost update means two turns interleaved")
}

// Different ships must not serialize against each other: two turns that each
// wait for the other ship's turn to have STARTED can only finish if they run
// concurrently.
func TestShipMailbox_ShipsRunIndependently(t *testing.T) {
	m := newShipMailboxes()
	pid := testPlayer(t, 1)

	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- m.run(context.Background(), pid, "SHIP-A", "x", func(ctx context.Context) error {
			close(aStarted)
			select {
			case <-bStarted:
				return nil
			case <-time.After(2 * time.Second):
				return errors.New("SHIP-B turn never started — ships are serialized against each other")
			}
		})
	}()
	go func() {
		done <- m.run(context.Background(), pid, "SHIP-B", "x", func(ctx context.Context) error {
			close(bStarted)
			select {
			case <-aStarted:
				return nil
			case <-time.After(2 * time.Second):
				return errors.New("SHIP-A turn never started")
			}
		})
	}()
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}

// A panic inside a turn must be returned to THAT caller as an error and must
// not kill the actor: the next turn still runs.
func TestShipMailbox_PanicIsContainedAndActorSurvives(t *testing.T) {
	m := newShipMailboxes()
	pid := testPlayer(t, 1)

	err := m.run(context.Background(), pid, "TORWIND-1", "boom", func(ctx context.Context) error {
		panic("nil deref")
	})
	var perr *supervise.PanicError
	require.ErrorAs(t, err, &perr)

	require.NoError(t, m.run(context.Background(), pid, "TORWIND-1", "after", func(ctx context.Context) error {
		return nil
	}))
}

// Calling run for the SAME ship from inside its own turn would deadlock the
// actor forever. Fail fast and loud instead (loud-fail doctrine).
func TestShipMailbox_ReentrantTurnFailsFast(t *testing.T) {
	m := newShipMailboxes()
	pid := testPlayer(t, 1)

	err := m.run(context.Background(), pid, "TORWIND-1", "outer", func(ctx context.Context) error {
		return m.run(ctx, pid, "TORWIND-1", "inner", func(ctx context.Context) error { return nil })
	})
	require.ErrorIs(t, err, errMailboxReentrant)

	// A DIFFERENT ship from inside a turn is legal (no self-deadlock).
	require.NoError(t, m.run(context.Background(), pid, "TORWIND-1", "outer", func(ctx context.Context) error {
		return m.run(ctx, pid, "TORWIND-2", "inner", func(ctx context.Context) error { return nil })
	}))
}

// A queued caller whose context is canceled gets ctx.Err() and its fn never
// runs — a dead gRPC request must not apply a stale mutation minutes later.
func TestShipMailbox_CanceledCallerSkipsTurn(t *testing.T) {
	m := newShipMailboxes()
	pid := testPlayer(t, 1)

	blockerIn := make(chan struct{})
	release := make(chan struct{})
	go m.run(context.Background(), pid, "TORWIND-1", "blocker", func(ctx context.Context) error {
		close(blockerIn)
		<-release
		return nil
	})
	<-blockerIn

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.run(ctx, pid, "TORWIND-1", "victim", func(ctx context.Context) error {
			ran.Store(true)
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond) // let the victim enqueue behind the blocker
	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)

	close(release)
	// Drain with one more turn, then confirm the canceled fn never ran.
	require.NoError(t, m.run(context.Background(), pid, "TORWIND-1", "drain", func(ctx context.Context) error { return nil }))
	require.False(t, ran.Load(), "canceled turn must be skipped at dequeue")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test -race ./internal/adapters/api/ -run TestShipMailbox -v`
Expected: FAIL (undefined: newShipMailboxes)

- [ ] **Step 3: Implement**

```go
// internal/adapters/api/ship_mailbox.go
package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// shipMailboxes serializes all operational-state writes per ship (sp-eum3).
// Each (playerID, shipSymbol) gets a lazily-spawned actor goroutine that
// executes turns strictly FIFO; ships never serialize against each other.
// This is the actor shape without the runtime swap: the mailbox owns the
// Find→mutate→Save critical section that the last-write-wins Save upsert
// (ship_repository.go, sp-60ff tripwire) otherwise leaves racy — the
// sp-n7yp dockrace class (async arrival vs owning container's dock).
//
// Lifecycle: actors are never reaped — a fleet is O(hundreds) of ships and a
// parked goroutine+channel is ~4KB; reaping would buy nothing and cost a
// teardown/enqueue race. Actors live until process exit.
type shipMailboxes struct {
	mu    sync.Mutex
	boxes map[string]*shipMailbox
}

type shipMailbox struct {
	ch chan shipTurn
}

type shipTurn struct {
	ctx   context.Context
	op    string
	fn    func(ctx context.Context) error
	reply chan error // buffered(1): the actor never blocks replying to an abandoned caller
}

// errMailboxReentrant: a turn tried to enqueue another turn for the SAME
// ship — that deadlocks the actor forever, so it fails fast instead
// (loud-fail doctrine). Almost always a verb called from inside a Mutate fn;
// use the *inTurn form or restructure the caller.
var errMailboxReentrant = errors.New("ship mailbox: re-entrant turn for the same ship")

// slowTurnWaitWarn: queue-wait beyond this is logged — a persistently deep
// mailbox means some turn is holding the actor (slow API call, DB stall).
const slowTurnWaitWarn = 5 * time.Second

type mailboxCtxKey struct{}

func newShipMailboxes() *shipMailboxes {
	return &shipMailboxes{boxes: make(map[string]*shipMailbox)}
}

// run executes fn as this ship's next turn and blocks until it completes (or
// ctx dies while queued). fn receives a ctx stamped with the ship's turn
// marker so nested same-ship turns are rejected instead of deadlocking.
func (m *shipMailboxes) run(ctx context.Context, playerID shared.PlayerID, symbol, op string, fn func(ctx context.Context) error) error {
	key := fmt.Sprintf("%d/%s", playerID.Value(), symbol)
	if held, ok := ctx.Value(mailboxCtxKey{}).(string); ok && held == key {
		return fmt.Errorf("%w: %s during %q", errMailboxReentrant, key, op)
	}

	box := m.box(key)
	turn := shipTurn{
		ctx:   context.WithValue(ctx, mailboxCtxKey{}, key),
		op:    op,
		fn:    fn,
		reply: make(chan error, 1),
	}

	enqueued := time.Now()
	select {
	case box.ch <- turn:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-turn.reply:
		if wait := time.Since(enqueued); wait > slowTurnWaitWarn {
			log.Printf("ship mailbox: %s turn %q waited %s in queue", key, op, wait)
		}
		return err
	case <-ctx.Done():
		// Abandon: the actor's dequeue-time ctx check skips the turn (or the
		// buffered reply absorbs a result that raced the cancellation).
		return ctx.Err()
	}
}

// box returns the ship's mailbox, spawning its actor on first use.
func (m *shipMailboxes) box(key string) *shipMailbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.boxes[key]; ok {
		return b
	}
	b := &shipMailbox{ch: make(chan shipTurn, 16)}
	m.boxes[key] = b
	go b.loop(key)
	return b
}

func (b *shipMailbox) loop(key string) {
	for turn := range b.ch {
		if turn.ctx.Err() != nil {
			turn.reply <- turn.ctx.Err() // caller is gone; buffered, never blocks
			continue
		}
		turn.reply <- runTurn(key, turn)
	}
}

// runTurn executes one turn with a panic barrier: a panicking mutation is
// returned to ITS caller as *supervise.PanicError; the actor — and every
// queued turn behind it — survives.
func runTurn(key string, turn shipTurn) (err error) {
	defer supervise.CapturePanic(&err, "ship-mailbox:"+key+":"+turn.op)
	return turn.fn(turn.ctx)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test -race ./internal/adapters/api/ -run TestShipMailbox -v`
Expected: PASS (all 5, race detector clean)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/api/ship_mailbox.go internal/adapters/api/ship_mailbox_test.go
rtk git commit -m "feat(daemon): per-ship mailbox core — FIFO turns, panic containment, re-entrancy loud-fail (sp-eum3)"
```

---

### Task 3: `ShipMutator` port + `Mutate` implementation

**Files:**
- Modify: `internal/domain/navigation/ports.go` (new port + sentinel; do NOT touch the big `ShipRepository` interface — a new narrow interface keeps every existing fake compiling)
- Modify: `internal/adapters/api/ship_repository.go` (`mailboxes` field at line ~43, init in `NewShipRepository` at line ~60, `Mutate` method)
- Test: `internal/adapters/api/ship_repository_mutate_test.go`

**Interfaces:**
- Consumes: Task 2 `shipMailboxes.run`; existing `FindBySymbol` (`ship_repository.go:88`), `Save` (guarded since Task 1); test helpers `newShipWriteTestRepo`/`seedShip`/`stubWaypoints` from Task 1's `ship_repository_version_test.go` (same package)
- Produces:

```go
// internal/domain/navigation/ports.go
var ErrSkipSave = errors.New("ship mutate: skip save")

type ShipMutator interface {
	Mutate(ctx context.Context, playerID shared.PlayerID, shipSymbol string, op string,
		fn func(ctx context.Context, ship *Ship) error) error
}
```

Semantics: `Mutate` runs one serialized turn: re-find fresh entity → `fn(ctx, fresh)` → `Save(fresh)`. `fn` returning `ErrSkipSave` commits nothing and `Mutate` returns nil; any other error aborts the save and is returned. The `fresh` entity is authoritative only inside `fn` — callers must not retain it. Task 6 (scheduler) and rollout bead sp-wa7c depend on exactly this signature.

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/api/ship_repository_mutate_test.go
package api

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// Mutate = one serialized re-find→mutate→save turn. 100 concurrent +1-fuel
// mutations must land exactly 100 fuel: with the old Find→mutate→Save free-
// for-all this loses updates. Run with -race.
func TestMutate_ConcurrentTurnsLoseNoUpdates(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-5", "IN_ORBIT", 0)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.Mutate(context.Background(), pid, "TORWIND-5", "refuel-tick",
				func(ctx context.Context, ship *navigation.Ship) error {
					return ship.Refuel(1)
				})
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	fresh, err := repo.FindBySymbol(context.Background(), "TORWIND-5", pid)
	require.NoError(t, err)
	require.Equal(t, 100, fresh.Fuel().Current, "lost update: turns interleaved")
}

// ErrSkipSave commits nothing: the row is untouched and Mutate returns nil.
func TestMutate_SkipSaveCommitsNothing(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-6", "IN_ORBIT", 7)

	err := repo.Mutate(context.Background(), pid, "TORWIND-6", "noop",
		func(ctx context.Context, ship *navigation.Ship) error {
			_ = ship.Refuel(500) // mutate the in-memory entity, then bail
			return navigation.ErrSkipSave
		})
	require.NoError(t, err)

	fresh, err := repo.FindBySymbol(context.Background(), "TORWIND-6", pid)
	require.NoError(t, err)
	require.Equal(t, 7, fresh.Fuel().Current, "skip-save must not persist the mutation")
}

// A real error from fn aborts the save and surfaces to the caller.
func TestMutate_FnErrorAbortsSave(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-7", "IN_ORBIT", 7)

	boom := errors.New("domain said no")
	err := repo.Mutate(context.Background(), pid, "TORWIND-7", "fails",
		func(ctx context.Context, ship *navigation.Ship) error {
			_ = ship.Refuel(500)
			return boom
		})
	require.ErrorIs(t, err, boom)

	fresh, err := repo.FindBySymbol(context.Background(), "TORWIND-7", pid)
	require.NoError(t, err)
	require.Equal(t, 7, fresh.Fuel().Current)
}

// The sp-60ff proof wire: serialized turns never trip the conflict counter,
// because Mutate re-finds fresh (latest version) inside the turn, so the
// Save CAS always matches. This is the before/after story for the gate
// metric: mailbox-routed writes drive spacetraders_ship_version_conflicts_total
// toward zero.
func TestMutate_TurnsNeverTripTheConflictCounter(t *testing.T) {
	repo, db, pid := newShipWriteTestRepo(t)
	seedShip(t, db, pid.Value(), "TORWIND-11", "IN_ORBIT", 0)

	before := shipVersionConflicts.Load()
	for i := 0; i < 20; i++ {
		require.NoError(t, repo.Mutate(context.Background(), pid, "TORWIND-11", "tick",
			func(ctx context.Context, ship *navigation.Ship) error { return ship.Refuel(1) }))
	}
	require.Equal(t, before, shipVersionConflicts.Load())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test -race ./internal/adapters/api/ -run TestMutate -v`
Expected: FAIL (repo has no Mutate; navigation.ErrSkipSave undefined)

- [ ] **Step 3: Implement**

`internal/domain/navigation/ports.go` — add near the `ArrivalScheduler` interface (line ~160), plus `"errors"` to imports:

```go
// ErrSkipSave is returned by a ShipMutator.Mutate fn to commit nothing:
// the turn ends successfully with no write (e.g. an arrival turn that found
// the ship no longer in transit).
var ErrSkipSave = errors.New("ship mutate: skip save")

// ShipMutator runs a serialized read-modify-write turn against one ship
// (sp-eum3). The implementation (api.ShipRepository) re-finds FRESH entity
// state inside the ship's mailbox turn, applies fn, and persists — so
// concurrent writers (owning container, arrival scheduler, sweeper) can
// never interleave or clobber each other's writes with stale snapshots.
// The *Ship passed to fn is authoritative ONLY inside fn; do not retain it.
// A narrow, separate port (not a ShipRepository method) so existing
// ShipRepository fakes keep compiling.
type ShipMutator interface {
	Mutate(ctx context.Context, playerID shared.PlayerID, shipSymbol string, op string,
		fn func(ctx context.Context, ship *Ship) error) error
}
```

`internal/adapters/api/ship_repository.go`:
1. Add field to `ShipRepository` struct (after `shipListCache`): `mailboxes *shipMailboxes`.
2. In `NewShipRepository`, add `mailboxes: newShipMailboxes(),` to the struct literal.
3. Add the method (after `Save`), plus the file-scope conformance check:

```go
var _ navigation.ShipMutator = (*ShipRepository)(nil)

// Mutate implements navigation.ShipMutator (sp-eum3): one serialized turn of
// find-fresh → fn → save on this ship's mailbox. See the port doc for
// semantics.
func (r *ShipRepository) Mutate(ctx context.Context, playerID shared.PlayerID, shipSymbol string, op string,
	fn func(ctx context.Context, ship *navigation.Ship) error) error {
	return r.mailboxes.run(ctx, playerID, shipSymbol, op, func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return fmt.Errorf("mutate %q: find %s: %w", op, shipSymbol, err)
		}
		if err := fn(ctx, fresh); err != nil {
			if errors.Is(err, navigation.ErrSkipSave) {
				return nil
			}
			return err
		}
		if err := r.Save(ctx, fresh); err != nil {
			return fmt.Errorf("mutate %q: save %s: %w", op, shipSymbol, err)
		}
		return nil
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `rtk go test -race ./internal/adapters/api/ -run TestMutate -v`
Expected: PASS. If the seeded `ShipModel` is missing a column `modelToDomain` requires (e.g. cargo JSON), the first failure names it — extend `seedShip` with that zero-value field rather than changing production code.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/domain/navigation/ports.go internal/adapters/api/
rtk git commit -m "feat(daemon): navigation.ShipMutator port + serialized Mutate on ShipRepository (sp-eum3)"
```

---

### Task 4: Serialize `Dock` and `Orbit` (arrival-reconcile pair) + `Ship.AdoptState`

**Files:**
- Modify: `internal/domain/navigation/ship.go` (add `AdoptState` near `Arrive`, line ~416)
- Modify: `internal/adapters/api/ship_repository.go` (`Dock` at line 296, `Orbit` at line 329)
- Test: `internal/adapters/api/ship_repository_serialized_verbs_test.go`

**Interfaces:**
- Consumes: Task 2 mailbox, `FindBySymbol`, `Save`, `domainPorts.APIClient.DockShip/OrbitShip`, `player.PlayerRepository.FindByID`; test helpers `stubWaypoints`/`seedShip` from Task 1's file
- Produces: `func (s *Ship) AdoptState(other *Ship)` (Task 5 reuses it). Verb signatures unchanged: `Dock(ctx, ship, playerID) error`, `Orbit(ctx, ship, playerID) error`.

Turn shape (this is the load-bearing design): the API call is made first (idempotent for dock/orbit), then the turn **re-finds fresh row state** and applies the transition to the FRESH entity — serialization without re-find would still persist the caller's stale snapshot (writing a phantom `arrival_time`, stale assignment columns, stale fuel). If the fresh row is still `IN_TRANSIT` but the API accepted the verb (server-side the ship has arrived; the local arrival turn just hasn't run), reconcile by applying `Arrive()` + `ClearArrivalTime()` first — strictly more truthful than today's stale write, and it is exactly the sp-n7yp phantom this closes. Finally `ship.AdoptState(fresh)` so the caller's in-hand entity reflects the committed outcome (callers read `ship.NavStatus()` after the verb today; that must keep working).

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/api/ship_repository_serialized_verbs_test.go
package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// stubAPI: embed the big interface, override only the verbs under test.
type stubAPI struct {
	domainPorts.APIClient
	dockCalls       int
	orbitCalls      int
	flightModeCalls int
}

func (a *stubAPI) DockShip(_ context.Context, _ string, _ string) error  { a.dockCalls++; return nil }
func (a *stubAPI) OrbitShip(_ context.Context, _ string, _ string) error { a.orbitCalls++; return nil }

// stubPlayers: verbs look up the token; domain player.Player has exported
// fields (internal/domain/player/player.go:6).
type stubPlayers struct{ player.PlayerRepository }

func (stubPlayers) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	return &player.Player{ID: id, AgentSymbol: "TORWIND", Token: "tok"}, nil
}

func newVerbTestRepo(t *testing.T) (*ShipRepository, *stubAPI, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	apiStub := &stubAPI{}
	repo := NewShipRepository(apiStub, stubPlayers{}, nil, stubWaypoints{}, db, nil)
	seedShip(t, db, p.ID, "TORWIND-8", "IN_ORBIT", 100)
	return repo, apiStub, shared.MustNewPlayerID(p.ID)
}

// The sp-n7yp phantom, repo-level: the caller docks with a STALE snapshot
// (says IN_ORBIT) while the ROW says IN_TRANSIT with a past arrival the
// async timer hasn't applied yet. The serialized turn must reconcile on
// FRESH state: arrive-then-dock, clear the arrival time, and never write the
// stale snapshot's phantom arrival back.
func TestDock_ReconcilesFreshInTransitRow(t *testing.T) {
	repo, _, pid := newVerbTestRepo(t)

	// Caller's snapshot: taken while IN_ORBIT.
	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	// Row moves on without the caller: navigation left it IN_TRANSIT with a
	// past arrival (timer not yet fired).
	past := time.Now().Add(-1 * time.Minute).UTC()
	require.NoError(t, repo.db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ? AND player_id = ?", "TORWIND-8", pid.Value()).
		Updates(map[string]interface{}{"nav_status": "IN_TRANSIT", "arrival_time": past, "version": 5}).Error)

	require.NoError(t, repo.Dock(context.Background(), stale, pid))

	var row persistence.ShipModel
	require.NoError(t, repo.db.Where("ship_symbol = ?", "TORWIND-8").First(&row).Error)
	require.Equal(t, "DOCKED", row.NavStatus)
	require.Nil(t, row.ArrivalTime, "phantom arrival_time is exactly the sp-n7yp clobber")

	// Caller's entity adopted the committed outcome.
	require.True(t, stale.IsDocked())
	require.Nil(t, stale.ArrivalTime())
}

// Plain path: dock from in-orbit still works and updates the caller's entity.
func TestDock_PlainPathDocksAndAdopts(t *testing.T) {
	repo, apiStub, pid := newVerbTestRepo(t)
	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	require.NoError(t, repo.Dock(context.Background(), ship, pid))
	require.Equal(t, 1, apiStub.dockCalls)
	require.True(t, ship.IsDocked())

	fresh, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)
	require.True(t, fresh.IsDocked())
}

// Orbit mirrors Dock's reconcile (arrive first if the fresh row is IN_TRANSIT).
func TestOrbit_ReconcilesFreshInTransitRow(t *testing.T) {
	repo, _, pid := newVerbTestRepo(t)
	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Minute).UTC()
	require.NoError(t, repo.db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ? AND player_id = ?", "TORWIND-8", pid.Value()).
		Updates(map[string]interface{}{"nav_status": "IN_TRANSIT", "arrival_time": past, "version": 5}).Error)

	require.NoError(t, repo.Orbit(context.Background(), stale, pid))

	var row persistence.ShipModel
	require.NoError(t, repo.db.Where("ship_symbol = ?", "TORWIND-8").First(&row).Error)
	require.Equal(t, "IN_ORBIT", row.NavStatus)
	require.Nil(t, row.ArrivalTime)
	require.True(t, stale.IsInOrbit())
}
```

(`repo.db` is the unexported field — these tests are in package `api`, so direct access is fine, matching the dedication tests' style. The raw `Updates` bumping `version` to 5 makes the caller's snapshot stale in exactly the way the tripwire counts — these verb turns re-find, so they must NOT increment `shipVersionConflicts`; add `require.Equal(t, before, shipVersionConflicts.Load())` guards if the counter moves during Step 4 debugging.)

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/api/ -run 'TestDock|TestOrbit' -v`
Expected: FAIL — `TestDock_ReconcilesFreshInTransitRow` fails today because `EnsureDocked()` on the stale IN_ORBIT snapshot "succeeds" and writes the stale row back (phantom arrival), or the row check sees the stale write.

- [ ] **Step 3: Implement AdoptState + the two verbs**

`internal/domain/navigation/ship.go`, after `Arrive()` (line ~423):

```go
// AdoptState overwrites this entity's state with other's (same hull).
// Repository verbs call it after a serialized mailbox turn re-loaded fresh
// row state (sp-eum3), so the caller's in-hand entity reflects the COMMITTED
// outcome instead of its pre-turn snapshot. Ship is a plain value struct
// (no locks), so whole-value assignment is safe.
func (s *Ship) AdoptState(other *Ship) {
	*s = *other
}
```

`internal/adapters/api/ship_repository.go` — replace `Dock` (line 296) with:

```go
// Dock docks the ship via API (idempotent) and persists state to database.
// The write runs as a serialized mailbox turn against FRESH row state
// (sp-eum3): the old path applied EnsureDocked to the caller's snapshot and
// full-row-Saved it, clobbering a concurrent arrival (sp-n7yp) or claim.
func (r *ShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to dock ship (API itself is idempotent - will succeed if already docked)
	if err := r.apiClient.DockShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		if !isAlreadyDockedError(err) {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	return r.mailboxes.run(ctx, playerID, ship.ShipSymbol(), "dock", func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("dock: refetch %s: %w", ship.ShipSymbol(), err)
		}
		// The API accepted the dock, so server-side the ship is not in
		// transit. A fresh row still IN_TRANSIT means the local arrival turn
		// hasn't run — reconcile it here instead of writing a phantom.
		if fresh.IsInTransit() {
			if err := fresh.Arrive(); err != nil {
				return fmt.Errorf("dock: reconcile arrival for %s: %w", ship.ShipSymbol(), err)
			}
			fresh.ClearArrivalTime()
		}
		if _, err := fresh.EnsureDocked(); err != nil {
			return fmt.Errorf("failed to update ship state: %w", err)
		}
		if err := r.Save(ctx, fresh); err != nil {
			log.Printf("Warning: failed to persist ship %s after dock: %v", ship.ShipSymbol(), err)
		}
		r.shipListCache.Delete(playerID.Value())
		ship.AdoptState(fresh)
		return nil
	})
}
```

Replace `Orbit` (line 329) with the mirror (same structure; `OrbitShip` + `isAlreadyInOrbitError` + `EnsureInOrbit` + the existing `ClearArrivalTime()` call after the transition):

```go
// Orbit puts ship in orbit via API (idempotent) and persists state to
// database, as a serialized mailbox turn against fresh row state (sp-eum3).
func (r *ShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	if err := r.apiClient.OrbitShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		if !isAlreadyInOrbitError(err) {
			return fmt.Errorf("failed to orbit ship: %w", err)
		}
	}

	return r.mailboxes.run(ctx, playerID, ship.ShipSymbol(), "orbit", func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("orbit: refetch %s: %w", ship.ShipSymbol(), err)
		}
		if fresh.IsInTransit() {
			if err := fresh.Arrive(); err != nil {
				return fmt.Errorf("orbit: reconcile arrival for %s: %w", ship.ShipSymbol(), err)
			}
		}
		if _, err := fresh.EnsureInOrbit(); err != nil {
			return fmt.Errorf("failed to update ship state: %w", err)
		}
		// Clear arrival time when ship arrives in orbit
		fresh.ClearArrivalTime()
		if err := r.Save(ctx, fresh); err != nil {
			log.Printf("Warning: failed to persist ship %s after orbit: %v", ship.ShipSymbol(), err)
		}
		r.shipListCache.Delete(playerID.Value())
		ship.AdoptState(fresh)
		return nil
	})
}
```

Design note (why the API call stays OUTSIDE the turn): dock/orbit API calls are idempotent and rate-limited (2/s, up to 30s under burst backoff); holding the ship's mailbox through an API retry storm would starve the arrival/cooldown turns behind it. The turn owns exactly the read-modify-write window. Navigate (Task 5) follows the same split.

- [ ] **Step 4: Run tests to verify they pass**

Run: `rtk go test ./internal/adapters/api/ -run 'TestDock|TestOrbit|TestMutate|TestShipMailbox' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
rtk git add internal/domain/navigation/ship.go internal/adapters/api/
rtk git commit -m "feat(daemon): Dock/Orbit run as serialized mailbox turns with fresh-row arrival reconcile (sp-eum3)"
```

---

### Task 5: Serialize `Navigate`, `Refuel`, `SetFlightMode`

**Files:**
- Modify: `internal/adapters/api/ship_repository.go` (`Navigate` at line 244, `Refuel` at line 365, `SetFlightMode` at line 404)
- Test: extend `internal/adapters/api/ship_repository_serialized_verbs_test.go`

**Interfaces:**
- Consumes: Tasks 2-4 (`mailboxes.run`, `AdoptState`); `apiClient.NavigateShip/RefuelShip/SetFlightMode`; `r.arrivalScheduler`
- Produces: unchanged verb signatures. `JettisonCargo` (line 436) is NOT touched — it makes no entity mutation and no `Save` (see its body: "Cargo is updated by the API, and we refetch").

- [ ] **Step 1: Write the failing tests**

Append to `ship_repository_serialized_verbs_test.go`:

```go
func (a *stubAPI) NavigateShip(_ context.Context, _ string, _ string, _ string) (*navigation.Result, error) {
	return &navigation.Result{
		FuelConsumed:   10,
		FlightMode:     "CRUISE",
		ArrivalTimeStr: time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}, nil
}

func (a *stubAPI) RefuelShip(_ context.Context, _ string, _ string, _ *int) (*navigation.RefuelResult, error) {
	return &navigation.RefuelResult{}, nil
}

func (a *stubAPI) SetFlightMode(_ context.Context, _ string, _ string, _ string) error {
	a.flightModeCalls++
	return nil
}

// Navigate applies the transition to FRESH row state: fuel consumed from the
// row's current fuel (not the caller's stale snapshot), transit + arrival
// recorded, caller's entity adopts the outcome.
func TestNavigate_AppliesTransitionToFreshRow(t *testing.T) {
	repo, _, pid := newVerbTestRepo(t)
	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	// Row's fuel drifted after the snapshot (another writer refueled to 500).
	require.NoError(t, repo.db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ? AND player_id = ?", "TORWIND-8", pid.Value()).
		Update("fuel_current", 500).Error)

	dest := &shared.Waypoint{Symbol: "X1-KN67-B2", SystemSymbol: "X1-KN67", X: 5, Y: 5}
	_, err = repo.Navigate(context.Background(), stale, dest, pid)
	require.NoError(t, err)

	var row persistence.ShipModel
	require.NoError(t, repo.db.Where("ship_symbol = ?", "TORWIND-8").First(&row).Error)
	require.Equal(t, "IN_TRANSIT", row.NavStatus)
	require.Equal(t, 490, row.FuelCurrent, "10 fuel consumed from the FRESH 500, not the stale snapshot's 100")
	require.NotNil(t, row.ArrivalTime)
	require.True(t, stale.IsInTransit(), "caller adopted the committed outcome")
}

// Refuel applies to fresh state and persists through the mailbox.
func TestRefuel_ToFullPersistsFreshState(t *testing.T) {
	repo, _, pid := newVerbTestRepo(t)
	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	_, err = repo.Refuel(context.Background(), ship, pid, nil)
	require.NoError(t, err)

	var row persistence.ShipModel
	require.NoError(t, repo.db.Where("ship_symbol = ?", "TORWIND-8").First(&row).Error)
	require.Equal(t, 1000, row.FuelCurrent)
	require.Equal(t, 1000, ship.Fuel().Current)
}

// SetFlightMode short-circuits against FRESH state: if the row already has
// the mode (set by another writer), no API call and no write happen.
func TestSetFlightMode_ShortCircuitsOnFreshState(t *testing.T) {
	repo, apiStub, pid := newVerbTestRepo(t)
	ship, err := repo.FindBySymbol(context.Background(), "TORWIND-8", pid)
	require.NoError(t, err)

	require.NoError(t, repo.db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ? AND player_id = ?", "TORWIND-8", pid.Value()).
		Update("flight_mode", "DRIFT").Error)

	require.NoError(t, repo.SetFlightMode(context.Background(), ship, pid, "DRIFT"))
	require.Equal(t, 0, apiStub.flightModeCalls, "already-set mode must not hit the API")
	require.Equal(t, "DRIFT", ship.FlightMode(), "caller adopts the fresh mode")
}
```

Check `navigation.Result`/`RefuelResult` field names against `internal/domain/navigation` (the verbs read `FuelConsumed`, `FlightMode`, `ArrivalTimeStr` — `ship_repository.go:262-277`) and the exact `ShipModel` column names (`fuel_current`, `flight_mode`, `nav_status`, `arrival_time`) against `internal/adapters/persistence/models.go:84-172`; adjust the raw `Update` calls if a tag differs.

- [ ] **Step 2: Run tests to verify they fail**

Run: `rtk go test ./internal/adapters/api/ -run 'TestNavigate_Applies|TestRefuel_ToFull|TestSetFlightMode_Short' -v`
Expected: FAIL (fuel asserts 490 but stale-snapshot math yields 90; flight-mode short-circuit reads the stale entity today)

- [ ] **Step 3: Implement**

`Navigate` (line 244) — keep player lookup + API call as-is, then replace everything from `// Update ship domain entity state from API response` to the end with:

```go
	err = r.mailboxes.run(ctx, playerID, ship.ShipSymbol(), "navigate", func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("navigate: refetch %s: %w", ship.ShipSymbol(), err)
		}
		// The API accepted the navigate, so server-side the ship was in
		// orbit. Reconcile a fresh row that disagrees (arrival not yet
		// applied, or still DOCKED from a stale write) before transiting.
		if fresh.IsInTransit() {
			if err := fresh.Arrive(); err != nil {
				return fmt.Errorf("navigate: reconcile arrival for %s: %w", ship.ShipSymbol(), err)
			}
			fresh.ClearArrivalTime()
		}
		if fresh.IsDocked() {
			if _, err := fresh.EnsureInOrbit(); err != nil {
				return fmt.Errorf("navigate: reconcile orbit for %s: %w", ship.ShipSymbol(), err)
			}
		}
		if err := fresh.StartTransit(destination); err != nil {
			return fmt.Errorf("failed to update ship state: %w", err)
		}
		if err := fresh.ConsumeFuel(navResult.FuelConsumed); err != nil {
			return fmt.Errorf("failed to consume fuel: %w", err)
		}
		if navResult.FlightMode != "" {
			fresh.SetFlightMode(navResult.FlightMode)
		}
		if navResult.ArrivalTimeStr != "" {
			if arrivalTime, err := time.Parse(time.RFC3339, navResult.ArrivalTimeStr); err == nil {
				fresh.SetArrivalTime(arrivalTime)
			}
		}
		if err := r.Save(ctx, fresh); err != nil {
			log.Printf("Warning: failed to persist ship %s after navigate: %v", ship.ShipSymbol(), err)
		}
		r.shipListCache.Delete(playerID.Value())
		if r.arrivalScheduler != nil {
			r.arrivalScheduler.ScheduleArrival(fresh)
		}
		ship.AdoptState(fresh)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return navResult, nil
```

`Refuel` (line 365) — keep the player lookup + API call as-is, then replace everything from `// Update ship domain entity state` to the final `return refuelResult, nil` with:

```go
	err = r.mailboxes.run(ctx, playerID, ship.ShipSymbol(), "refuel", func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("refuel: refetch %s: %w", ship.ShipSymbol(), err)
		}
		// Update ship domain entity state
		// If units specified, add that amount, otherwise refuel to full
		if units != nil {
			if err := fresh.Refuel(*units); err != nil {
				return fmt.Errorf("failed to update ship fuel: %w", err)
			}
		} else {
			if _, err := fresh.RefuelToFull(); err != nil {
				return fmt.Errorf("failed to update ship fuel: %w", err)
			}
		}
		if err := r.Save(ctx, fresh); err != nil {
			log.Printf("Warning: failed to persist ship %s after refuel: %v", ship.ShipSymbol(), err)
		}
		r.shipListCache.Delete(playerID.Value())
		ship.AdoptState(fresh)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return refuelResult, nil
```

`SetFlightMode` (line 404) — move the whole body (including the early-return equality check and the API call) INSIDE the turn, checking against `fresh`:

```go
func (r *ShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, mode string) error {
	return r.mailboxes.run(ctx, playerID, ship.ShipSymbol(), "set-flight-mode", func(ctx context.Context) error {
		fresh, err := r.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
		if err != nil {
			return fmt.Errorf("set flight mode: refetch %s: %w", ship.ShipSymbol(), err)
		}
		if fresh.FlightMode() == mode {
			ship.AdoptState(fresh)
			return nil
		}
		player, err := r.playerRepo.FindByID(ctx, playerID)
		if err != nil {
			return fmt.Errorf("failed to find player: %w", err)
		}
		if err := r.apiClient.SetFlightMode(ctx, ship.ShipSymbol(), mode, player.Token); err != nil {
			return fmt.Errorf("failed to set flight mode: %w", err)
		}
		fresh.SetFlightMode(mode)
		if err := r.Save(ctx, fresh); err != nil {
			log.Printf("Warning: failed to persist ship %s after set flight mode: %v", ship.ShipSymbol(), err)
		}
		r.shipListCache.Delete(playerID.Value())
		ship.AdoptState(fresh)
		return nil
	})
}
```

(SetFlightMode's API call inside the turn is a deliberate exception to the Task 4 note: the equality short-circuit needs fresh state BEFORE deciding to call, and mode changes are rare.)

- [ ] **Step 4: Run the full package**

Run: `rtk go test -race ./internal/adapters/api/`
Expected: PASS (new + existing repo tests: claim/dedication/power-slots untouched paths stay green)

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/api/
rtk git commit -m "feat(daemon): Navigate/Refuel/SetFlightMode run as serialized fresh-state mailbox turns (sp-eum3)"
```

---

### Task 6: Route the `ShipStateScheduler` through `Mutate`

**Files:**
- Modify: `internal/adapters/grpc/ship_state_scheduler.go` (ctor line 38; `handleArrival` line 84; `handleCooldownClear` line 158; `sweepStuckShips` line 275)
- Modify: `internal/adapters/grpc/daemon_server.go:187` (construction)
- Test: `internal/adapters/grpc/ship_state_scheduler_mutate_test.go`

**Interfaces:**
- Consumes: `navigation.ShipMutator`, `navigation.ErrSkipSave` (Task 3)
- Produces: `NewShipStateScheduler(shipRepo navigation.ShipRepository, clock shared.Clock, eventPublisher navigation.ShipEventPublisher, mutator navigation.ShipMutator) *ShipStateScheduler` — 4th parameter added. Daemon wiring passes the concrete repo: `shipRepo.(navigation.ShipMutator)` (unchecked assert — if a future repo impl lacks Mutate, the daemon must fail at boot, loudly, not limp with a racy scheduler).

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/grpc/ship_state_scheduler_mutate_test.go
package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeMutator records turns and executes fn against a canned ship, so the
// scheduler's transition logic is testable without a DB.
type fakeMutator struct {
	ship  *navigation.Ship
	ops   []string
	saved bool
}

func (f *fakeMutator) Mutate(ctx context.Context, _ shared.PlayerID, _ string, op string,
	fn func(ctx context.Context, ship *navigation.Ship) error) error {
	f.ops = append(f.ops, op)
	err := fn(ctx, f.ship)
	if err == navigation.ErrSkipSave {
		return nil
	}
	if err == nil {
		f.saved = true
	}
	return err
}

// handleArrival transitions IN_TRANSIT→IN_ORBIT through a Mutate turn (the
// serialized path) instead of the old Find→Arrive→Save free-for-all.
func TestHandleArrival_TransitionsThroughMutate(t *testing.T) {
	ship := reconstructTestShipInTransit(t, "TORWIND-9", 7, pastArrival())
	fm := &fakeMutator{ship: ship}
	s := NewShipStateScheduler(nil, &shared.MockClock{}, nil, fm)

	s.handleArrival("TORWIND-9", shared.MustNewPlayerID(7))

	require.Equal(t, []string{"arrival"}, fm.ops)
	require.True(t, fm.saved)
	require.True(t, ship.IsInOrbit())
	require.Nil(t, ship.ArrivalTime())
}

// A ship that already left IN_TRANSIT (the timer lost the race legitimately)
// skips the save entirely.
func TestHandleArrival_NotInTransitSkipsSave(t *testing.T) {
	ship := reconstructTestShipInOrbit(t, "TORWIND-9", 7)
	fm := &fakeMutator{ship: ship}
	s := NewShipStateScheduler(nil, &shared.MockClock{}, nil, fm)

	s.handleArrival("TORWIND-9", shared.MustNewPlayerID(7))

	require.False(t, fm.saved, "no write for a no-op arrival")
	require.True(t, ship.IsInOrbit())
}
```

Build the two small entity helpers (`reconstructTestShipInTransit` from Task 5 of the sp-i01z plan may already exist — reuse it; add `reconstructTestShipInOrbit` and `pastArrival()` the same way, via `navigation.ReconstructShip`, signature at `ship.go:810`).

- [ ] **Step 2: Run test to verify it fails**

Run: `rtk go test ./internal/adapters/grpc/ -run TestHandleArrival -v`
Expected: FAIL (ctor has 3 params; handleArrival doesn't use a mutator)

- [ ] **Step 3: Implement**

Ctor (line 38): add the parameter + field.

```go
type ShipStateScheduler struct {
	shipRepo       navigation.ShipRepository
	mutator        navigation.ShipMutator
	clock          shared.Clock
	eventPublisher navigation.ShipEventPublisher
	timers         map[string]*time.Timer
	mu             sync.Mutex
	stopCh         chan struct{}
}

// NewShipStateScheduler creates a new scheduler for ship state transitions.
// eventPublisher is optional - if nil, no events will be published.
// mutator is the serialized write path (sp-eum3): every transition this
// scheduler makes goes through the ship's mailbox turn.
func NewShipStateScheduler(shipRepo navigation.ShipRepository, clock shared.Clock, eventPublisher navigation.ShipEventPublisher, mutator navigation.ShipMutator) *ShipStateScheduler {
```

(keep the existing nil-clock default; store `mutator` in the struct.)

`handleArrival` (line 84) — replace the body's fetch/transition/save with one turn; publish AFTER the committed turn using values captured inside it:

```go
func (s *ShipStateScheduler) handleArrival(symbol string, playerID shared.PlayerID) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var location string
	var status navigation.NavStatus
	applied := false
	err := s.mutator.Mutate(ctx, playerID, symbol, "arrival", func(ctx context.Context, ship *navigation.Ship) error {
		if !ship.IsInTransit() {
			return navigation.ErrSkipSave // another writer already landed it
		}
		if err := ship.Arrive(); err != nil {
			return fmt.Errorf("transition to orbit: %w", err)
		}
		ship.ClearArrivalTime()
		if ship.CurrentLocation() != nil {
			location = ship.CurrentLocation().Symbol
		}
		status = ship.NavStatus()
		applied = true
		return nil
	})
	if err != nil {
		fmt.Printf("Warning: arrival turn for %s failed: %v\n", symbol, err)
	} else if applied {
		fmt.Printf("Ship %s arrived at %s\n", symbol, location)
		if s.eventPublisher != nil {
			s.eventPublisher.PublishArrived(symbol, playerID, location, status)
		}
	}

	// Cleanup timer reference
	s.mu.Lock()
	delete(s.timers, symbol)
	s.mu.Unlock()
}
```

`handleCooldownClear` (line 158) — same shape, with the correctness bump the old code lacked (never clear a cooldown that another writer RE-ARMED to a future time):

```go
	err := s.mutator.Mutate(ctx, playerID, symbol, "cooldown-clear", func(ctx context.Context, ship *navigation.Ship) error {
		exp := ship.CooldownExpiration()
		if exp == nil {
			return navigation.ErrSkipSave // already cleared
		}
		if exp.After(s.clock.Now()) {
			return navigation.ErrSkipSave // re-armed to a FUTURE cooldown by a newer operation — not ours to clear
		}
		ship.ClearCooldown()
		return nil
	})
	if err != nil {
		fmt.Printf("Warning: cooldown-clear turn for %s failed: %v\n", symbol, err)
	} else {
		fmt.Printf("Cooldown cleared for ship %s\n", symbol)
	}
```

(keep the surrounding ctx setup and timer-cleanup lines as they are.)

`sweepStuckShips` (line 275) — keep both discovery queries; replace each per-ship `Arrive/ClearArrivalTime/Save` and `ClearCooldown/Save` body with the SAME turn shapes as above (`"sweep-arrival"` / `"sweep-cooldown"` op names; the in-turn `IsInTransit`/expiration re-checks make the sweep race-proof against the timers it backstops). Publish `PublishArrived` after committed sweep-arrival turns exactly as `handleArrival` does.

Wiring, `daemon_server.go:187`:

```go
	shipStateScheduler := NewShipStateScheduler(shipRepo, clock, shipEventPublisher, shipRepo.(navigation.ShipMutator))
```

Run `rtk grep -rn 'NewShipStateScheduler(' --include='*.go' internal/ cmd/` and update every test constructor to pass a fourth argument (a `fakeMutator` or `nil` where the sweeper/timers aren't exercised — note the handlers deref it, so tests exercising them must pass a fake).

- [ ] **Step 4: Run the scheduler + regression suites**

Run: `rtk go test ./internal/adapters/grpc/ -run 'TestHandleArrival|TestRunSweeper|TestArrivalTimerPanic' -v`
Expected: PASS
Run the coordinator-level dockrace regressions (they exercise the same class at the application layer and must stay green):
Run: `rtk go test ./internal/application/trading/commands/ -run 'TestTradeRoute_DockStep_ResyncsAndRetriesTheNavCacheRace|TestTradeRouteCoordinator'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/grpc/
rtk git commit -m "feat(daemon): scheduler arrival/cooldown/sweeper writes run as Mutate turns (sp-eum3)"
```

---

### Task 7: Full verification

- [ ] **Step 1: Vet + full suite + race on the touched packages**

Run: `rtk go vet ./... && rtk go test ./... && rtk go test -race ./internal/adapters/api/ ./internal/adapters/grpc/`
Expected: all PASS.

- [ ] **Step 2: Build**

Run: `rtk go build -o /tmp/spacetraders-daemon-mailbox ./cmd/spacetraders-daemon && rm /tmp/spacetraders-daemon-mailbox`
Expected: clean build.

- [ ] **Step 3: Close out — the after-measurement**

`bd update sp-eum3 --append-notes="implementation complete: mailbox core, Mutate port, 5 verbs serialized, scheduler turns"`, then the repo session-close protocol (commit, `bd dolt push`, `git push`). **After deploy, re-read `spacetraders_ship_version_conflicts_total` against the Task 1 baseline recorded in sp-60ff**: the rate on mailbox-routed paths must trend to ~0; whatever remains is the ranked worklist for bead sp-wa7c (the paired ERROR logs name the ships/paths).

---

## Deliberately out of scope (beads filed)

- **sp-wa7c** — migrate the ~20 external `FindBySymbol → mutate → Save` call sites (list in the bead: gas siphon/transfer, cargo_transaction, jump_ship, outfitting, route_executor, worker lifecycle managers, coordinators' direct Saves, `refresh_ship`, `purchase_ship`, `scout_markets`, `container_ops_scout_posts`, `container_runner` release path, `daemon_server`) to `ShipMutator.Mutate`. The Task 1 tripwire ranks them by actual conflict frequency.
- `SaveAll` (`ship_repository.go:850`) keeps legacy semantics (no known callers found; used by bulk sync at most) — flag in sp-wa7c.
- Sync paths (`SyncShipFromAPI`/`SyncAllFromAPI`, the `UpdateAll` writers at `:1643`/`:1706`) stay outside the mailbox and the CAS guard: startup/new-hull API imports, not steady-state contention. The tripwire will say if that assumption is wrong.
- `ClaimShip`/`ReserveForCaptain`/`ReleaseCaptainReservation`/`AssignFleet`/`SetCargoReservation`: already column-scoped under `SELECT ... FOR UPDATE` — correct as-is, untouched.

## Design decisions (for the reviewer)

1. **Probe first (sp-60ff), mailbox gated on the reading**: the dockrace class is proven (sp-n7yp incident + regression tests), but the app layer's resync-retry mitigations mask its *current* frequency. One day of telemetry converts "should we build the mailbox" from argument into measurement — and the tripwire stays valuable forever either way (permanent regression alarm + sp-wa7c's ranking signal).
2. **Mailbox inside the repository, not a separate service**: all five verbs get serialized with zero call-site churn, and the repo is the only component that owns both the API client and the row — the turn = the whole read-modify-write.
3. **Narrow `ShipMutator` port instead of widening `navigation.ShipRepository`**: the big port has many fakes across 169 test files; adding a method breaks all of them for zero benefit.
4. **Re-find fresh + `AdoptState` copy-back**: serialization alone still writes stale snapshots (phantom arrival, stale assignment columns). Fresh-apply is the semantic fix; copy-back preserves every caller's "read the entity after the verb" contract.
5. **API call outside the turn (except SetFlightMode)**: rate-limited API calls can stall 30s under burst backoff; holding the mailbox through them would starve arrival turns. Dock/orbit are idempotent; navigate's API-then-turn ordering matches today's API-then-save ordering.
6. **Version CAS is detection-only**: flipping conflicts to hard failures would change ~20 call sites' error behavior in one shot. Loud-but-preserving converts the migration (sp-wa7c) from a leap into a measured, metric-guided walk.
7. **No actor reaping**: bounded by fleet size; a teardown/enqueue race is real complexity for ~zero memory.
