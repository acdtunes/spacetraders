# Autonomous Captain — Core Loop Implementation Plan (1 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the event outbox, the captain supervisor binary, and the `captain/` workspace so a `claude -p` strategist runs the fleet on events + heartbeat (spec rollout phases 1–2).

**Architecture:** The gobot daemon records strategic events into a new Postgres `captain_events` table (outbox pattern). A new `cmd/captain` binary polls that table, detects synthetic conditions (idle ships, stale heartbeats, credit thresholds), composes a fleet-snapshot prompt, and invokes `claude -p --model opus` in the `captain/` workspace, where memory files (`captain-log.md`, `strategy.md`, `lessons.md`, `decisions.jsonl`) give cross-session learning.

**Tech Stack:** Go 1.25, GORM (Postgres prod / SQLite `:memory:` tests), cobra, viper, testify. LLM runtime: local `claude` CLI on the user's Max subscription.

**Spec:** `docs/superpowers/specs/2026-07-02-autonomous-captain-design.md` — read it before starting.

## Global Constraints

- Module path: `github.com/andrescamacho/spacetraders-go`. Repo root for all paths below: `gobot/` unless the path starts with `captain/`, `docs/`, or `claude-captain/` (those are monorepo-root relative).
- Migrations are hand-run raw SQL pairs in `gobot/migrations/NNN_name.up.sql` / `.down.sql`. Next free number is `030`. Also add every new model to `AutoMigrate` in `gobot/internal/infrastructure/database/connection.go` (tests depend on it).
- Tests: testify only, co-located `*_test.go` files. Use `database.NewTestConnection()` (in-memory SQLite) for DB tests. There are currently ZERO test files in the repo — you are setting the pattern; keep tests black-box (test exported behavior, not internals).
- SQLite compatibility: JSON columns are Go `string` with `gorm:"type:jsonb"`; never use Postgres-only types in Go model tags beyond what existing models use.
- Logging: this codebase uses `fmt.Printf`/`log` — do NOT introduce zap or any logging dependency.
- The captain session must never see `ANTHROPIC_API_KEY` (it would silently bill the API instead of the Max subscription).
- `claude -p` model flag value comes from config; default `"opus"`.
- Commit after every task with the message given in the task's final step.
- Run `make build-daemon` (and `go vet ./...`) before every commit that touches `gobot/`.

---

### Task 1: Captain domain events + `captain_events` outbox (migration, model, repository)

**Files:**
- Create: `gobot/internal/domain/captain/events.go`
- Create: `gobot/migrations/030_add_captain_events_table.up.sql`
- Create: `gobot/migrations/030_add_captain_events_table.down.sql`
- Create: `gobot/internal/adapters/persistence/captain_event_repository.go`
- Create: `gobot/internal/adapters/persistence/captain_event_repository_test.go`
- Modify: `gobot/internal/adapters/persistence/models.go` (append `CaptainEventModel`)
- Modify: `gobot/internal/infrastructure/database/connection.go` (add model to `AutoMigrate` list, around line 86)

**Interfaces:**
- Consumes: existing `database.NewTestConnection()`, GORM.
- Produces: `captain.Event`, `captain.EventType` constants, `captain.EventRecorder` (write-only port used by the daemon), `captain.EventStore` (read/write port used by the supervisor), `persistence.NewGormCaptainEventRepository(db *gorm.DB) *GormCaptainEventRepository` implementing `captain.EventStore`.

- [ ] **Step 1: Write the domain types**

`gobot/internal/domain/captain/events.go`:

```go
// Package captain defines the strategic-event outbox consumed by the
// autonomous captain supervisor (see docs/superpowers/specs/2026-07-02-autonomous-captain-design.md).
package captain

import (
	"context"
	"time"
)

type EventType string

const (
	EventWorkflowFinished  EventType = "workflow.finished"
	EventWorkflowFailed    EventType = "workflow.failed"
	EventContainerCrashed  EventType = "container.crashed"
	EventHeartbeatLost     EventType = "container.heartbeat_lost"
	EventShipIdle          EventType = "ship.idle"
	EventCreditsThreshold  EventType = "credits.threshold"
	EventContractCompleted EventType = "contract.completed"
	EventContractFailed    EventType = "contract.failed"
)

type Event struct {
	ID          int64
	Type        EventType
	Ship        string // ship symbol, empty when not ship-scoped
	PlayerID    int
	Payload     string // JSON object with event-specific detail
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

// EventRecorder is the write-only port the daemon uses.
type EventRecorder interface {
	Record(ctx context.Context, e *Event) error
}

// EventStore is the full port the captain supervisor uses.
type EventStore interface {
	EventRecorder
	// FindUnprocessed returns events with ProcessedAt IS NULL, oldest first.
	FindUnprocessed(ctx context.Context, playerID int, limit int) ([]*Event, error)
	MarkProcessed(ctx context.Context, ids []int64, at time.Time) error
	// HasUnprocessed reports whether an unprocessed event of the given type
	// exists for the ship (used to avoid duplicate synthetic events).
	HasUnprocessed(ctx context.Context, playerID int, t EventType, ship string) (bool, error)
}
```

- [ ] **Step 2: Write the SQL migration**

`gobot/migrations/030_add_captain_events_table.up.sql`:

```sql
-- Strategic-event outbox for the autonomous captain (spec: 2026-07-02-autonomous-captain-design.md)
CREATE TABLE IF NOT EXISTS captain_events (
    id           BIGSERIAL PRIMARY KEY,
    player_id    INTEGER NOT NULL REFERENCES players(id) ON UPDATE CASCADE ON DELETE CASCADE,
    type         VARCHAR(50) NOT NULL,
    ship         VARCHAR(100) NOT NULL DEFAULT '',
    payload      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    processed_at TIMESTAMP WITH TIME ZONE
);
CREATE INDEX IF NOT EXISTS idx_captain_events_unprocessed
    ON captain_events(player_id, created_at) WHERE processed_at IS NULL;
COMMENT ON TABLE captain_events IS 'Outbox of strategic events consumed by the captain supervisor';
```

`gobot/migrations/030_add_captain_events_table.down.sql`:

```sql
DROP TABLE IF EXISTS captain_events;
```

- [ ] **Step 3: Add the GORM model**

Append to `gobot/internal/adapters/persistence/models.go`:

```go
// CaptainEventModel represents the captain_events strategic-event outbox
type CaptainEventModel struct {
	ID          int64        `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID    int          `gorm:"column:player_id;index:idx_captain_events_player;not null"`
	Player      *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Type        string       `gorm:"column:type;size:50;not null"`
	Ship        string       `gorm:"column:ship;size:100;not null;default:''"`
	Payload     string       `gorm:"column:payload;type:jsonb"`
	CreatedAt   time.Time    `gorm:"column:created_at;not null;autoCreateTime"`
	ProcessedAt *time.Time   `gorm:"column:processed_at"`
}

func (CaptainEventModel) TableName() string {
	return "captain_events"
}
```

In `gobot/internal/infrastructure/database/connection.go`, add `&persistence.CaptainEventModel{},` to the `AutoMigrate(...)` list after `&persistence.TransactionModel{},`.

- [ ] **Step 4: Write the failing repository test**

`gobot/internal/adapters/persistence/captain_event_repository_test.go`:

```go
package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func setupCaptainEventRepo(t *testing.T) (*persistence.GormCaptainEventRepository, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewGormCaptainEventRepository(db), player.ID
}

func TestCaptainEventLifecycle(t *testing.T) {
	repo, playerID := setupCaptainEventRepo(t)
	ctx := context.Background()

	e := &captain.Event{Type: captain.EventWorkflowFailed, Ship: "SHIP-1",
		PlayerID: playerID, Payload: `{"error":"boom"}`}
	require.NoError(t, repo.Record(ctx, e))

	got, err := repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, captain.EventWorkflowFailed, got[0].Type)
	require.Equal(t, "SHIP-1", got[0].Ship)
	require.NotZero(t, got[0].ID)
	require.Nil(t, got[0].ProcessedAt)

	dup, err := repo.HasUnprocessed(ctx, playerID, captain.EventWorkflowFailed, "SHIP-1")
	require.NoError(t, err)
	require.True(t, dup)

	require.NoError(t, repo.MarkProcessed(ctx, []int64{got[0].ID}, time.Now()))
	got, err = repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Empty(t, got)

	dup, err = repo.HasUnprocessed(ctx, playerID, captain.EventWorkflowFailed, "SHIP-1")
	require.NoError(t, err)
	require.False(t, dup)
}

func TestFindUnprocessedOrdersOldestFirstAndScopesPlayer(t *testing.T) {
	repo, playerID := setupCaptainEventRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventShipIdle, Ship: "A", PlayerID: playerID}))
	require.NoError(t, repo.Record(ctx, &captain.Event{Type: captain.EventShipIdle, Ship: "B", PlayerID: playerID}))

	got, err := repo.FindUnprocessed(ctx, playerID, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "A", got[0].Ship)

	other, err := repo.FindUnprocessed(ctx, playerID+999, 10)
	require.NoError(t, err)
	require.Empty(t, other)
}
```

- [ ] **Step 5: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/adapters/persistence/ -run TestCaptainEvent -v`
Expected: FAIL — `undefined: persistence.NewGormCaptainEventRepository`

- [ ] **Step 6: Implement the repository**

`gobot/internal/adapters/persistence/captain_event_repository.go`:

```go
package persistence

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type GormCaptainEventRepository struct {
	db *gorm.DB
}

var _ captain.EventStore = (*GormCaptainEventRepository)(nil)

func NewGormCaptainEventRepository(db *gorm.DB) *GormCaptainEventRepository {
	return &GormCaptainEventRepository{db: db}
}

func (r *GormCaptainEventRepository) Record(ctx context.Context, e *captain.Event) error {
	payload := e.Payload
	if payload == "" {
		payload = "{}"
	}
	model := CaptainEventModel{
		PlayerID: e.PlayerID,
		Type:     string(e.Type),
		Ship:     e.Ship,
		Payload:  payload,
	}
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		return err
	}
	e.ID = model.ID
	e.CreatedAt = model.CreatedAt
	return nil
}

func (r *GormCaptainEventRepository) FindUnprocessed(ctx context.Context, playerID int, limit int) ([]*captain.Event, error) {
	var models []CaptainEventModel
	q := r.db.WithContext(ctx).
		Where("player_id = ? AND processed_at IS NULL", playerID).
		Order("created_at ASC, id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&models).Error; err != nil {
		return nil, err
	}
	events := make([]*captain.Event, 0, len(models))
	for i := range models {
		events = append(events, modelToCaptainEvent(&models[i]))
	}
	return events, nil
}

func (r *GormCaptainEventRepository) MarkProcessed(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&CaptainEventModel{}).
		Where("id IN ?", ids).
		Update("processed_at", at).Error
}

func (r *GormCaptainEventRepository) HasUnprocessed(ctx context.Context, playerID int, t captain.EventType, ship string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&CaptainEventModel{}).
		Where("player_id = ? AND type = ? AND ship = ? AND processed_at IS NULL", playerID, string(t), ship).
		Count(&count).Error
	return count > 0, err
}

func modelToCaptainEvent(m *CaptainEventModel) *captain.Event {
	return &captain.Event{
		ID:          m.ID,
		Type:        captain.EventType(m.Type),
		Ship:        m.Ship,
		PlayerID:    m.PlayerID,
		Payload:     m.Payload,
		CreatedAt:   m.CreatedAt,
		ProcessedAt: m.ProcessedAt,
	}
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/adapters/persistence/ -run TestCaptainEvent -v && go test ./internal/adapters/persistence/ -run TestFindUnprocessed -v`
Expected: PASS (both)

- [ ] **Step 8: Commit**

```bash
git add gobot/internal/domain/captain gobot/migrations/030_add_captain_events_table.up.sql gobot/migrations/030_add_captain_events_table.down.sql gobot/internal/adapters/persistence/captain_event_repository.go gobot/internal/adapters/persistence/captain_event_repository_test.go gobot/internal/adapters/persistence/models.go gobot/internal/infrastructure/database/connection.go
git commit -m "feat(captain): add captain_events outbox (domain port, migration, GORM repo)"
```

---

### Task 2: Record workflow/crash events from the daemon's container lifecycle

**Files:**
- Modify: `gobot/internal/adapters/grpc/container_runner.go` (functions `signalCompletionWithStatus` ~line 389 and `handleError` ~line 442)
- Create: `gobot/internal/adapters/grpc/captain_recorder.go`
- Create: `gobot/internal/adapters/grpc/captain_recorder_test.go`
- Modify: `gobot/cmd/spacetraders-daemon/main.go` (wire recorder, right after `shipEventBus := ship.NewShipEventBus()` ~line 210)

**Interfaces:**
- Consumes: `captain.EventRecorder`, `persistence.NewGormCaptainEventRepository` (Task 1); existing `ContainerRunner` fields `containerEntity` (exposes ID/PlayerID/metadata — inspect `internal/domain/container` for exact getters before coding; `signalCompletionWithStatus` already extracts `ship_symbol` from container metadata, reuse that same extraction).
- Produces: `grpc.SetCaptainEventRecorder(rec captain.EventRecorder)` — package-level injection so the ~17 `NewContainerRunner` call sites don't all change.

**Why package-level:** `ContainerRunner.SetEventPublisher` exists but is never called anywhere; per-site wiring was already forgotten once. A single package-level recorder set once in `main` cannot be forgotten per-site.

- [ ] **Step 1: Write the failing test**

`gobot/internal/adapters/grpc/captain_recorder_test.go`:

```go
package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type fakeRecorder struct{ events []*captain.Event }

func (f *fakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

func TestRecordCaptainEventNoopWhenUnset(t *testing.T) {
	SetCaptainEventRecorder(nil)
	// must not panic
	recordCaptainEvent(captain.EventWorkflowFailed, "SHIP-1", 1, map[string]any{"error": "x"})
}

func TestRecordCaptainEventForwards(t *testing.T) {
	f := &fakeRecorder{}
	SetCaptainEventRecorder(f)
	defer SetCaptainEventRecorder(nil)

	recordCaptainEvent(captain.EventWorkflowFinished, "SHIP-2", 7, map[string]any{"container_id": "c-1"})

	require.Len(t, f.events, 1)
	require.Equal(t, captain.EventWorkflowFinished, f.events[0].Type)
	require.Equal(t, "SHIP-2", f.events[0].Ship)
	require.Equal(t, 7, f.events[0].PlayerID)
	require.Contains(t, f.events[0].Payload, "c-1")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/grpc/ -run TestRecordCaptainEvent -v`
Expected: FAIL — `undefined: SetCaptainEventRecorder`

- [ ] **Step 3: Implement the recorder shim**

`gobot/internal/adapters/grpc/captain_recorder.go`:

```go
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

var (
	captainRecorderMu sync.RWMutex
	captainRecorder   captain.EventRecorder
)

// SetCaptainEventRecorder wires the strategic-event outbox. Called once from
// the daemon main; nil disables recording (tests, CLI-only runs).
func SetCaptainEventRecorder(rec captain.EventRecorder) {
	captainRecorderMu.Lock()
	defer captainRecorderMu.Unlock()
	captainRecorder = rec
}

// recordCaptainEvent is fire-and-forget: outbox failures must never break
// container execution, so errors are printed and swallowed.
func recordCaptainEvent(t captain.EventType, ship string, playerID int, payload map[string]any) {
	captainRecorderMu.RLock()
	rec := captainRecorder
	captainRecorderMu.RUnlock()
	if rec == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Record(ctx, &captain.Event{
		Type: t, Ship: ship, PlayerID: playerID, Payload: string(raw),
	}); err != nil {
		fmt.Printf("captain outbox: failed to record %s: %v\n", t, err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/adapters/grpc/ -run TestRecordCaptainEvent -v`
Expected: PASS

- [ ] **Step 5: Hook the container lifecycle**

In `gobot/internal/adapters/grpc/container_runner.go`:

(a) In `signalCompletionWithStatus(success bool, errMsg string)` (~line 389): the function currently early-returns when `r.eventPublisher == nil`. Insert the captain recording BEFORE that early return, reusing the same container-ID / player-ID / ship-symbol extraction the function already performs for `WorkerCompletedEvent` (read the function body first and reuse its exact metadata lookups — do not invent new ones):

```go
	eventType := captain.EventWorkflowFinished
	if !success {
		eventType = captain.EventWorkflowFailed
	}
	recordCaptainEvent(eventType, shipSymbol, playerID, map[string]any{
		"container_id":   containerID,
		"command_type":   r.containerEntity.CommandType(), // adjust to the actual accessor used in this file
		"success":        success,
		"error":          errMsg,
	})
```

If the ship/player/container variables are only computed after the nil-publisher early return, lift those extractions above the early return so both consumers share them.

(b) In `handleError(err error)` (~line 442), after the container is marked failed, add:

```go
	recordCaptainEvent(captain.EventContainerCrashed, shipSymbol, playerID, map[string]any{
		"container_id": containerID,
		"error":        err.Error(),
	})
```

Again reuse the identifiers already available in that function (it persists `ContainerStatusFailed`, so container ID and player ID are at hand; use empty string for ship if not available there).

Add the import `"github.com/andrescamacho/spacetraders-go/internal/domain/captain"` to the file.

(c) In `gobot/cmd/spacetraders-daemon/main.go`, immediately after `shipEventBus := ship.NewShipEventBus()`:

```go
	captainEventRepo := persistence.NewGormCaptainEventRepository(db)
	grpc.SetCaptainEventRecorder(captainEventRepo)
	fmt.Println("Captain event outbox initialized")
```

(`persistence` and `grpc` are already imported in this file.)

- [ ] **Step 6: Build and run the package tests**

Run: `cd gobot && go build ./... && go test ./internal/adapters/grpc/ -run TestRecordCaptainEvent -v`
Expected: build OK, tests PASS

- [ ] **Step 7: Commit**

```bash
git add gobot/internal/adapters/grpc/captain_recorder.go gobot/internal/adapters/grpc/captain_recorder_test.go gobot/internal/adapters/grpc/container_runner.go gobot/cmd/spacetraders-daemon/main.go
git commit -m "feat(captain): record workflow finished/failed and container crash events from daemon"
```

---

### Task 3: Restore the `spacetraders` CLI binary (captain's hands)

The cobra CLI package (`internal/adapters/cli`) is complete, but `cmd/spacetraders/main.go` is missing from the tree, so `make build-cli` fails and nothing can invoke the CLI. The captain acts exclusively through this binary — it must build.

**Files:**
- Create: `gobot/cmd/spacetraders/main.go`

**Interfaces:**
- Consumes: `cli.Execute()` from `gobot/internal/adapters/cli/root.go`.
- Produces: `bin/spacetraders` binary (used by Task 8's CLI reference generator and by every captain session).

- [ ] **Step 1: Write main.go**

`gobot/cmd/spacetraders/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

Check `cli.Execute()`'s actual signature in `internal/adapters/cli/root.go` first — if it returns nothing (calls `os.Exit` itself), drop the error handling accordingly.

- [ ] **Step 2: Build and smoke-test**

Run: `cd gobot && make build-cli && ./bin/spacetraders --help`
Expected: build succeeds; help lists commands `config, player, ship, shipyard, market, contract, goods, ledger, workflow, container, health, operations, construction`

- [ ] **Step 3: Commit**

```bash
git add gobot/cmd/spacetraders/main.go
git commit -m "fix(cli): restore missing cmd/spacetraders main so the CLI builds"
```

---

### Task 4: `captain` config section

**Files:**
- Create: `gobot/internal/infrastructure/config/captain.go`
- Create: `gobot/internal/infrastructure/config/captain_test.go`
- Modify: `gobot/internal/infrastructure/config/config.go` (add field to `Config`)
- Modify: `gobot/internal/infrastructure/config/defaults.go` (defaults)
- Modify: `gobot/config.yaml.example` (documented block)

**Interfaces:**
- Consumes: existing viper `LoadConfig` flow.
- Produces: `config.CaptainConfig` with fields listed below, reachable as `cfg.Captain`.

- [ ] **Step 1: Write the failing test**

`gobot/internal/infrastructure/config/captain_test.go`:

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptainDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.False(t, cfg.Captain.Enabled)
	require.Equal(t, 30, cfg.Captain.PollIntervalSeconds)
	require.Equal(t, 45, cfg.Captain.HeartbeatMinutes)
	require.Equal(t, 6, cfg.Captain.MaxSessionsPerHour)
	require.Equal(t, 10, cfg.Captain.SessionTimeoutMinutes)
	require.Equal(t, 30, cfg.Captain.ShipIdleMinutes)
	require.Equal(t, 5, cfg.Captain.StaleHeartbeatMinutes)
	require.Equal(t, "claude", cfg.Captain.ClaudeBin)
	require.Equal(t, "opus", cfg.Captain.Model)
	require.Equal(t, "../captain", cfg.Captain.WorkspaceDir)
	require.Equal(t, []int{100000, 250000, 500000, 1000000}, cfg.Captain.CreditsThresholds)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/infrastructure/config/ -run TestCaptainDefaults -v`
Expected: FAIL — `cfg.Captain undefined`

- [ ] **Step 3: Implement config**

`gobot/internal/infrastructure/config/captain.go`:

```go
package config

// CaptainConfig configures the autonomous captain supervisor (cmd/captain).
type CaptainConfig struct {
	Enabled               bool   `mapstructure:"enabled"`
	PlayerID              int    `mapstructure:"player_id" validate:"omitempty,min=1"`
	WorkspaceDir          string `mapstructure:"workspace_dir"`
	ClaudeBin             string `mapstructure:"claude_bin"`
	Model                 string `mapstructure:"model"`
	PollIntervalSeconds   int    `mapstructure:"poll_interval_seconds" validate:"omitempty,min=5"`
	HeartbeatMinutes      int    `mapstructure:"heartbeat_minutes" validate:"omitempty,min=1"`
	MaxSessionsPerHour    int    `mapstructure:"max_sessions_per_hour" validate:"omitempty,min=1"`
	SessionTimeoutMinutes int    `mapstructure:"session_timeout_minutes" validate:"omitempty,min=1"`
	ShipIdleMinutes       int    `mapstructure:"ship_idle_minutes" validate:"omitempty,min=1"`
	StaleHeartbeatMinutes int    `mapstructure:"stale_heartbeat_minutes" validate:"omitempty,min=1"`
	CreditsThresholds     []int  `mapstructure:"credits_thresholds"`
}
```

In `config.go` add to the `Config` struct:

```go
	Captain CaptainConfig `mapstructure:"captain"`
```

In `defaults.go` add (follow the file's existing style of guarded assignments):

```go
	// Captain defaults
	if cfg.Captain.PollIntervalSeconds == 0 {
		cfg.Captain.PollIntervalSeconds = 30
	}
	if cfg.Captain.HeartbeatMinutes == 0 {
		cfg.Captain.HeartbeatMinutes = 45
	}
	if cfg.Captain.MaxSessionsPerHour == 0 {
		cfg.Captain.MaxSessionsPerHour = 6
	}
	if cfg.Captain.SessionTimeoutMinutes == 0 {
		cfg.Captain.SessionTimeoutMinutes = 10
	}
	if cfg.Captain.ShipIdleMinutes == 0 {
		cfg.Captain.ShipIdleMinutes = 30
	}
	if cfg.Captain.StaleHeartbeatMinutes == 0 {
		cfg.Captain.StaleHeartbeatMinutes = 5
	}
	if cfg.Captain.ClaudeBin == "" {
		cfg.Captain.ClaudeBin = "claude"
	}
	if cfg.Captain.Model == "" {
		cfg.Captain.Model = "opus"
	}
	if cfg.Captain.WorkspaceDir == "" {
		cfg.Captain.WorkspaceDir = "../captain"
	}
	if len(cfg.Captain.CreditsThresholds) == 0 {
		cfg.Captain.CreditsThresholds = []int{100000, 250000, 500000, 1000000}
	}
```

Append to `gobot/config.yaml.example`:

```yaml
# Autonomous captain supervisor (cmd/captain)
captain:
  enabled: false           # master switch; also see captain/DISABLED kill-switch file
  player_id: 1             # which player the captain commands
  workspace_dir: ../captain
  claude_bin: claude       # local Claude Code CLI (Max subscription; never set ANTHROPIC_API_KEY)
  model: opus
  poll_interval_seconds: 30
  heartbeat_minutes: 45
  max_sessions_per_hour: 6
  session_timeout_minutes: 10
  ship_idle_minutes: 30
  stale_heartbeat_minutes: 5
  credits_thresholds: [100000, 250000, 500000, 1000000]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/infrastructure/config/ -run TestCaptainDefaults -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/infrastructure/config/captain.go gobot/internal/infrastructure/config/captain_test.go gobot/internal/infrastructure/config/config.go gobot/internal/infrastructure/config/defaults.go gobot/config.yaml.example
git commit -m "feat(config): add captain supervisor config section with defaults"
```

---

### Task 5: Workspace + decision ledger readers (`internal/captain`)

The supervisor needs read access to the captain's memory files: kill switches, file tails for prompt composition, and the `decisions.jsonl` ledger to find decisions due for review.

**Files:**
- Create: `gobot/internal/captain/workspace.go`
- Create: `gobot/internal/captain/workspace_test.go`
- Create: `gobot/internal/captain/decisions.go`
- Create: `gobot/internal/captain/decisions_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (pure file I/O).
- Produces:
  - `captainsup.Workspace` struct: `NewWorkspace(dir string) Workspace`, methods `Disabled() bool` (checks `<dir>/DISABLED`), `StatePath(name string) string`, `Tail(name string, maxBytes int) string` (last maxBytes of `<dir>/state/<name>`, empty string if missing), `ReadFull(name string) string`.
  - `captainsup.Decision` struct with JSON tags matching the spec: `id, ts, action, rationale, expectation, review_after, outcome, verdict, lesson`, all strings except `ReviewAfter time.Time` (`review_after`, RFC3339) and `Outcome *string`.
  - `captainsup.ReadDecisions(path string) ([]Decision, error)` (skips malformed lines), `captainsup.DueForReview(ds []Decision, now time.Time) []Decision`.
  - Package name is `captainsup` (supervisor-side), distinct from domain package `captain`.

- [ ] **Step 1: Write the failing tests**

`gobot/internal/captain/workspace_test.go`:

```go
package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledKillSwitch(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspace(dir)
	require.False(t, ws.Disabled())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DISABLED"), nil, 0o644))
	require.True(t, ws.Disabled())
}

func TestTailReturnsLastBytesAndEmptyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.Equal(t, "", ws.Tail("captain-log.md", 100))

	content := "OLD-OLD-OLD\nNEW-TAIL"
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), []byte(content), 0o644))
	require.Equal(t, "NEW-TAIL", ws.Tail("captain-log.md", 8))
	require.Equal(t, content, ws.Tail("captain-log.md", 10000))
}
```

`gobot/internal/captain/decisions_test.go`:

```go
package captainsup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadDecisionsSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	lines := `{"id":"d-1","action":"buy hauler","expectation":"utilization +10%","review_after":"2026-07-01T00:00:00Z"}
not json at all
{"id":"d-2","action":"start arbitrage","expectation":"+40k in 3h","review_after":"2099-01-01T00:00:00Z"}
{"id":"d-3","action":"done thing","expectation":"x","review_after":"2026-07-01T00:00:00Z","outcome":"worked"}
`
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o644))

	ds, err := ReadDecisions(path)
	require.NoError(t, err)
	require.Len(t, ds, 3)

	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	due := DueForReview(ds, now)
	require.Len(t, due, 1) // d-1: past review_after, no outcome. d-2 future. d-3 has outcome.
	require.Equal(t, "d-1", due[0].ID)
}

func TestReadDecisionsMissingFileIsEmpty(t *testing.T) {
	ds, err := ReadDecisions(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.NoError(t, err)
	require.Empty(t, ds)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -v`
Expected: FAIL — package does not exist yet / undefined identifiers

- [ ] **Step 3: Implement workspace.go**

`gobot/internal/captain/workspace.go`:

```go
// Package captainsup implements the captain supervisor: the deterministic
// plumbing that turns outbox events + heartbeats into claude -p sessions.
package captainsup

import (
	"os"
	"path/filepath"
)

type Workspace struct {
	dir string
}

func NewWorkspace(dir string) Workspace { return Workspace{dir: dir} }

func (w Workspace) Dir() string { return w.dir }

// Disabled reports the master kill switch (a plain file, flippable over SSH).
func (w Workspace) Disabled() bool {
	_, err := os.Stat(filepath.Join(w.dir, "DISABLED"))
	return err == nil
}

func (w Workspace) StatePath(name string) string {
	return filepath.Join(w.dir, "state", name)
}

// Tail returns up to maxBytes from the end of state/<name>; "" if missing.
func (w Workspace) Tail(name string, maxBytes int) string {
	data, err := os.ReadFile(w.StatePath(name))
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return string(data)
}

// ReadFull returns the whole state/<name>; "" if missing.
func (w Workspace) ReadFull(name string) string {
	data, err := os.ReadFile(w.StatePath(name))
	if err != nil {
		return ""
	}
	return string(data)
}
```

- [ ] **Step 4: Implement decisions.go**

`gobot/internal/captain/decisions.go`:

```go
package captainsup

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Decision mirrors one line of state/decisions.jsonl (spec: Learning loop §1).
type Decision struct {
	ID          string    `json:"id"`
	TS          string    `json:"ts,omitempty"`
	Action      string    `json:"action"`
	Rationale   string    `json:"rationale,omitempty"`
	Expectation string    `json:"expectation"`
	ReviewAfter time.Time `json:"review_after"`
	Outcome     *string   `json:"outcome,omitempty"`
	Verdict     string    `json:"verdict,omitempty"`
	Lesson      string    `json:"lesson,omitempty"`
}

// ReadDecisions parses decisions.jsonl, skipping malformed lines (the file is
// LLM-written; one bad line must not poison the ledger). Missing file = empty.
func ReadDecisions(path string) ([]Decision, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Decision
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var d Decision
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil || d.ID == "" {
			continue
		}
		out = append(out, d)
	}
	return out, scanner.Err()
}

// DueForReview: review_after has passed and no outcome recorded yet.
func DueForReview(ds []Decision, now time.Time) []Decision {
	var due []Decision
	for _, d := range ds {
		if d.Outcome == nil && !d.ReviewAfter.IsZero() && d.ReviewAfter.Before(now) {
			due = append(due, d)
		}
	}
	return due
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -v`
Expected: PASS (all 4 tests)

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/captain/workspace.go gobot/internal/captain/workspace_test.go gobot/internal/captain/decisions.go gobot/internal/captain/decisions_test.go
git commit -m "feat(captain): workspace kill-switch/tail helpers and decision ledger reader"
```

---

### Task 6: Snapshot composer + synthetic-event detectors

**Files:**
- Create: `gobot/internal/captain/snapshot.go`
- Create: `gobot/internal/captain/snapshot_test.go`
- Create: `gobot/internal/captain/detectors.go`
- Create: `gobot/internal/captain/detectors_test.go`

**Interfaces:**
- Consumes: `captain.EventStore` (Task 1), `Workspace`/`ReadDecisions`/`DueForReview` (Task 5), GORM models `persistence.ShipModel`, `persistence.ContainerModel`, `persistence.TransactionModel`.
- Produces:
  - `captainsup.ComposeSnapshot(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, events []*captain.Event, now time.Time) (string, error)` — the full prompt text for a strategy session.
  - `captainsup.RunDetectors(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error` with `DetectorConfig{PlayerID int; ShipIdle time.Duration; StaleHeartbeat time.Duration; CreditsThresholds []int; LastCredits int}` returning nothing but recording synthetic `ship.idle`, `container.heartbeat_lost`, `credits.threshold` events (deduped via `HasUnprocessed`).
  - `captainsup.CurrentCredits(ctx context.Context, db *gorm.DB, playerID int) (int, error)` — latest `transactions.balance_after` for the player (0 if no transactions).

- [ ] **Step 1: Write the failing detector tests**

`gobot/internal/captain/detectors_test.go`:

```go
package captainsup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func setupDB(t *testing.T) (*gorm.DB, int, *persistence.GormCaptainEventRepository) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	return db, p.ID, persistence.NewGormCaptainEventRepository(db)
}

func TestDetectStaleHeartbeat(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	stale := now.Add(-10 * time.Minute)
	fresh := now.Add(-1 * time.Minute)
	started := now.Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-stale", PlayerID: playerID, Status: "RUNNING", HeartbeatAt: &stale, StartedAt: &started,
	}).Error)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-fresh", PlayerID: playerID, Status: "RUNNING", HeartbeatAt: &fresh, StartedAt: &started,
	}).Error)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: 5 * time.Minute,
		ShipIdle: time.Hour, CreditsThresholds: nil}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventHeartbeatLost, events[0].Type)
	require.Contains(t, events[0].Payload, "c-stale")

	// Running detectors again must not duplicate the event.
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))
	events, err = store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestDetectCreditsThresholdCrossing(t *testing.T) {
	db, playerID, store := setupDB(t)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now, TransactionType: "SELL",
		Category: "TRADING_REVENUE", Amount: 5000, BalanceBefore: 98000, BalanceAfter: 103000,
	}).Error)

	credits, err := CurrentCredits(context.Background(), db, playerID)
	require.NoError(t, err)
	require.Equal(t, 103000, credits)

	cfg := DetectorConfig{PlayerID: playerID, StaleHeartbeat: time.Hour, ShipIdle: time.Hour,
		CreditsThresholds: []int{100000, 250000}, LastCredits: 98000}
	require.NoError(t, RunDetectors(context.Background(), db, store, cfg, now))

	events, err := store.FindUnprocessed(context.Background(), playerID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, captain.EventCreditsThreshold, events[0].Type)
	require.Contains(t, events[0].Payload, "100000")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestDetect -v`
Expected: FAIL — `undefined: DetectorConfig`, `RunDetectors`, `CurrentCredits`

- [ ] **Step 3: Implement detectors.go**

`gobot/internal/captain/detectors.go`:

```go
package captainsup

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type DetectorConfig struct {
	PlayerID          int
	ShipIdle          time.Duration
	StaleHeartbeat    time.Duration
	CreditsThresholds []int
	LastCredits       int // credits at the previous poll; 0 disables crossing detection
}

// RunDetectors writes synthetic strategic events for conditions that are
// state (not daemon events): stale heartbeats, idle ships, credit crossings.
// Dedup: an event is skipped while an unprocessed twin exists.
func RunDetectors(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if err := detectStaleHeartbeats(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectIdleShips(ctx, db, store, cfg, now); err != nil {
		return err
	}
	return detectCreditsCrossing(ctx, db, store, cfg)
}

func detectStaleHeartbeats(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	cutoff := now.Add(-cfg.StaleHeartbeat)
	var stale []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ? AND heartbeat_at IS NOT NULL AND heartbeat_at < ?",
			cfg.PlayerID, "RUNNING", cutoff).
		Find(&stale).Error; err != nil {
		return err
	}
	for _, c := range stale {
		dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventHeartbeatLost, c.ID)
		if err != nil || dup {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventHeartbeatLost, Ship: c.ID, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"container_id":%q,"command_type":%q,"last_heartbeat":%q}`,
				c.ID, c.CommandType, c.HeartbeatAt.UTC().Format(time.RFC3339)),
		})
	}
	return nil
}

func detectIdleShips(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	// A ship is idle if it is not IN_TRANSIT and no RUNNING container's config
	// references it. Container config is JSON text; a LIKE match on the quoted
	// symbol is the pragmatic join (config stores "ship_symbol":"X").
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND nav_status != ?", cfg.PlayerID, "IN_TRANSIT").
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			continue
		}
		dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventShipIdle, s.ShipSymbol)
		if err != nil || dup {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventShipIdle, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"location":%q,"nav_status":%q}`, s.LocationSymbol, s.NavStatus),
		})
	}
	return nil
}

func detectCreditsCrossing(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig) error {
	if cfg.LastCredits == 0 || len(cfg.CreditsThresholds) == 0 {
		return nil
	}
	current, err := CurrentCredits(ctx, db, cfg.PlayerID)
	if err != nil {
		return err
	}
	for _, th := range cfg.CreditsThresholds {
		crossedUp := cfg.LastCredits < th && current >= th
		crossedDown := cfg.LastCredits >= th && current < th
		if !crossedUp && !crossedDown {
			continue
		}
		direction := "up"
		if crossedDown {
			direction = "down"
		}
		key := fmt.Sprintf("%d", th)
		dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventCreditsThreshold, key)
		if err != nil || dup {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventCreditsThreshold, Ship: key, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"threshold":%d,"direction":%q,"credits":%d}`, th, direction, current),
		})
	}
	return nil
}

// CurrentCredits reads the latest transaction balance for the player.
func CurrentCredits(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC").
		Limit(1).
		Find(&tx).Error
	if err != nil {
		return 0, err
	}
	return tx.BalanceAfter, nil
}
```

Note: `detectIdleShips` intentionally does not use `ShipIdle` duration yet — ships have no "idle since" column. Idle = not in transit AND unassigned, deduped by the unprocessed-event check. The `ShipIdle` field stays in `DetectorConfig` so the supervisor (Task 7) can pass it; document this in the struct if the linter flags it, but keep the field.

- [ ] **Step 4: Run detector tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run TestDetect -v`
Expected: PASS. (If `TestDetectStaleHeartbeat` finds 2+ events, the idle-ship detector fired for a ship you didn't create — it shouldn't, there are no ships in that fixture.)

- [ ] **Step 5: Write the failing snapshot test**

`gobot/internal/captain/snapshot_test.go`:

```go
package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

func TestComposeSnapshotContainsAllSections(t *testing.T) {
	db, playerID, _ := setupDB(t)
	now := time.Now()

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, NavStatus: "DOCKED",
		LocationSymbol: "X1-A1", FuelCurrent: 300, FuelCapacity: 400,
		CargoUnits: 10, CargoCapacity: 40,
	}).Error)
	started := now.Add(-30 * time.Minute)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-1", PlayerID: playerID, Status: "RUNNING", CommandType: "arbitrage",
		StartedAt: &started, HeartbeatAt: &now,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now.Add(-2 * time.Hour), TransactionType: "SELL",
		Category: "TRADING_REVENUE", Amount: 4000, BalanceBefore: 96000, BalanceAfter: 100000,
	}).Error)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.NoError(t, os.WriteFile(ws.StatePath("strategy.md"), []byte("PRIORITIZE MANUFACTURING"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("lessons.md"), []byte("LESSON: never overfuel"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("decisions.jsonl"),
		[]byte(`{"id":"d-1","action":"test arb","expectation":"+40k in 3h","review_after":"2020-01-01T00:00:00Z"}`+"\n"), 0o644))

	events := []*captain.Event{{ID: 5, Type: captain.EventWorkflowFailed, Ship: "SHIP-1",
		PlayerID: playerID, Payload: `{"error":"no fuel"}`, CreatedAt: now}}

	prompt, err := ComposeSnapshot(context.Background(), db, ws, playerID, events, now)
	require.NoError(t, err)

	for _, want := range []string{
		"## Pending events", "workflow.failed", "no fuel",
		"## Fleet", "SHIP-1", "DOCKED", "X1-A1",
		"## Active containers", "c-1", "arbitrage",
		"## Treasury", "100000",
		"## Decisions due for review", "d-1", "+40k in 3h",
		"## Standing strategy", "PRIORITIZE MANUFACTURING",
		"## Lessons", "never overfuel",
		"## Your obligations this session",
	} {
		require.Contains(t, prompt, want, "missing section content: %s", want)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd gobot && go test ./internal/captain/ -run TestComposeSnapshot -v`
Expected: FAIL — `undefined: ComposeSnapshot`

- [ ] **Step 7: Implement snapshot.go**

`gobot/internal/captain/snapshot.go`:

```go
package captainsup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

const (
	logTailBytes    = 8 * 1024
	maxDueDecisions = 20
)

// ComposeSnapshot builds the full prompt for a strategy session: fleet state,
// KPIs, pending events, due decisions, and memory. The captain spends its
// turns deciding, not fetching (spec: Component 3).
func ComposeSnapshot(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, events []*captain.Event, now time.Time) (string, error) {
	var b strings.Builder

	b.WriteString("# Fleet situation report\n")
	b.WriteString("Generated: " + now.UTC().Format(time.RFC3339) + "\n\n")

	// Pending events
	b.WriteString("## Pending events\n")
	if len(events) == 0 {
		b.WriteString("(none — heartbeat review)\n")
	}
	for _, e := range events {
		b.WriteString(fmt.Sprintf("- [%d] %s ship=%s at %s payload=%s\n",
			e.ID, e.Type, e.Ship, e.CreatedAt.UTC().Format(time.RFC3339), e.Payload))
	}

	// Fleet
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).Where("player_id = ?", playerID).Find(&ships).Error; err != nil {
		return "", err
	}
	b.WriteString("\n## Fleet\n")
	for _, s := range ships {
		b.WriteString(fmt.Sprintf("- %s: %s at %s fuel=%d/%d cargo=%d/%d\n",
			s.ShipSymbol, s.NavStatus, s.LocationSymbol,
			s.FuelCurrent, s.FuelCapacity, s.CargoUnits, s.CargoCapacity))
	}

	// Containers
	var containers []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ?", playerID, "RUNNING").
		Find(&containers).Error; err != nil {
		return "", err
	}
	b.WriteString("\n## Active containers\n")
	if len(containers) == 0 {
		b.WriteString("(none running)\n")
	}
	for _, c := range containers {
		age := "?"
		if c.StartedAt != nil {
			age = now.Sub(*c.StartedAt).Round(time.Minute).String()
		}
		b.WriteString(fmt.Sprintf("- %s: %s running for %s\n", c.ID, c.CommandType, age))
	}

	// Treasury / KPIs from ledger
	credits, err := CurrentCredits(ctx, db, playerID)
	if err != nil {
		return "", err
	}
	var dayAgoTx persistence.TransactionModel
	_ = db.WithContext(ctx).
		Where("player_id = ? AND timestamp >= ?", playerID, now.Add(-24*time.Hour)).
		Order("timestamp ASC").Limit(1).Find(&dayAgoTx).Error
	b.WriteString("\n## Treasury\n")
	b.WriteString(fmt.Sprintf("- Credits: %d\n", credits))
	if dayAgoTx.ID != "" {
		delta := credits - dayAgoTx.BalanceBefore
		b.WriteString(fmt.Sprintf("- 24h delta: %+d (≈ %+d credits/hour)\n", delta, delta/24))
	} else {
		b.WriteString("- 24h delta: no transactions in window\n")
	}

	// Decisions due for review (Learning loop §2 — forced outcome review)
	decisions, err := ReadDecisions(ws.StatePath("decisions.jsonl"))
	if err != nil {
		return "", err
	}
	due := DueForReview(decisions, now)
	if len(due) > maxDueDecisions {
		due = due[:maxDueDecisions]
	}
	b.WriteString("\n## Decisions due for review\n")
	if len(due) == 0 {
		b.WriteString("(none)\n")
	}
	for _, d := range due {
		raw, _ := json.Marshal(d)
		b.WriteString("- " + string(raw) + "\n")
	}

	// Memory
	b.WriteString("\n## Standing strategy (state/strategy.md)\n")
	b.WriteString(ws.ReadFull("strategy.md") + "\n")
	b.WriteString("\n## Lessons (state/lessons.md)\n")
	b.WriteString(ws.ReadFull("lessons.md") + "\n")
	b.WriteString("\n## Recent log tail (state/captain-log.md)\n")
	b.WriteString(ws.Tail("captain-log.md", logTailBytes) + "\n")

	// Session contract (details live in the workspace CLAUDE.md; this is the reminder)
	b.WriteString(`
## Your obligations this session
1. Close every decision listed under "Decisions due for review": append an
   updated JSONL line to state/decisions.jsonl with outcome (worked|failed|inconclusive),
   verdict notes, and a lesson for failures/surprises.
2. Assess the pending events and fleet state; act via the spacetraders CLI.
3. Record every non-trivial action as a new decision line with a measurable
   expectation and review_after time.
4. Append a dated entry to state/captain-log.md (decisions + rationale + friction: tags).
5. Revise state/strategy.md if KPIs disagree with its targets; curate state/lessons.md.
`)
	return b.String(), nil
}
```

- [ ] **Step 8: Run all package tests**

Run: `cd gobot && go test ./internal/captain/ -v`
Expected: PASS (workspace, decisions, detectors, snapshot)

- [ ] **Step 9: Commit**

```bash
git add gobot/internal/captain/snapshot.go gobot/internal/captain/snapshot_test.go gobot/internal/captain/detectors.go gobot/internal/captain/detectors_test.go
git commit -m "feat(captain): snapshot composer and synthetic-event detectors"
```

---

### Task 7: Session runner + supervisor loop

**Files:**
- Create: `gobot/internal/captain/session.go`
- Create: `gobot/internal/captain/session_test.go`
- Create: `gobot/internal/captain/supervisor.go`
- Create: `gobot/internal/captain/supervisor_test.go`

**Interfaces:**
- Consumes: everything above — `captain.EventStore`, `ComposeSnapshot`, `RunDetectors`, `Workspace`, `config.CaptainConfig`.
- Produces:
  - `captainsup.SessionRunner` interface: `Run(ctx context.Context, prompt string) error`.
  - `captainsup.ErrUsageLimit` sentinel error.
  - `captainsup.NewClaudeRunner(bin, model, workDir string, timeout time.Duration) *ClaudeRunner` implementing `SessionRunner` (invokes `bin -p --model <model>` with prompt on stdin, cwd=workDir, env scrubbed of `ANTHROPIC_API_KEY`).
  - `captainsup.NewSupervisor(db *gorm.DB, store captain.EventStore, runner SessionRunner, ws Workspace, cfg config.CaptainConfig) *Supervisor` with methods `Tick(ctx context.Context, now time.Time) (ran bool, err error)` (single testable iteration) and `Run(ctx context.Context) error` (ticker loop calling Tick every PollInterval).

- [ ] **Step 1: Write the failing session-runner test**

`gobot/internal/captain/session_test.go` (uses a stub shell script as the "claude" binary):

```go
package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeStub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-stub")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755))
	return path
}

func TestClaudeRunnerPassesPromptAndScrubsAPIKey(t *testing.T) {
	out := filepath.Join(t.TempDir(), "capture")
	stub := writeStub(t, `cat > `+out+`.prompt; echo "$ANTHROPIC_API_KEY" > `+out+`.key; echo "args: $@" > `+out+`.args`)
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")

	r := NewClaudeRunner(stub, "opus", t.TempDir(), time.Minute)
	require.NoError(t, r.Run(context.Background(), "HELLO CAPTAIN"))

	prompt, _ := os.ReadFile(out + ".prompt")
	require.Equal(t, "HELLO CAPTAIN", string(prompt))
	key, _ := os.ReadFile(out + ".key")
	require.Equal(t, "\n", string(key), "ANTHROPIC_API_KEY must be scrubbed")
	args, _ := os.ReadFile(out + ".args")
	require.Contains(t, string(args), "-p")
	require.Contains(t, string(args), "--model opus")
}

func TestClaudeRunnerDetectsUsageLimit(t *testing.T) {
	stub := writeStub(t, `echo "You have reached your usage limit" >&2; exit 1`)
	r := NewClaudeRunner(stub, "opus", t.TempDir(), time.Minute)
	err := r.Run(context.Background(), "x")
	require.ErrorIs(t, err, ErrUsageLimit)
}

func TestClaudeRunnerTimesOut(t *testing.T) {
	stub := writeStub(t, `sleep 5`)
	r := NewClaudeRunner(stub, "opus", t.TempDir(), 100*time.Millisecond)
	err := r.Run(context.Background(), "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUsageLimit)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestClaudeRunner -v`
Expected: FAIL — `undefined: NewClaudeRunner`

- [ ] **Step 3: Implement session.go**

`gobot/internal/captain/session.go`:

```go
package captainsup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrUsageLimit means the Max-subscription window is exhausted. This is a
// normal state, not a failure: the supervisor backs off and events queue.
var ErrUsageLimit = errors.New("claude usage limit reached")

type SessionRunner interface {
	Run(ctx context.Context, prompt string) error
}

type ClaudeRunner struct {
	Bin     string
	Model   string
	WorkDir string
	Timeout time.Duration
}

var _ SessionRunner = (*ClaudeRunner)(nil)

func NewClaudeRunner(bin, model, workDir string, timeout time.Duration) *ClaudeRunner {
	return &ClaudeRunner{Bin: bin, Model: model, WorkDir: workDir, Timeout: timeout}
}

func (r *ClaudeRunner) Run(ctx context.Context, prompt string) error {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Bin, "-p", "--model", r.Model)
	cmd.Dir = r.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Scrub ANTHROPIC_API_KEY: with it set, claude bills the API instead of
	// the Max subscription (spec: LLM runtime).
	env := os.Environ()
	scrubbed := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	cmd.Env = scrubbed

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		combined := strings.ToLower(out.String() + " " + errBuf.String())
		if strings.Contains(combined, "usage limit") || strings.Contains(combined, "rate limit") {
			return fmt.Errorf("%w: %s", ErrUsageLimit, strings.TrimSpace(errBuf.String()))
		}
		return fmt.Errorf("claude session failed: %w (stderr: %s)", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}
```

- [ ] **Step 4: Run session tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run TestClaudeRunner -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Write the failing supervisor tests**

`gobot/internal/captain/supervisor_test.go`:

```go
package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type stubRunner struct {
	prompts []string
	err     error
}

func (s *stubRunner) Run(_ context.Context, prompt string) error {
	s.prompts = append(s.prompts, prompt)
	return s.err
}

func newTestSupervisor(t *testing.T, runner SessionRunner) (*Supervisor, *captainStores) {
	t.Helper()
	db, playerID, store := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	cfg := config.CaptainConfig{
		Enabled: true, PlayerID: playerID, WorkspaceDir: dir,
		PollIntervalSeconds: 30, HeartbeatMinutes: 45, MaxSessionsPerHour: 6,
		SessionTimeoutMinutes: 10, ShipIdleMinutes: 30, StaleHeartbeatMinutes: 5,
	}
	sup := NewSupervisor(db, store, runner, NewWorkspace(dir), cfg)
	return sup, &captainStores{store: store, playerID: playerID, dir: dir}
}

type captainStores struct {
	store    captain.EventStore
	playerID int
	dir      string
}

func TestTickNoTriggerNoSession(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now() // heartbeat not due

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
	require.Empty(t, runner.prompts)
}

func TestTickRunsOnEventAndMarksProcessed(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFailed, Ship: "S", PlayerID: s.playerID, Payload: `{"error":"x"}`}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, runner.prompts, 1)
	require.Contains(t, runner.prompts[0], "workflow.failed")

	left, err := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, err)
	require.Empty(t, left, "events must be marked processed after a successful session")
}

func TestTickHeartbeatTriggersWithoutEvents(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now().Add(-2 * time.Hour)

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Contains(t, runner.prompts[0], "heartbeat review")
}

func TestTickFailedSessionLeavesEventsUnprocessed(t *testing.T) {
	runner := &stubRunner{err: ErrUsageLimit}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventShipIdle, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.True(t, ran)
	require.ErrorIs(t, err, ErrUsageLimit)

	left, lerr := s.store.FindUnprocessed(context.Background(), s.playerID, 10)
	require.NoError(t, lerr)
	require.Len(t, left, 1, "failed session must leave events for retry")
}

func TestTickRespectsKillSwitch(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "DISABLED"), nil, 0o644))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
}

func TestTickRespectsHourlyCap(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventShipIdle, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "cap reached: events queue, no session")
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestTick -v`
Expected: FAIL — `undefined: Supervisor`

- [ ] **Step 7: Implement supervisor.go**

`gobot/internal/captain/supervisor.go`:

```go
package captainsup

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

const eventBatchLimit = 50

// Supervisor is pure plumbing: it decides WHEN a session runs, never WHAT
// the captain does (spec: Component 2).
type Supervisor struct {
	db     *gorm.DB
	store  captain.EventStore
	runner SessionRunner
	ws     Workspace
	cfg    config.CaptainConfig

	lastSession   time.Time
	lastCredits   int
	sessionStarts []time.Time
}

func NewSupervisor(db *gorm.DB, store captain.EventStore, runner SessionRunner, ws Workspace, cfg config.CaptainConfig) *Supervisor {
	return &Supervisor{db: db, store: store, runner: runner, ws: ws, cfg: cfg}
}

// Tick performs one supervisor iteration. Returns ran=true when a session was
// attempted (successfully or not).
func (s *Supervisor) Tick(ctx context.Context, now time.Time) (bool, error) {
	if s.ws.Disabled() {
		return false, nil
	}

	// Synthetic events (state-derived): stale heartbeats, idle ships, credit crossings.
	dcfg := DetectorConfig{
		PlayerID:          s.cfg.PlayerID,
		ShipIdle:          time.Duration(s.cfg.ShipIdleMinutes) * time.Minute,
		StaleHeartbeat:    time.Duration(s.cfg.StaleHeartbeatMinutes) * time.Minute,
		CreditsThresholds: s.cfg.CreditsThresholds,
		LastCredits:       s.lastCredits,
	}
	if err := RunDetectors(ctx, s.db, s.store, dcfg, now); err != nil {
		return false, fmt.Errorf("detectors: %w", err)
	}
	if credits, err := CurrentCredits(ctx, s.db, s.cfg.PlayerID); err == nil {
		s.lastCredits = credits
	}

	events, err := s.store.FindUnprocessed(ctx, s.cfg.PlayerID, eventBatchLimit)
	if err != nil {
		return false, err
	}
	heartbeatDue := now.Sub(s.lastSession) >= time.Duration(s.cfg.HeartbeatMinutes)*time.Minute
	if len(events) == 0 && !heartbeatDue {
		return false, nil
	}
	if s.sessionsInLastHour(now) >= s.cfg.MaxSessionsPerHour {
		fmt.Printf("captain: session cap reached (%d/h), %d events queued\n",
			s.cfg.MaxSessionsPerHour, len(events))
		return false, nil
	}

	prompt, err := ComposeSnapshot(ctx, s.db, s.ws, s.cfg.PlayerID, events, now)
	if err != nil {
		return false, err
	}

	s.sessionStarts = append(s.sessionStarts, now)
	s.lastSession = now
	fmt.Printf("captain: starting session (%d events, heartbeatDue=%v)\n", len(events), heartbeatDue)
	if err := s.runner.Run(ctx, prompt); err != nil {
		// Events stay unprocessed → retried next tick. Usage limit is normal.
		return true, err
	}

	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if err := s.store.MarkProcessed(ctx, ids, now); err != nil {
		return true, fmt.Errorf("mark processed: %w", err)
	}
	fmt.Printf("captain: session complete, %d events processed\n", len(ids))
	return true, nil
}

// Run loops Tick on the poll interval until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) error {
	interval := time.Duration(s.cfg.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.Tick(ctx, time.Now()); err != nil {
				fmt.Printf("captain: tick error: %v\n", err)
			}
		}
	}
}

func (s *Supervisor) sessionsInLastHour(now time.Time) int {
	cutoff := now.Add(-time.Hour)
	kept := s.sessionStarts[:0]
	for _, t := range s.sessionStarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.sessionStarts = kept
	return len(kept)
}
```

- [ ] **Step 8: Run all captain tests**

Run: `cd gobot && go test ./internal/captain/ -v`
Expected: PASS (all)

- [ ] **Step 9: Commit**

```bash
git add gobot/internal/captain/session.go gobot/internal/captain/session_test.go gobot/internal/captain/supervisor.go gobot/internal/captain/supervisor_test.go
git commit -m "feat(captain): claude -p session runner and supervisor tick loop"
```

---

### Task 8: `cmd/captain` binary, Makefile targets, CLI reference generator

**Files:**
- Create: `gobot/cmd/captain/main.go`
- Create: `gobot/scripts/gen-cli-reference.sh`
- Modify: `gobot/Makefile` (add `build-captain`, `run-captain`, `cli-reference`; wire `build-captain` into `build`)

**Interfaces:**
- Consumes: `config.MustLoadConfig`, `database.NewConnection`, `persistence.NewGormCaptainEventRepository`, `captainsup.NewSupervisor`/`NewClaudeRunner`/`NewWorkspace` (Tasks 1–7).
- Produces: `bin/captain` binary with a `--once` flag (single Tick, for validation); `captain/CLI_REFERENCE.md` generated from the real binary's help tree.

- [ ] **Step 1: Write main.go**

`gobot/cmd/captain/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	captainsup "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func main() {
	once := flag.Bool("once", false, "run a single supervisor tick and exit")
	flag.Parse()

	cfg := config.MustLoadConfig("")
	if !cfg.Captain.Enabled && !*once {
		log.Fatal("captain.enabled is false in config; refusing to start (use --once to force a single tick)")
	}
	if cfg.Captain.PlayerID == 0 {
		log.Fatal("captain.player_id must be set in config")
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close(db)

	store := persistence.NewGormCaptainEventRepository(db)
	ws := captainsup.NewWorkspace(cfg.Captain.WorkspaceDir)
	runner := captainsup.NewClaudeRunner(
		cfg.Captain.ClaudeBin,
		cfg.Captain.Model,
		cfg.Captain.WorkspaceDir,
		time.Duration(cfg.Captain.SessionTimeoutMinutes)*time.Minute,
	)
	sup := captainsup.NewSupervisor(db, store, runner, ws, cfg.Captain)

	// Regenerate the CLI reference so sessions never see a stale command surface
	// (spec: Tool discovery §1). Best-effort: a missing binary must not stop the
	// supervisor, it only degrades tool discovery to --help fallback.
	if out, err := exec.Command("./scripts/gen-cli-reference.sh", "./bin/spacetraders",
		cfg.Captain.WorkspaceDir+"/CLI_REFERENCE.md").CombinedOutput(); err != nil {
		fmt.Printf("warning: CLI reference regeneration failed: %v: %s\n", err, out)
	}

	fmt.Printf("Captain supervisor starting (player=%d workspace=%s model=%s)\n",
		cfg.Captain.PlayerID, cfg.Captain.WorkspaceDir, cfg.Captain.Model)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		ran, err := sup.Tick(ctx, time.Now())
		fmt.Printf("tick: ran=%v err=%v\n", ran, err)
		return
	}
	if err := sup.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("supervisor: %v", err)
	}
}
```

- [ ] **Step 2: Write the CLI reference generator**

`gobot/scripts/gen-cli-reference.sh`:

```bash
#!/usr/bin/env bash
# Generates captain/CLI_REFERENCE.md from the spacetraders binary's --help tree.
# Regenerated at supervisor startup / make cli-reference so it can never drift
# from the installed binary (spec: Tool discovery §1).
set -euo pipefail

BIN="${1:-./bin/spacetraders}"
OUT="${2:-../captain/CLI_REFERENCE.md}"

if [ ! -x "$BIN" ]; then
  echo "error: $BIN not found or not executable (run: make build-cli)" >&2
  exit 1
fi

{
  echo "# spacetraders CLI reference"
  echo
  echo "> Generated by scripts/gen-cli-reference.sh — do not edit by hand."
  echo
  echo '```'
  "$BIN" --help 2>&1
  echo '```'

  # Walk one level of subcommands from the help output's Available Commands block.
  "$BIN" --help 2>&1 | awk '/Available Commands:/{f=1;next} /^Flags:/{f=0} f&&NF{print $1}' |
  while read -r cmd; do
    [ "$cmd" = "help" ] && continue
    echo
    echo "## spacetraders $cmd"
    echo '```'
    "$BIN" "$cmd" --help 2>&1
    echo '```'
    # Second level (e.g. container list, ledger report)
    "$BIN" "$cmd" --help 2>&1 | awk '/Available Commands:/{f=1;next} /^Flags:/{f=0} f&&NF{print $1}' |
    while read -r sub; do
      [ "$sub" = "help" ] && continue
      echo
      echo "### spacetraders $cmd $sub"
      echo '```'
      "$BIN" "$cmd" "$sub" --help 2>&1
      echo '```'
    done
  done
} > "$OUT"

echo "wrote $OUT"
```

Make it executable: `chmod +x gobot/scripts/gen-cli-reference.sh`

- [ ] **Step 3: Add Makefile targets**

In `gobot/Makefile`, change the `build` line to include the captain and add targets (match the file's existing tab-indented recipe style):

```makefile
build: build-cli build-daemon build-routing-service build-captain

build-captain:
	go build -o bin/captain ./cmd/captain

run-captain: build-captain
	./bin/captain

cli-reference: build-cli
	./scripts/gen-cli-reference.sh ./bin/spacetraders ../captain/CLI_REFERENCE.md
```

Add `build-captain`, `run-captain`, `cli-reference` to the `.PHONY` list.

- [ ] **Step 4: Build everything**

Run: `cd gobot && make build-captain && make build-cli && ./bin/captain --help`
Expected: builds succeed; `--help` shows the `--once` flag. (Do NOT run `make cli-reference` yet — the `captain/` directory is created in Task 9.)

- [ ] **Step 5: Commit**

```bash
git add gobot/cmd/captain/main.go gobot/scripts/gen-cli-reference.sh gobot/Makefile
git commit -m "feat(captain): cmd/captain binary with --once flag, CLI reference generator, make targets"
```

---

### Task 9: Captain workspace (`captain/` at monorepo root)

This task is files-only (no Go). It creates the workspace the sessions run in. Phase-1 (advisory) permissions: read-only CLI commands + memory-file writes only.

**Files:**
- Create: `captain/CLAUDE.md`
- Create: `captain/.claude/settings.json`
- Create: `captain/state/strategy.md`
- Create: `captain/state/lessons.md`
- Create: `captain/state/captain-log.md`
- Create: `captain/state/decisions.jsonl` (empty file)
- Create: `captain/reports/bugs/.gitkeep`
- Create: `captain/README.md`

**Interfaces:**
- Consumes: CLI reference generation (Task 8), the session contract expected by `ComposeSnapshot` (Task 6).
- Produces: the workspace `cmd/captain` points at via `captain.workspace_dir` config.

- [ ] **Step 1: Write CLAUDE.md**

`captain/CLAUDE.md`:

```markdown
# You are the Captain

You are the autonomous strategist for a SpaceTraders fleet. There is no human
in the loop: you decide, you act, you record, you learn. The Go daemon executes
tactics (navigation, mining loops, contract steps); you own strategy,
allocation, and recovery.

@CLI_REFERENCE.md

## How you act

- Your ONLY actuator is the `spacetraders` CLI (invoked via Bash). Binary path:
  `../gobot/bin/spacetraders`. If unsure about flags, run `<cmd> --help`.
- Never edit code, never touch files outside this workspace, never call APIs
  directly. If the bot itself is broken, write a bug report (see Escalation).

## Session contract (non-negotiable, in order)

1. **Close due decisions.** For every decision listed under "Decisions due for
   review" in your prompt, append an updated line to `state/decisions.jsonl`
   (same id) adding: `outcome` (worked|failed|inconclusive), `verdict` (one
   sentence: expected vs actual), and `lesson` when the outcome was failed or
   surprising.
2. **Assess and act.** Handle pending events first (failures before
   opportunities), then evaluate strategy vs KPIs.
3. **Record decisions.** Every non-trivial action gets a NEW line in
   `state/decisions.jsonl`:
   `{"id":"d-<next>","ts":"<now RFC3339>","action":"...","rationale":"...","expectation":"<measurable>","review_after":"<RFC3339>"}`
4. **Log.** Append a dated entry to `state/captain-log.md`: what you decided,
   why, and any `friction:` observations (tools you wished existed, data you
   had to derive by hand, repeated manual command chains).
5. **Maintain memory.** Revise `state/strategy.md` if KPIs disagree with its
   targets. Curate `state/lessons.md`: merge duplicates, generalize, prune
   lessons invalidated by bot changes. Hard cap: 50 lessons.

## Recovery playbook

On `workflow.failed`, `container.crashed`, or `container.heartbeat_lost`:
1. Inspect: `spacetraders container get <id>`, `spacetraders container logs <id>`,
   `spacetraders health`.
2. Correct: restart the workflow, reassign the ship, refuel, or stop the zombie
   container — whichever the evidence supports.
3. Record the incident in the log with the failure signature (command_type +
   error class).

## Escalation

The SAME failure signature 3+ times across sessions (check your log tail):
STOP retrying. Write `reports/bugs/YYYY-MM-DD-<slug>.md` containing: failure
signature, evidence (container ids, log excerpts), expected vs actual behavior,
and impact. Note it in the log, then work around it (mark the capability
degraded in strategy.md).

## Style

Decisive, evidence-first, cheap experiments before big commitments. When two
options are close, pick the one that is easier to reverse.
```

- [ ] **Step 2: Write settings.json (phase-1 advisory allowlist)**

`captain/.claude/settings.json`:

```json
{
  "permissions": {
    "allow": [
      "Read",
      "Edit(state/**)",
      "Write(state/**)",
      "Write(reports/**)",
      "Bash(../gobot/bin/spacetraders health)",
      "Bash(../gobot/bin/spacetraders ship list*)",
      "Bash(../gobot/bin/spacetraders ship get*)",
      "Bash(../gobot/bin/spacetraders market list*)",
      "Bash(../gobot/bin/spacetraders market get*)",
      "Bash(../gobot/bin/spacetraders ledger*)",
      "Bash(../gobot/bin/spacetraders container list*)",
      "Bash(../gobot/bin/spacetraders container get*)",
      "Bash(../gobot/bin/spacetraders container logs*)",
      "Bash(../gobot/bin/spacetraders contract list*)",
      "Bash(../gobot/bin/spacetraders workflow list*)",
      "Bash(../gobot/bin/spacetraders * --help)"
    ],
    "deny": []
  }
}
```

Verify each allowed subcommand actually exists in the generated CLI reference (Task 8) and adjust names to the real command tree (e.g. if it's `container get` vs `container inspect`). Phase 2 (spec rollout) later adds mutating commands: `navigate`, `dock`, `orbit`, `refuel`, `contract *`, `workflow *`, `container stop`, `shipyard *`, `operations *` — do NOT add them in this task.

- [ ] **Step 3: Seed state files**

`captain/state/strategy.md`:

```markdown
# Standing strategy

## KPI targets
- Credits/hour: establish a baseline over the first 24h of operation, then set
  a target 20% above baseline. Record the baseline here when known.
- Fleet utilization: no ship idle > 60 minutes without a recorded reason.

## Current posture
- Bootstrap mode: observe, learn the fleet, prefer contracts and proven trade
  routes over speculative arbitrage until the credits/hour baseline exists.

## Revision protocol
Revise this file at any heartbeat where actuals diverge from targets for 2+
consecutive sessions. Note the revision + reason in captain-log.md.
```

`captain/state/lessons.md` — seed by distilling `claude-captain/strategies.md`. Read that file and copy its concrete, still-valid heuristics as numbered lessons in this format (do this at execution time; if the file is missing, start with the header only):

```markdown
# Lessons (max 50 — curate ruthlessly)

Format: `L<N> [evidence: decision-ids] — heuristic`

L1 [seed] — Probes are cheap: keep 1 probe per 2-3 markets for price freshness
before committing haulers to a route.
<!-- Append seeded lessons from claude-captain/strategies.md here, then earned lessons below. -->
```

`captain/state/captain-log.md`:

```markdown
# Captain's log

<!-- Newest entries at the bottom. Supervisor may trim the oldest entries. -->
```

`captain/state/decisions.jsonl`: create empty (`touch captain/state/decisions.jsonl`).

`captain/reports/bugs/.gitkeep`: empty file.

- [ ] **Step 4: Write README.md**

`captain/README.md`:

```markdown
# Captain workspace

Working directory for autonomous `claude -p` strategy sessions, driven by
`gobot/cmd/captain`. See docs/superpowers/specs/2026-07-02-autonomous-captain-design.md.

- `CLAUDE.md` — persona + session contract loaded into every session
- `CLI_REFERENCE.md` — generated; run `make cli-reference` in gobot/ (do not edit)
- `state/` — the captain's memory (log, strategy, lessons, decision ledger)
- `reports/bugs/` — escalated failures awaiting the fix pipeline (plan 2 of 2)
- `DISABLED` — create this file to stop all sessions (kill switch)

Run one supervised tick manually: `cd ../gobot && make build && ./bin/captain --once`
```

- [ ] **Step 5: Generate the CLI reference and validate the allowlist**

Run: `cd gobot && make cli-reference && head -40 ../captain/CLI_REFERENCE.md`
Expected: file generated with the real command tree. Now cross-check every `Bash(...)` entry in `captain/.claude/settings.json` against it and fix names that don't match reality.

- [ ] **Step 6: Commit**

```bash
git add captain/
git commit -m "feat(captain): workspace — persona, session contract, advisory-mode permissions, seeded memory"
```

---

### Task 10: End-to-end advisory validation

**Files:**
- Modify: `captain/state/captain-log.md` (will gain an entry — evidence, not code)

**Interfaces:**
- Consumes: everything.
- Produces: verified phase-1 system.

- [ ] **Step 1: Apply the migration to the dev database**

Run: `psql "$DATABASE_URL" -f gobot/migrations/030_add_captain_events_table.up.sql`
Expected: `CREATE TABLE`, `CREATE INDEX`. (Use the same `DATABASE_URL` the daemon uses: `postgresql://spacetraders:dev_password@localhost:5432/spacetraders` unless overridden.)

- [ ] **Step 2: Configure and start the daemon**

In the gobot config (`config.yaml`, copied from `config.yaml.example` if absent), set `captain.enabled: true` and `captain.player_id` to a real player id (`SELECT id, agent_symbol FROM players;`). Then:

Run: `cd gobot && make build && make run-daemon` (in a separate terminal, leave running)
Expected: startup banner includes `Captain event outbox initialized`.

- [ ] **Step 3: Run one supervisor tick**

Run: `cd gobot && ./bin/captain --once`
Expected: `captain: starting session (N events, heartbeatDue=true)` (first run always heartbeats because lastSession is zero), then a real `claude -p` session runs in `captain/`; finally `tick: ran=true err=<nil>`. If `claude` is not on PATH or the Max window is exhausted, expect `err=claude usage limit reached` or an exec error — both leave events queued, which is correct behavior; fix the environment and re-run.

- [ ] **Step 4: Verify the session obeyed the contract**

Run: `tail -30 captain/state/captain-log.md && tail -5 captain/state/decisions.jsonl && psql "$DATABASE_URL" -c "SELECT type, ship, processed_at IS NOT NULL AS done FROM captain_events ORDER BY id DESC LIMIT 10;"`
Expected: a dated log entry exists; any due decisions were closed; consumed events show `done = t`. If the session wrote nothing, the prompt/CLAUDE.md contract needs tightening — iterate on `captain/CLAUDE.md` wording, not on Go code.

- [ ] **Step 5: Commit the evidence**

```bash
git add captain/state/
git commit -m "test(captain): first advisory-mode session transcript (phase 1 validation)"
```

---

## Deferred to Plan 2 (do not build here)

Fix pipeline (worktrees, gate, auto-merge, daemon restart), `fix_requested` handling, meta-review sessions, improvement backlog, feature budgets, `DISABLED_FIXES`. See `docs/superpowers/plans/2026-07-02-autonomous-captain-self-improvement.md`.
