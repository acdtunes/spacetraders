# Autonomous Captain — Design

**Date:** 2026-07-02
**Status:** Draft — awaiting user review

## Goal

Replace the human "Admiral" in the fleet-command loop with an autonomous LLM strategist
("the captain") driven by `claude -p`, so the SpaceTraders bot runs 24/7 unattended and
plays the game better over time. The Go daemon keeps all tactical execution; the captain
owns strategy, health/recovery, and gated self-improvement of the bot code.

Decisions made during brainstorming:

| Question | Decision |
|---|---|
| Efficiency dimensions | In-game performance (credits/hour, utilization) + autonomy (no human) |
| Trigger model | Hybrid: strategic events + periodic heartbeat |
| Captain scope | Strategy & allocation, health & recovery, code self-improvement |
| Code-change guardrails | Branch + build/tests gate + auto-merge + daemon restart |
| Foundation | Fresh `captain/` project targeting gobot; old `claude-captain/` kept as prompt raw material only |
| Event delivery | Postgres outbox table + polling supervisor (durable, auditable) |
| LLM runtime | `claude -p --model opus` via the local Claude Code CLI, authenticated to the user's Max subscription (no API key) |
| Actuation | gobot `spacetraders` CLI via Bash (not MCP) — richer surface, self-documenting, permission-gated |

## Architecture

```
gobot daemon
  ShipEventBus / workflow lifecycle / container heartbeats
        │
        ▼  CaptainEventPublisher (new, in gobot)
  Postgres: captain_events (outbox)
        │
        ▼  captain supervisor (new binary: gobot/cmd/captain)
  poll 30s · heartbeat timer · debounce · budget guards · kill switch
        │
        ▼  claude -p session (workspace: captain/ at repo root)
  prompt = composed fleet snapshot + pending events + memory tail
  acts via the gobot CLI (Bash) · updates captain-log.md / strategy.md
```

### Component 1: CaptainEventPublisher (gobot)

- Subscribes to the existing in-memory `ShipEventBus` and hooks workflow/container
  lifecycle points.
- Filters to **strategic** events only; tactical noise (individual navigation steps)
  never reaches the outbox. Initial event set:
  - `contract.completed`, `contract.failed`
  - `container.crashed`, `container.heartbeat_lost`
  - `workflow.finished`, `workflow.failed`
  - `ship.idle` (idle beyond configurable threshold)
  - `credits.threshold` (crossing configured bands, up or down)
- Writes to a new table via migration:
  `captain_events(id, type, ship, payload jsonb, created_at, processed_at NULL)`.

### Component 2: Captain supervisor (gobot/cmd/captain)

A small Go binary containing **zero strategy** — pure plumbing:

- Polls `captain_events` every 30s.
- Triggers a session when unprocessed events exist OR the heartbeat interval
  (default 45 min) has elapsed since the last session.
- Composes the session prompt (see below), runs `claude -p` in the `captain/`
  workspace, and on session success marks the batch `processed_at = now()`.
- Guards: one session at a time; max sessions/hour (default 6); session wall-clock
  timeout (10 min strategy / 30 min fix); kill switch files `captain/DISABLED`
  (everything) and `captain/DISABLED_FIXES` (fix pipeline only).
- Watches for `fix_requested` markers and drives the self-improvement pipeline.

### LLM runtime: claude -p on a Max subscription (Opus)

- Sessions are invoked as `claude -p --model opus` using the locally installed
  Claude Code CLI, authenticated to the user's **Max subscription** — the supervisor
  must run as that user and must **not** set `ANTHROPIC_API_KEY` (an API key would
  silently switch billing away from the subscription).
- Max plans have rolling usage windows, so the effective budget is quota, not
  dollars. The session caps above double as quota protection, and the supervisor
  must recognize usage-limit responses from the CLI (non-zero exit / limit message):
  back off, leave events unprocessed, and resume when the window resets — a
  limit-hit is a normal state, not an error.
- Strategy and fix sessions both use Opus by default; model is a config value so
  it can be changed without code.

### Component 3: Captain session (captain/ workspace)

New top-level `captain/` directory:

```
captain/
├── CLAUDE.md            # persona, decision rules, playbooks, escalation rules
├── CLI_REFERENCE.md     # generated gobot CLI command reference (see Tool discovery)
├── .claude/settings.json# pre-approved Bash permissions for the spacetraders CLI
├── state/
│   ├── captain-log.md   # append-only decision journal (supervisor trims to ~100 entries)
│   └── strategy.md      # standing strategy, maintained by the captain itself
└── reports/
    └── bugs/            # structured bug reports that feed the fix pipeline
```

**Composed prompt** (built by the supervisor, so the captain decides instead of
fetching): credits + delta since last session; ships with status/assignment/idle time;
active containers + health; KPIs from the ledger (credits/hour overall and per
workflow type); pending event batch; tails of captain-log.md and strategy.md.

**Memory model:** files, not `--resume`. Every session must end by appending its
decisions + rationale to `captain-log.md`. `strategy.md` holds the current strategy
and KPI targets; heartbeat sessions are explicitly prompted to compare actual KPIs
against those targets and revise the strategy when reality disagrees. This is the
in-game-performance feedback loop (ports TARS's fleet-manager/feature-proposer ideas
into one KPI-grounded loop).

### Tool discovery: how the captain knows what it can do

The captain acts by running the `spacetraders` CLI through Bash (the CLI talks to
the daemon over its socket). It learns the available commands three ways:

1. **`captain/CLI_REFERENCE.md`** — a command reference generated from the binary's
   own `--help` tree (`spacetraders --help` recursively, via a make target the
   supervisor runs at startup). `captain/CLAUDE.md` imports it, so every session
   starts with the full, current command surface in context. Because it is
   generated from the binary, it cannot drift from what is actually installed.
2. **Self-documenting fallback** — for flags or subcommands not memorized, the
   captain runs `spacetraders <cmd> --help` in-session; cobra help output is
   designed for exactly this.
3. **The Bash permission allowlist** (`captain/.claude/settings.json`) is the
   enforcement boundary: it defines which commands the captain *may* run
   (read-only commands in rollout phase 1; mutating commands added in phase 2).
   CLAUDE.md playbooks say *when* to use a command; the allowlist decides
   *whether* it runs at all.

## Health & recovery

Crash / heartbeat-loss / stuck-workflow events reach the captain within ~30s.
Playbook (encoded in captain CLAUDE.md):

1. Inspect via the CLI (`spacetraders container list/inspect/logs`, `spacetraders health`).
2. Corrective action via the CLI (restart workflow, reassign ship, refuel, stop zombie
   container).
3. Record the incident in the captain's log.
4. **Escalation:** same failure signature 3+ times → stop retrying, write a
   structured bug report to `captain/reports/bugs/`, emit `fix_requested`.

## Self-improvement pipeline (gated)

Separate from strategy sessions. Caps: 1 concurrent, N/day (default 3).

1. Supervisor sees `fix_requested` → creates a git worktree of gobot on branch
   `captain/fix-<slug>`.
2. Dedicated `claude -p` fix session in the worktree. Prompt = bug report.
   Rules: TDD (failing test first), minimal diff, no migrations, no changes outside
   `gobot/`.
3. **Gate run by the supervisor** (never trusted from session output):
   `go build ./... && go test ./...` (+ lint if configured).
4. Pass → squash-merge to main, rebuild daemon binary, restart daemon, log the
   deploy. Fail → branch + report left for the human; captain resumes strategy in
   "known bug, workaround mode".

Audit trail: git history + `captain/reports/` + captain-log.md.

## Safety & failure isolation

- Events are marked processed only on session success; a crashed session means the
  next tick retries with the same batch. Nothing is dropped.
- Budget caps make the worst-case cost bounded and predictable; when caps are hit,
  events queue.
- The daemon never blocks on the captain. Captain downtime degrades strategy
  freshness only; the game loop keeps running.
- Kill switches are plain files — easy to flip over SSH.

## Testing

Follow gobot conventions (testify unit tests + godog BDD):

- **CaptainEventPublisher:** which bus/lifecycle events produce outbox rows; payload
  shape; filtering of tactical noise.
- **Supervisor:** trigger conditions (events vs heartbeat), debounce/batching, caps,
  kill switches, processed-only-on-success semantics — with a stubbed session runner.
- **Prompt composer:** snapshot correctness against seeded DB fixtures.
- **Fix pipeline:** end-to-end against a scratch git repo (gate pass → merge; gate
  fail → branch preserved).
- **Prompt quality:** dry-run `claude -p` sessions reviewed by hand before enabling
  auto-merge; auto-merge ships disabled by default until validated.

## Out of scope

- Real-time (sub-30s) reaction — unnecessary at strategy timescale.
- Migrating or deleting the old `claude-captain/` and Python `bot/` projects.
- Multi-agent captain hierarchies (specialist subagents can be added later inside
  the captain workspace if single sessions prove insufficient).
- LLM token-cost optimization beyond the budget caps (explicitly not a goal per
  brainstorming).

## Rollout

1. Ship publisher + outbox + supervisor with sessions in **advisory mode** (captain
   writes decisions to the log but mutating CLI commands are not in the Bash
   allowlist — read-only commands only) — validate prompt quality.
2. Allow mutating CLI commands (strategy + recovery go live).
3. Enable fix pipeline with auto-merge off (propose-only), then flip auto-merge on
   after a few good fixes.
