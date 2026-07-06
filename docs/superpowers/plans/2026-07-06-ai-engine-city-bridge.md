# AI Engine City Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every `claude -p` in the fleet's AI engine with visible acd city agents (standing captain + crew), and every state file with beads — per the approved spec `docs/superpowers/specs/2026-07-06-ai-engine-city-bridge-design.md`.

**Architecture:** The Go supervisor (`internal/captain`, package `captainsup`) slims into a Watchkeeper: detectors, rate caps, backoff, and kill switch stay; prompt-building and `ClaudeRunner` die. Wakes become `gc mail send` + `gc session nudge` to a standing captain session. The Fixer's Go pipeline driver is replaced by a Shipwright city agent that codes in worktrees and invokes a new `captain-gate` CLI wrapping the existing `RunGate`/`SquashMerge`. All captain state migrates to the rig beads db (`sp-` prefix).

**Tech Stack:** Go 1.24 (gobot), cobra CLI, `gc`/`acd`/`bd` CLIs (exec'd from Go via an adapter), acd agent templates (markdown), launchd (service shape unchanged).

## Global Constraints

- **Hands untouched:** never modify `internal/adapters/grpc`, `internal/application/*` coordinators, or the daemon. This plan touches ONLY: `internal/captain/`, `cmd/captain/`, new `cmd/captain-gate/`, new `cmd/captain-migrate/`, `internal/adapters/cli/captain_ops.go`, `internal/infrastructure/config/` (captain section), `city/agents/*`, `dashboard/captain_dashboard.py`, docs.
- **Safety rails are Tier 3 by definition** (spec): the gate binary, Watchkeeper, kill-switch handling, and agent templates require Admiral sign-off to change — the plan builds them; future self-modification is out of bounds for the engine itself.
- Kill switches keep working at every step: `captain/DISABLED` (all nudging/sessions), `captain/DISABLED_FIXES` (pipeline only).
- Feature flag `captain.engine_mode: legacy | bridge` (default `legacy` until Task 9). Every task before Task 9 must leave `legacy` mode fully working.
- No comments in code beyond what existing files already practice. TDD per task. Run tests from `gobot/`: `go test ./internal/captain/... -count=1`.
- Beads for fleet ops live in the **rig db** (`sp-`): run `bd` with `cwd` = repo root (`/Users/andres.dandrea/IdeaProjects/cities/spacetraders`). City db (`st-`) is not touched by the engine.
- The `claude` binary is billed on the Max subscription — the GC adapter must never set `ANTHROPIC_API_KEY` in child env (same scrubbing rule as today's `session.go:50-58`).
- Commit after each task with the exact paths listed; never `git add .` (the tree carries unrelated dirty files).

## File Structure (end state)

```
gobot/internal/captain/
  supervisor.go        MODIFIED  Watchkeeper: detectors + caps + backoff + wake dispatch
  gc.go                NEW       CityGateway + BeadsClient adapters (exec gc/bd)
  gc_test.go           NEW
  wake.go              NEW       bridge-mode wake: compose event mail, nudge, re-nudge, escalate
  wake_test.go         NEW
  respawn.go           NEW       session-death respawn + shipwright orphan-bead requeue
  respawn_test.go      NEW
  fixer.go             MODIFIED  (Task 10) pipeline driver deleted; gate/worktree helpers stay
  session.go           DELETED   (Task 9, ClaudeRunner)
  snapshot.go          DELETED   (Task 9)
  metareview.go        DELETED   (Task 9)
  workspace.go         MODIFIED  (Task 9) slimmed to Disabled()/DisabledFixes()/Dir()
gobot/cmd/captain/main.go        MODIFIED  wiring per mode
gobot/cmd/captain-gate/main.go   NEW       gate CLI for the Shipwright
gobot/cmd/captain-migrate/main.go NEW      one-time files→beads migration
gobot/internal/adapters/cli/captain_ops.go NEW  `spacetraders captain events list|ack`
gobot/internal/infrastructure/config/*    MODIFIED  new captain fields
city/agents/captain/prompt.template.md    NEW
city/agents/shipwright/prompt.template.md NEW
city/agents/trade-analyst/prompt.template.md   NEW
city/agents/fleet-architect/prompt.template.md NEW
city/agents/{shipwright,trade-analyst,fleet-architect}/agent.toml NEW (model overrides)
dashboard/captain_dashboard.py   MODIFIED  (Task 12) read sessions/beads instead of dead files
```

---

### Task 1: Event ack/list CLI (`spacetraders captain events`)

The captain acks events by ID during its wake ritual; the Watchkeeper re-nudges unacked. The seam exists (`EventStore.MarkProcessed`, `internal/domain/captain/events.go:43`) — expose it.

**Files:**
- Create: `gobot/internal/adapters/cli/captain_ops.go`
- Modify: wherever root command registration lives — find with `grep -rn "NewShipCommand()" gobot/internal/adapters/cli/` (the same registrar adds `NewCaptainCommand()`)
- Test: `gobot/internal/adapters/cli/captain_ops_test.go`

**Interfaces:**
- Consumes: `captain.EventStore` (`FindUnprocessed(ctx, playerID, limit)`, `MarkProcessed(ctx, ids []int64, at time.Time)`), constructed the way other cli files build repos (copy the DB bootstrap from an existing cli file, e.g. how `waypoint.go` gets its repo).
- Produces: `spacetraders captain events list --player-id 1 [--json]` and `spacetraders captain events ack --player-id 1 --ids 12,13,14`. Later tasks (captain template) rely on these exact invocations.

- [ ] **Step 1: Write the failing test** — table test against a fake EventStore:

```go
type fakeEventStore struct {
	unprocessed []*captain.Event
	marked      []int64
}

func (f *fakeEventStore) FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error) {
	return f.unprocessed, nil
}
func (f *fakeEventStore) MarkProcessed(ctx context.Context, ids []int64, at time.Time) error {
	f.marked = append(f.marked, ids...)
	return nil
}

func TestCaptainEventsAckMarksParsedIDs(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,13,14")
	require.NoError(t, err)
	require.Equal(t, []int64{12, 13, 14}, fs.marked)
}

func TestCaptainEventsAckRejectsGarbage(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,abc")
	require.Error(t, err)
	require.Empty(t, fs.marked)
}
```

- [ ] **Step 2: Run** `go test ./internal/adapters/cli/ -run TestCaptainEvents -count=1` — FAIL (undefined `runEventsAck`).
- [ ] **Step 3: Implement** `captain_ops.go`: `runEventsAck(ctx, store, csv)` parses the CSV to `[]int64` (error on any bad token, atomic: parse all before marking), calls `MarkProcessed(ctx, ids, time.Now())`. `runEventsList(ctx, store, playerID, jsonOut)` prints `id  type  ship  created_at` rows or JSON. Wrap both in cobra commands under `NewCaptainCommand()` (mirror `NewShipCommand`'s structure at `ship.go:25-47`), register beside it.
- [ ] **Step 4: Run test — PASS.** Also `go build ./...`.
- [ ] **Step 5: Manual check (works with fleet stopped, DB up):** `docker ps | grep spacetraders-postgres && ./bin/spacetraders captain events list --player-id 1` → prints rows or empty; skip if DB down, note in report.
- [ ] **Step 6: Commit** `git add gobot/internal/adapters/cli/captain_ops.go gobot/internal/adapters/cli/captain_ops_test.go <registrar file>` ; message `feat(captain): events list/ack CLI for wake ritual`.

---

### Task 2: `captain-gate` CLI (the Shipwright's merge gate)

Wrap the existing gate exactly — no semantic changes. Today `Fixer.ProcessOne` calls `RunGate(moduleDir, timeout)` then stale-check then `SquashMerge` (`fixer.go:146-205`).

**Files:**
- Create: `gobot/cmd/captain-gate/main.go`
- Test: `gobot/internal/captain/gatecli_test.go` (test the extracted helper, not main)
- Modify: `gobot/internal/captain/fixer.go` — extract the gate sequence into an exported helper (mechanical extraction of :146-205's gate/stale/merge logic)

**Interfaces:**
- Consumes: existing `RunGate(moduleDir string, timeout time.Duration) (bool, string)`, `SquashMerge(repoDir, branch, msg string) error`, and the stale-base check as currently written in `ProcessOne` (read `fixer.go:170-186` for its exact form; extract, don't rewrite).
- Produces: exported `GateAndMerge(repoDir, worktreeDir, branch, commitMsg string, timeout time.Duration, merge bool) (GateResult, error)` where `GateResult struct { GatePassed bool; Stale bool; Merged bool; Log string }`; and the binary `captain-gate --repo <dir> --worktree <dir> --branch <name> --message <msg> [--merge] [--timeout 20m]` exiting 0 only when gate passed AND (merge not requested OR merged); prints the `GateResult` as JSON on stdout, gate log to stderr.

- [ ] **Step 1: Failing test** for the helper with a stub — since `RunGate` shells out to go build/test, test `GateAndMerge`'s decision logic by extracting it over injected funcs:

```go
func TestGateAndMergeRefusesMergeWhenGateFails(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return false, "boom" },
		func(string) (bool, error) { return false, nil },
		func(string, string, string) error { t.Fatal("must not merge"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.False(t, r.GatePassed)
	require.False(t, r.Merged)
}

func TestGateAndMergeRefusesMergeOnStaleBase(t *testing.T) {
	r := gateAndMergeWith(
		func(string, time.Duration) (bool, string) { return true, "ok" },
		func(string) (bool, error) { return true, nil },
		func(string, string, string) error { t.Fatal("must not merge stale"); return nil },
		"repo", "wt", "b", "msg", time.Minute, true)
	require.True(t, r.GatePassed)
	require.True(t, r.Stale)
	require.False(t, r.Merged)
}
```

- [ ] **Step 2: Run — FAIL** (undefined `gateAndMergeWith`).
- [ ] **Step 3: Implement**: `gateAndMergeWith(runGate, isStale, squashMerge, …)` holds the decision chain (gate → stale → merge-if-requested); `GateAndMerge` binds the real functions; `ProcessOne` now calls `GateAndMerge` (behavior identical — keep its status-file writes around the call for now, they die in Task 10); `cmd/captain-gate/main.go` is a flag-parse + `GateAndMerge` + JSON print (~60 lines).
- [ ] **Step 4: Tests PASS**; `go build ./...`; `go test ./internal/captain/... -count=1` green (existing fixer tests still pass — extraction was mechanical).
- [ ] **Step 5: Commit** `git add gobot/cmd/captain-gate gobot/internal/captain/fixer.go gobot/internal/captain/gatecli_test.go` ; `feat(captain): captain-gate CLI wrapping the unchanged merge gate`.

---

### Task 3: CityGateway + BeadsClient adapters (`gc.go`)

All city interaction from Go goes through one exec adapter, fake-able in tests.

**Files:**
- Create: `gobot/internal/captain/gc.go`, `gobot/internal/captain/gc_test.go`

**Interfaces (produced — later tasks depend on these exact names):**

```go
type Execer func(ctx context.Context, name string, args ...string) (stdout string, err error)

type CityGateway struct {
	GCBin   string        // "gc"
	CityDir string        // city root, used as cwd for gc
	Exec    Execer
}

func (g *CityGateway) SendMail(ctx context.Context, to, subject, body string) error
func (g *CityGateway) Nudge(ctx context.Context, alias, text string) error
func (g *CityGateway) SessionAlive(ctx context.Context, alias string) (bool, error)
func (g *CityGateway) SpawnSession(ctx context.Context, agent, alias string) error

type BeadsClient struct {
	BDBin  string // "bd"
	RigDir string // repo root — resolves the sp- db
	Exec   Execer
}

type PipelineBead struct{ ID, Type, Assignee string }

func (b *BeadsClient) ListInProgressPipeline(ctx context.Context) ([]PipelineBead, error) // bug+feature beads labeled "shipwright"
func (b *BeadsClient) Reopen(ctx context.Context, id, reason string) error
```

- [ ] **Step 1: Verify the real CLI shapes** (5 min, before coding — record exact flags in the test fixtures):

```bash
gc session nudge --help
gc session list --help        # look for --json and alias/state fields
gc session new --help
gc mail send --help
bd list --help | grep -E "json|label|status"
```

Expected: `nudge <alias> [message]`-style positional or `--message`; `session list` has machine-readable output; `mail send` takes `--to/--subject/--body` or positionals. Adjust the arg slices in Step 3 to what `--help` actually says — then the fake-exec tests pin those shapes.

- [ ] **Step 2: Failing tests** with a recording fake:

```go
func recordingExec(calls *[][]string, out string) Execer {
	return func(ctx context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return out, nil
	}
}

func TestNudgeInvokesGCSessionNudge(t *testing.T) {
	var calls [][]string
	g := &CityGateway{GCBin: "gc", CityDir: "/city", Exec: recordingExec(&calls, "")}
	require.NoError(t, g.Nudge(context.Background(), "captain", "3 events pending"))
	require.Len(t, calls, 1)
	require.Equal(t, "gc", calls[0][0])
	require.Contains(t, calls[0], "nudge")
	require.Contains(t, calls[0], "captain")
}

func TestSessionAliveParsesListOutput(t *testing.T) {
	var calls [][]string
	g := &CityGateway{Exec: recordingExec(&calls, `[{"alias":"captain","state":"active"}]`)}
	alive, err := g.SessionAlive(context.Background(), "captain")
	require.NoError(t, err)
	require.True(t, alive)
}

func TestReopenRunsBdUpdate(t *testing.T) {
	var calls [][]string
	b := &BeadsClient{BDBin: "bd", RigDir: "/rig", Exec: recordingExec(&calls, "")}
	require.NoError(t, b.Reopen(context.Background(), "sp-abc", "shipwright session died"))
	require.Contains(t, calls[0], "update")
	require.Contains(t, calls[0], "sp-abc")
}
```

- [ ] **Step 3: Implement.** Default `Execer` uses `exec.CommandContext` with `cmd.Dir` set (CityDir/RigDir) and env scrubbed of `ANTHROPIC_API_KEY` (copy the scrub loop from `session.go:50-58` before it's deleted). `SessionAlive` prefers `gc session list --json` + alias match on an active/running state; if `--json` is unsupported (Step 1 finding), fall back to parsing the table for the alias + state column. `SpawnSession` replicates headless `acd run`: `gc session new <agent> --alias <alias> --no-attach`, then prime by piping `acd prime <agent>` output into `gc session submit <alias>` (two Exec calls; if `submit` needs a flag for stdin vs arg, Step 1 told you).
- [ ] **Step 4: Tests PASS**; build green.
- [ ] **Step 5: Live smoke (safe, city running):** from gobot: write a tiny `go run` snippet or temporary test tagged `//go:build live` that calls `SendMail(ctx, "harbormaster", "gc.go smoke", "ignore")`, then check `acd mail inbox` shows it. Delete/skip-tag after confirming.
- [ ] **Step 6: Commit** `git add gobot/internal/captain/gc.go gobot/internal/captain/gc_test.go` ; `feat(captain): CityGateway/BeadsClient exec adapters`.

---

### Task 4: Config — bridge-mode fields

**Files:**
- Modify: the captain config struct — find with `grep -rn "HeartbeatMinutes" gobot/internal/infrastructure/config/` (same struct gains the new fields)
- Modify: `captain/config.yaml` (defaults documented)
- Test: extend the config package's existing test file (find with `ls gobot/internal/infrastructure/config/*_test.go`)

**Interfaces (produced):** on `config.CaptainConfig`:

```go
EngineMode            string `mapstructure:"engine_mode"`              // "legacy" | "bridge"; default "legacy"
CaptainAgent          string `mapstructure:"captain_agent"`            // default "captain"
AckTimeoutMinutes     int    `mapstructure:"ack_timeout_minutes"`      // default 10
EscalateAfterRenudges int    `mapstructure:"escalate_after_renudges"`  // default 3
AdmiralAlias          string `mapstructure:"admiral_alias"`            // default "human"
GCBin                 string `mapstructure:"gc_bin"`                   // default "gc"
BDBin                 string `mapstructure:"bd_bin"`                   // default "bd"
CityDir               string `mapstructure:"city_dir"`                 // default "../city"
```

- [ ] **Step 1: Failing test** asserting defaults load when yaml omits them (follow the config package's existing default-test pattern):

```go
func TestCaptainBridgeDefaults(t *testing.T) {
	cfg := loadMinimalCaptainConfig(t) // reuse the package's existing helper/pattern
	require.Equal(t, "legacy", cfg.Captain.EngineMode)
	require.Equal(t, 10, cfg.Captain.AckTimeoutMinutes)
	require.Equal(t, 3, cfg.Captain.EscalateAfterRenudges)
	require.Equal(t, "captain", cfg.Captain.CaptainAgent)
}
```

- [ ] **Step 2: FAIL → Step 3: add fields + defaults** (wherever the package sets `heartbeat_minutes` defaults today) **→ Step 4: PASS.** Add the fields, commented-out `engine_mode: bridge`, to `captain/config.yaml`'s captain section.
- [ ] **Step 5: Commit** `feat(captain): bridge-mode config fields (engine_mode, ack/escalation, gc wiring)`.

---

### Task 5: Bridge wake path (`wake.go`) — mail + nudge + re-nudge + escalate

**Files:**
- Create: `gobot/internal/captain/wake.go`, `gobot/internal/captain/wake_test.go`
- Modify: `gobot/internal/captain/supervisor.go` — in `Tick`, where legacy mode builds a prompt and calls `s.runner.Run` (read the section after the `sessionsInLastHour` check), branch: `if s.cfg.EngineMode == "bridge" { return s.bridgeWake(ctx, now, events) }`. Legacy path untouched.
- Modify: `gobot/cmd/captain/main.go` — construct `CityGateway`/`BeadsClient` from config, pass into `NewSupervisor` (new optional setter `SetCity(gw *CityGateway, bc *BeadsClient)` to avoid breaking the constructor's signature for legacy tests).

**Interfaces:**
- Consumes: Task 3 adapters, Task 4 config, existing `EventStore`.
- Produces: `(s *Supervisor) bridgeWake(ctx, now, events) (bool, error)`; internal state `renudges map[int64]int` (event id → count).

**Behavior to pin (write these as the tests):**
1. Events pending → ONE mail (subject `wake: N events`, body = `id  type  ship  age` lines + literal instruction footer `ack: spacetraders captain events ack --player-id <id> --ids <csv>`) + ONE nudge (`"wake: N events + heartbeat — check mail"`), rate-cap counted exactly like legacy sessions (`sessionStarts` reused).
2. Heartbeat-due with zero events → nudge only (`"heartbeat — no events"`), no mail.
3. Events still unprocessed after `AckTimeoutMinutes` → re-nudge (no duplicate mail), max `EscalateAfterRenudges` times per event.
4. Event exceeding max re-nudges → `SendMail(AdmiralAlias, "captain unresponsive", …)` once, then stop counting that event (escalated set).
5. `DISABLED` present → no mail, no nudge (already guarded at `Tick` top — add a test proving bridge path respects it).

- [ ] **Step 1: Failing tests** — fake gateway recording calls, fake clock via `now` params (the package already passes `now time.Time` everywhere):

```go
type fakeGateway struct{ mails, nudges [][]string }

func (f *fakeGateway) SendMail(_ context.Context, to, s, b string) error {
	f.mails = append(f.mails, []string{to, s, b}); return nil
}
func (f *fakeGateway) Nudge(_ context.Context, a, t string) error {
	f.nudges = append(f.nudges, []string{a, t}); return nil
}
// SessionAlive/SpawnSession as no-ops for this task

func TestBridgeWakeSendsMailAndNudgeForEvents(t *testing.T) { /* events=2 → 1 mail containing both ids, 1 nudge */ }
func TestBridgeHeartbeatNudgesWithoutMail(t *testing.T)      { /* events=0, heartbeat due → 0 mails, 1 nudge */ }
func TestBridgeRenudgesUnackedAfterTimeout(t *testing.T)     { /* same event unprocessed across ticks past AckTimeout → second nudge, still 1 mail */ }
func TestBridgeEscalatesToAdmiralAfterMaxRenudges(t *testing.T) { /* renudges=3 → mail to AdmiralAlias exactly once */ }
```

(The supervisor takes an interface, not the concrete `CityGateway` — define `type cityGateway interface { SendMail(...) error; Nudge(...) error; SessionAlive(...) (bool, error); SpawnSession(...) error }` in `wake.go`; `*CityGateway` satisfies it.)

- [ ] **Step 2: FAIL → Step 3: implement `bridgeWake`** — compose mail body from `events`, call in order (mail → nudge → mark wake time), maintain `renudges`/`escalated` maps keyed by event ID, prune both maps for IDs no longer unprocessed (acked events reset cleanly).
- [ ] **Step 4: PASS**; whole package green: `go test ./internal/captain/... -count=1` (legacy tests untouched).
- [ ] **Step 5: Commit** `feat(captain): bridge wake path — mail+nudge with re-nudge and Admiral escalation`.

---

### Task 6: Respawn + orphan requeue (`respawn.go`)

**Files:**
- Create: `gobot/internal/captain/respawn.go`, `gobot/internal/captain/respawn_test.go`
- Modify: `supervisor.go` — bridge mode only: call `s.ensureCaptainAlive(ctx)` at the top of each Tick (after `Disabled` check), and `s.requeueOrphanedPipelineBeads(ctx)` on the same cadence the legacy fixer recovery ran (startup + each tick is fine; it's cheap and idempotent).

**Behavior to pin:**
1. `SessionAlive("captain") == false` → `SpawnSession("captain", "captain")` once per tick, and a mail to Admiral only if respawn ALSO fails (spawn errors must not crash the tick).
2. `ListInProgressPipeline()` returns beads whose assignee session is dead (`SessionAlive(assignee)==false`) → `Reopen(id, "session died")`. Beads with a live assignee untouched.
3. Kill switch: `DISABLED` → no respawn (sessions stay down when you say down).

- [ ] **Step 1: Failing tests** (extend `fakeGateway` with scripted `SessionAlive` returns and a spawn recorder; fake `BeadsClient` via a small `beadsClient` interface mirroring Task 5's pattern).
- [ ] **Step 2: FAIL → 3: implement → 4: PASS → 5: Commit** `feat(captain): captain respawn + shipwright orphan-bead requeue`.

---

### Task 7: Migration command (`captain-migrate`) — files → beads

**Files:**
- Create: `gobot/cmd/captain-migrate/main.go`, `gobot/internal/captain/migrate.go`, `gobot/internal/captain/migrate_test.go`

**Interfaces:**
- Consumes: `BeadsClient.Exec` (all bead writes go through `bd` exec — testable with the recording fake).
- Produces: `Migrate(ctx context.Context, b *BeadsClient, stateDir, reportsDir string, apply bool) (MigrationReport, error)`; `MigrationReport struct{ Strategy, Decisions, Lessons, Backlog, Bugs int; Commands [][]string }`. Binary: `captain-migrate --state ../captain/state --reports ../captain/reports/bugs [--apply]` (dry-run default: prints the `bd` commands it WOULD run).

**Mapping (from spec, exact):**
| Source | bd command shape |
|---|---|
| `strategy.md` (whole file) | `bd create "Fleet strategy" -t design -l strategy --body-file -` (content via stdin) |
| `decisions.jsonl` (per line: `{id, decision/text, outcome?, ...}` — read one real line first and match its actual keys) | `bd create "<first 80 chars>" -t decision -l migrated` then `bd note <new-id> "outcome: <outcome>"` when outcome present. Closed if outcome exists: `bd close <new-id> --reason "historical"` |
| `lessons.md` + `lessons-archive.md` (split on `^- ` or `^## ` bullets — read the file, pick the actual delimiter) | `bd remember "<lesson text>"` per lesson |
| `improvement-backlog.md` + `friction.md` (per bullet) | `bd create "<bullet>" -t feature -l backlog -p 3` / `-l friction` |
| `reports/bugs/*.md` (frontmatter title/status/kind) | non-terminal statuses only (`new`, `in_progress`, `gate_failed`, `awaiting_human`): `bd create "<title>" -t bug -l shipwright --body-file <path>`; terminal (`merged`/closed/) skipped, counted |

- [ ] **Step 1: Failing tests** on `Migrate` with fixture dir (tmpdir with 2-line decisions.jsonl, a 2-bullet lessons.md, one `status: new` report, one `status: merged` report) + recording fake exec: assert command shapes and that `apply=false` executes NOTHING (`Commands` populated, zero Exec calls), `apply=true` executes each.
- [ ] **Step 2: FAIL → 3: implement → 4: PASS.**
- [ ] **Step 5: Dry-run against the real workspace:** `go run ./cmd/captain-migrate --state ../captain/state --reports ../captain/reports/bugs` → eyeball the printed plan (counts sane: ~1 strategy, tens of decisions/lessons). **Do NOT `--apply` yet** — that's the cutover checklist (Task 9), so migration runs once, immediately before the flip.
- [ ] **Step 6: Commit** `feat(captain): captain-migrate files→beads (dry-run default)`.

---

### Task 8: Captain agent template

**Files:**
- Create: `city/agents/captain/prompt.template.md`, `city/agents/captain/agent.toml`

**agent.toml:**
```toml
# provider default (claude); model pinned to fable via provider args if city.toml
# does not already default to it — verify with: acd prime captain (header shows command)
```
(If the city's provider default already runs `claude --effort max` on the session model, leave agent.toml empty except a comment; model selection for city agents follows city.toml — check `grep -A3 workspace city/city.toml`.)

**prompt.template.md — full content (condensed here to its required sections; write ALL of them):**

```markdown
# Captain

You are **{{ .AgentName }}**, captain of the TORWIND successor fleet — the standing
decision-maker of this SpaceTraders operation. Your session is long-lived and visible;
the Admiral may attach at any moment and read your reasoning as it happens.

## Chain of command
Admiral (human) sets mission and approves Tier-3 work. You command fleet operations.
The crew advises: shipwright (code), trade-analyst (markets), fleet-architect (fleet
composition). Harbormaster audits the port; its notes are advisory.

## Hard rules
1. You act ONLY through the `spacetraders` CLI and `bd`/`gc mail`. You NEVER edit code,
   templates, or config files — code belongs to the shipwright via beads.
2. Memory lives in beads (rig db). No state files. If it matters tomorrow, it is a
   bead note, a decision bead, or `bd remember` — before your turn ends.
3. Any single spend > 25% of treasury requires a "refute this plan" consult first
   (mail a specialist; record refutation on the decision bead).
4. Never start/stop system services. The kill switch `captain/DISABLED` is the
   Admiral's; if you see it, idle.

## Wake ritual (every nudge)
1. `gc mail check` — read event mail + crew/Admiral messages.
2. `spacetraders captain events list --player-id {{ .PlayerID }}` — live queue.
3. Assess: fleet status, treasury, containers (CLI).
4. Act: navigate/trade/contract/manufacture via CLI.
5. Record: `spacetraders captain events ack --player-id {{ .PlayerID }} --ids <csv>`;
   outcome notes on open decision beads (`bd note`); one wake-summary note; durable
   lessons via `bd remember`; strategy bead edit if posture changed.
6. Idle wake (no events, nothing anomalous): ack heartbeat, one-line note, groom one
   backlog bead (label: backlog), stop.

## Decision beads
Every non-trivial choice: `bd create "<decision>" -t decision`, link consults
(`bd dep add <decision> <consult> -t related`), close with outcome when observable.

## Consults
`bd create "<question>" -t task -l consult` with context in description; then
`gc mail send <specialist> ...` pointing at the bead; continue your wake — answers
arrive as mail-nudges. Never block waiting.

## Rollover
When context feels heavy or daily: write a handoff bead (`-t task -l handoff`:
posture, in-flight intentions, open consults, anomalies), then `gc handoff` yourself.
The watchkeeper respawns you; you re-prime from beads. Trust the ledger, not memory.

## Shipwright pipeline (you file, it builds)
- Bug found: `bd create -t bug -l shipwright` with failure signature/evidence.
- Small improvement: `-t feature -l shipwright` + acceptance criteria (`--acceptance`).
- Big feature (new package/schema/API-contract/cross-cutting/safety-rails): spec on
  the bead, then `bd human <id>` — the Admiral approves BEFORE code. Never skip this.
```

- [ ] **Step 1:** Write both files exactly as above (plus `{{ .PlayerID }}` — check the template variables acd actually provides with `acd prime harbormaster | head -30` and reuse only variables that render; hardcode player 1 if PlayerID isn't a template var).
- [ ] **Step 2: Smoke:** `acd prime captain | head -40` — renders without template errors, hard rules visible.
- [ ] **Step 3: Session smoke (no fleet needed):** `acd run captain` → session starts, primes, you see the wake ritual acknowledged; then detach (Ctrl-b d) and `acd session close captain` (don't leave it running pre-cutover).
- [ ] **Step 4: Commit** `git add city/agents/captain` ; `feat(city): captain agent template`.

---

### Task 9: Cutover — bridge default, legacy deleted

**Pre-flight checklist (manual, with Admiral present):** fleet services down (they are), DB up, city dolt server up, weekly usage pool sane.

**Files:**
- Modify: `captain/config.yaml` — `engine_mode: bridge`
- Delete: `gobot/internal/captain/session.go`, `snapshot.go`, `metareview.go` (+ their tests)
- Modify: `gobot/internal/captain/workspace.go` — keep only `Dir()`, `Disabled()`, `DisabledFixes()` (grep callers of every deleted method first; legacy-only callers die with legacy)
- Modify: `gobot/internal/captain/supervisor.go` — remove the legacy branch + `runner` field; `EngineMode` still read (unknown values → error at startup, not silent default)
- Modify: `gobot/cmd/captain/main.go` — drop `NewClaudeRunner` wiring; keep fixer wiring UNTIL Task 10 (legacy fixer still drives pipeline; it doesn't use ClaudeRunner for strategy, it has its own factory — verify with `grep -n fixerFactory gobot/cmd/captain/main.go` and keep exactly that path compiling)

- [ ] **Step 1: Run the migration for real:** `go run ./cmd/captain-migrate --state ../captain/state --reports ../captain/reports/bugs --apply` → record `MigrationReport` counts; verify spot-checks: `bd list -l strategy`, `bd list -t decision | head`, `bd memories fleet | head` (from repo root = rig db).
- [ ] **Step 2: Archive files:** `git rm -r --cached` nothing — instead `git mv captain/state captain/state.pre-bridge` and commit the rename (history preserved, files inert). Watchkeeper/dashboard code no longer reads them after this task + Task 12.
- [ ] **Step 3: Delete legacy code** per Files list; fix compilation; delete tests of deleted units; `go test ./internal/captain/... -count=1` green.
- [ ] **Step 4: Live smoke:** `go run ./cmd/captain --once` with `engine_mode: bridge`, `DISABLED` REMOVED for the smoke, captain session pre-started (`acd run captain`, detached): expect mail in captain's inbox + nudge visible in `acd session peek captain`, captain acks, `captain events list` empties. Then re-create `DISABLED` (fleet stays down until Admiral orders).
- [ ] **Step 5: Commit** `feat(captain)!: cutover to city-bridge engine; delete claude -p runner`.

---

### Task 10: Shipwright — pipeline off files, onto beads

**Files:**
- Create: `city/agents/shipwright/prompt.template.md`, `city/agents/shipwright/agent.toml` (`provider = "claude"` + model opus per the city's provider-args convention — same verification as Task 8)
- Modify: `gobot/internal/captain/fixer.go` — delete the driver (`ProcessOne`, report-file scanning, `SetReportStatus`, statuses, `RecoverOrphanedFixes`) keeping `RunGate`, `SquashMerge`, stale-check, worktree provisioning helpers (`worktree.go` stays — shipwright uses `git worktree` itself, but gate needs the proto-provisioning helper: check what `fixer.go:137-146` provisions and keep that exported as `ProvisionWorktree(dir) error`)
- Modify: `gobot/cmd/captain/main.go` — drop fixer wiring entirely
- Modify: `gobot/cmd/captain-gate/main.go` — add `--provision` flag calling `ProvisionWorktree` before the gate (so the shipwright needs no Go knowledge)

**Shipwright template — required sections (write in full):** identity (builds AND repairs; nautical register); queue = `bd ready --type bug,feature -l shipwright` in rig db; claim (`bd update <id> --claim --status in_progress`); tier rules verbatim from spec (Tier 1 bug/failure-signature → TDD → gate; Tier 2 feature+acceptance-criteria-present → TDD against criteria; Tier 3 = `bd human` approved marker required BEFORE work — if criteria missing on Tier 2 or approval missing on Tier 3, mail captain and release the bead); worktree discipline (`git worktree add ../captain-worktrees/<bead-id> origin/main`, TDD, `captain-gate --repo <rig> --worktree <wt> --branch <branch> --message "<msg>" --provision --merge`); on gate pass → `bd close <id> --reason "merged <sha>"` + note gate JSON; on fail → note gate log, status back to open, mail captain; NEVER `git merge/push` by hand; NEVER touch watchkeeper/gate/templates (Tier-3 rails — mail the Admiral instead); rate limits honored via config caps mirrored as instructions (max 3 fixes + 2 features/day — read current caps from `captain/config.yaml` lines `max_fixes_per_day`/`max_features_per_day`).

- [ ] **Step 1:** Template + toml written; `acd prime shipwright` renders.
- [ ] **Step 2:** Go deletions; `go build ./... && go test ./internal/captain/... -count=1` green (gate tests from Task 2 still pass — they never touched the driver).
- [ ] **Step 3: End-to-end smoke with a synthetic bead:** create `bd create "smoke: add a failing-then-passing unit test to pkg/utils" -t bug -l shipwright`; `acd run shipwright` (attached); watch it claim, worktree, commit, run captain-gate, merge, close bead. This is the moment the new pipeline proves itself — budget one attended run.
- [ ] **Step 4: Commit** `feat(engine): shipwright agent replaces file-based fixer driver`.

---

### Task 11: Trade Analyst + Fleet Architect templates

**Files:**
- Create: `city/agents/trade-analyst/prompt.template.md` + `agent.toml`, `city/agents/fleet-architect/prompt.template.md` + `agent.toml`

**Both templates — required sections:** identity + scope (analyst: markets/manufacturing margins/opportunity ranking; architect: fleet composition/purchase timing/shipyard specs); READ-ONLY actuators (CLI queries + `bd` + mail only — they never navigate/trade/purchase); consult protocol (mail arrives pointing at a consult bead → read bead + description → investigate via CLI market/shipyard queries → answer as `bd note` on the bead, structured: recommendation / evidence / confidence / what-would-change-my-mind → `bd update <id> --status closed`? NO — captain closes; specialist notes then mails captain "answered <bead-id>" AND nudges the captain's session directly — `gc session nudge captain "consult answered: <bead-id>"` — so answers wake the captain immediately instead of waiting for the next heartbeat, satisfying the spec's mail-arrival-nudge requirement); adversarial mode (when the mail says "refute": argue AGAINST the plan with evidence; a refutation that fails honestly strengthens the decision); rollover same as captain (handoff bead, `gc handoff`); idle = truly idle (no self-directed spending of tokens; they act only on mail).

- [ ] **Step 1:** Write all four files. **Step 2:** `acd prime trade-analyst && acd prime fleet-architect` render clean. **Step 3:** Consult smoke: create a consult bead, mail the analyst, `acd run trade-analyst`, watch it answer on the bead (fleet down: it answers from DB market data or says data is stale — honesty instruction covers it). **Step 4: Commit** `feat(city): trade-analyst + fleet-architect crew templates`.

---

### Task 12: Dashboard repoint + retire + docs

**Files:**
- Modify: `dashboard/captain_dashboard.py` — `collect()` reads that die: `captain-log.md` heads/last-entry (`:50-52,79-81,86`), `decisions.jsonl` (`:53-57,82`), `reports/bugs` scan (`:58-72,86`). Replace: log panel → last 60 lines of `subprocess` `gc session logs captain --tail 60` (title "Captain session (live)"); decisions count → `bd list -t decision --status open --json | jq length` equivalent via subprocess with the same 4s cache; reports table → `bd list -l shipwright --json` mapped to the same row dict shape (`name,status,kind,closed`) so the JS needs zero changes except the modal endpoint, which switches from file-read to `bd show <id>` text.
- Modify: `captain/CLAUDE.md`, `captain/README.md`, `captain/AUTOMATION_GUIDE.md` — one banner line each at top: `> ENGINE MOVED: this workspace is legacy. The captain is a city agent (acd run captain); state lives in beads (sp- db). See docs/superpowers/specs/2026-07-06-ai-engine-city-bridge-design.md.` (Full rewrite is not this plan's job.)
- Test: none automated for the py dashboard (it has no test rig) — manual: run it, all panels render, no tracebacks with fleet down.

- [ ] **Step 1:** Dashboard edits (keep the stdlib-only constraint — subprocess is already used for psql). **Step 2:** `python3 dashboard/captain_dashboard.py` + open `:8899` — panels render, SIGNAL-LOST-style empties where daemon is down are fine, no traceback. **Step 3:** Doc banners. **Step 4: Commit** `chore(engine): dashboard reads sessions+beads; legacy workspace docs point to bridge`.

---

## Execution notes

- Order is dependency order; Tasks 1-4 are independent of each other and parallelizable (disjoint files); 5-6 need 3+4; 7 needs 3; 8 anytime; 9 needs ALL of 1-8; 10-12 after 9.
- Task 9 (cutover) and Task 10 Step 3 (attended shipwright smoke) want the Admiral present.
- `DISABLED` stays in place throughout except the two smoke moments; the fleet does not start as part of this plan — bring-up remains `st-wm7`, gated on the Admiral.
- Rollback at any point pre-Task-9: set `engine_mode: legacy` (nothing legacy is deleted before 9). Post-9 rollback = git revert of the cutover commit (state files still exist as `captain/state.pre-bridge`).
