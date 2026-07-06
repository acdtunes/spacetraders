# AI Engine Redesign: The City Bridge — Design

**Date:** 2026-07-06
**Status:** Approved (Admiral, via brainstorm)
**Scope:** Replaces everything `claude -p`-shaped in the fleet's AI engine — the captain's
decision brain, the wake/event plumbing, and the self-improvement pipeline. The Go
execution layer (daemon, coordinators, ship containers — "the hands") is untouched.

## Purpose

The current engine runs the captain as invisible one-shot `claude -p` sessions whose
memory is markdown files (`captain/state/*.md`, `decisions.jsonl`) rebuilt into a prompt
each wake by a Go supervisor. Requirements for the redesign, set by the Admiral:

1. **Beads instead of files** — all captain state/memory lives in the beads database.
2. **No more `claude -p`** — proper acd city agents whose sessions are visible and
   attachable as they unroll (`acd session list` / `attach` / `peek` / `logs`).
3. **A crew** — specialist city agents help the captain decide.

Decisions made during brainstorm:

| Decision | Choice |
|---|---|
| Scope | Brain + wake plumbing + Fixer. Hands stay. |
| Captain lifecycle | **Standing session + rollover** (not per-wake spawns, not always-on) |
| Specialists | **Full standing crew** — all persistent, mail-driven, visible |
| Architecture | **A: City-native bridge** — Go slims to a Watchkeeper; cognition moves to city agents |
| Feature work | First-class: Fixer → **Shipwright**, three-tier bug/improvement/feature pipeline |

## Components

### Watchkeeper (Go, slimmed from `captainsup`)

Keeps: DB detectors (idle ships, stale heartbeats, credit crossings — `RunDetectors`),
nudge rate-limiting (`MaxSessionsPerHour` semantics → max nudges/hour), usage-limit
backoff, `captain/DISABLED` kill switch, `config.yaml`.

Drops: prompt building (`snapshot.go`), `ClaudeRunner`, file-workspace management
(`workspace.go`), meta-review (`metareview.go` — replaced by captain bd rituals).

New behavior on events-pending or heartbeat-due:
1. Send the captain **mail**: compact event batch with event IDs.
2. **Nudge** the standing session (tmux inject — the same bracketed-paste mechanism
   `acd run` uses to prime).

Event semantics are **at-least-once**: the captain acks event IDs via CLI during its
wake ritual; the Watchkeeper re-nudges unacked events after a timeout, and after N
failed re-nudges (default 3) escalates by mail to the Admiral. A captain session found
dead at tick is respawned (see Rollover).

### The Captain (standing city agent — model: Fable)

- Template: `city/agents/captain/prompt.template.md`; session created/attached via
  `acd run captain`; visible in `acd session list`.
- Primed from `gc prime` + `bd prime` + mail inject. No prompt-builder in Go.
- Acts through the existing `spacetraders` CLI. **Never edits code** — code belongs to
  the Shipwright.
- Memory is beads only (see State). Its transcript replaces the captain-log.

### The Crew (standing city agents, mail-addressable, visible)

| Agent | Model | Role |
|---|---|---|
| **Shipwright** (was Fixer) | Opus | Owns the code pipeline: bugs AND features, worktree + TDD, invokes the unchanged Go gate |
| **Trade Analyst** | Opus | Market/manufacturing opportunity analysis on demand |
| **Fleet Architect** | Opus | Fleet composition, ship purchase timing/specs |

No standing Risk Officer: for big irreversible spends the captain runs a mandatory
adversarial "refute this plan" consult (see Consult protocol) with the domain specialist.

Idle sessions cost nothing; tokens burn only when an agent is prompted.

## State: every file becomes a bead

All fleet-ops beads live in the **rig database** (`sp-` prefix; the captain works from
the rig, `bd` resolves naturally). The **city database** (`st-`) remains port
coordination (harbormaster/Admiral ledger). Mail crosses freely.

| Today (files in `captain/state/`) | New engine (beads, rig db) |
|---|---|
| `strategy.md` | One living `design` bead, updated in place; history in dolt |
| `decisions.jsonl` | `decision` beads with outcome notes, linked to the consult beads that informed them |
| `lessons.md`, `lessons-archive.md` | `bd remember` memories — searchable, auto-primed each session |
| `friction.md`, `improvement-backlog.md` | `task`/`feature` beads with labels; groomed by the captain during idle wakes |
| `reports/bugs/*.md` | `bug` beads (Shipwright queue); gate output as bead notes (replaces `.gate.log`) |
| `captain-log.md` (+ archive) | **Retired.** The session transcript is the log (live, attachable); plus one summary note per wake on the wake's event bead |

Stays Go-side, untouched: `config.yaml`, `DISABLED` kill-switch file, `captain/bin`.

## Protocols

### Wake ritual
Nudge → read mail (events + crew/Admiral messages) → query live state via CLI →
decide → act via CLI → record: ack event IDs; outcome notes on open `decision` beads;
one-note wake summary; `bd remember` durable lessons; strategy-bead edit if posture
shifted. Idle wakes are deliberately cheap: ack heartbeat, one-line note, done.
Heartbeat cadence and nudges/hour cap are Watchkeeper config (defaults carry over:
45 min, current hourly cap).

### Consult protocol
The captain mails a specialist a **question bead ID** (question, context, deadline on
the bead; mail is pointer + nudge). The specialist answers as bead notes, mails back.
Advice never binds; the captain's `decision` bead links the consult bead. Consults are
async — specialist replies nudge the captain on arrival (mail-arrival nudge via the
same Watchkeeper mechanism), so answers do not wait for the next heartbeat.

**Mandatory adversarial pass:** any single spend exceeding a configured fraction of
treasury (default 25%) requires a "try to refute this plan" consult before execution;
the refutation lands on the decision bead.

### Rollover ritual
Trigger: context-pressure self-check each wake, or daily mark. The captain writes a
**handoff bead** (posture, in-flight intentions, open consults, active anomalies),
closes its session; the Watchkeeper notices the dead session at next tick and spawns a
fresh one, primed from `bd prime` + handoff bead + memories. Nothing lives only in
context. Crew sessions roll over the same way, far less often.

### Shipwright pipeline — three tiers

One agent, one worktree + TDD discipline, one unchanged Go gate (build + full tests +
stale-base guard + merge). Tiers differ in approval geometry:

- **Tier 1 — bugs** (`bug` bead with failure signature): today's proven flow. TDD
  against the failure; gate green → **auto-merge**.
- **Tier 2 — bounded improvements** (`feature` bead, small scope): the captain writes
  **acceptance criteria on the bead first** (`bd --acceptance`); Shipwright implements
  TDD against them; gate green → **auto-merge**. Audit trail = decision bead + criteria.
- **Tier 3 — big features** (new package, DB schema/migration, API-contract change,
  cross-cutting): captain files the bead, gathers consults into a short spec on the
  bead, flags `bd human` — **the Admiral approves the spec before any code is written**.
  After approval: same pipeline, gate green → auto-merge.

**Self-modification rule (non-negotiable):** the engine may never autonomously modify
its own safety rails — the gate binary, the Watchkeeper, kill-switch handling, and the
agent templates are Tier 3 by definition, always requiring Admiral sign-off.

Orphan recovery carries over: a `bug`/`feature` bead stuck `in_progress` with a dead
Shipwright session is re-queued by the Watchkeeper's stale check (same at-least-once
philosophy as events; same semantics `RecoverOrphanedFixes` provides today).

Feature intake: captain friction, specialist observations, harbormaster audits, and
Admiral mail all land as `feature` beads in the rig db.

## Migration order (each step shippable; fleet startable throughout)

1. **Beads first** — one-time migration script moves state files → rig-db beads;
   captain CLI learns to read/write beads; old files archived (git history), not deleted.
2. **Captain agent** — template + acd wiring; Watchkeeper grows mail+nudge alongside
   `claude -p` behind a feature flag.
3. **Cutover** — flip the flag; delete `ClaudeRunner`, snapshot/prompt-builder; what
   remains of `captainsup` is the Watchkeeper.
4. **Crew** — Shipwright migrates the fix pipeline off `claude -p` (gate untouched),
   gains the tiered feature flow; then Trade Analyst + Fleet Architect templates.
5. **Retire** — `captain/state/*` archived; `captain/` keeps `bin`, `config.yaml`,
   `DISABLED`.

## Failure honesty & testing

- Watchkeeper: existing unit tests for detectors/backoff/caps carry over; nudge
  delivery gets an integration test against a mock tmux target.
- Bead rituals: the ack/note/remember CLI seams are Go — unit-tested normally.
- Silent-captain risk: unacked-event re-nudge + escalation mail after N failures.
- Kill switch unchanged: `DISABLED` stops all nudging; standing sessions idle harmlessly.
- Gate remains the merge safety boundary for ALL Shipwright tiers.

## Out of scope

The Go daemon/coordinator/container layer ("hands"); the dashboard and visualizer
(they gain nothing/lose nothing here — the visualizer plan `st-g7j` is independent);
multi-captain / multi-agent-per-universe play; changing game strategy itself.

## Assumptions to verify at planning

1. The tmux-inject (bracketed-paste) mechanism `acd run` uses is invocable headlessly
   by a Go process for nudges (or `gc` exposes an equivalent — e.g. a nudge/paste
   subcommand); mail-arrival nudges piggyback the same path.
2. `bd` CLI performance is fine at wake frequency (dozens of reads/writes per wake).
3. The rig beads db (`sp-`) is initialized and usable from the rig root (it is — `acd
   rig list` shows it initialized).
4. Session-death detection (for rollover respawn and orphan re-queue) is observable to
   the Watchkeeper via `gc session`/bead state without attaching.
