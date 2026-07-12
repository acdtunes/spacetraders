# Universe Reset Runbook

Source of truth: `docs/superpowers/specs/2026-07-06-cross-universe-archive-reset-design.md`
(rev 2 — in-place, player-partitioned history; no archive schema copy).

One ordered, idempotent sequence. Each phase names its actor, exact copy-paste
commands, and its reversal. **The whole runbook is Admiral-triggered; nothing
in it ever runs autonomously.** Phases marked **ADMIRAL ONLY** must not be run
by any agent, script, or automation — ever.

Placeholders: `<era>` = lowercase agent symbol (e.g. `torwind`); `<AGENT>` =
agent symbol (e.g. `TORWIND`); `<NEW_AGENT>` = the incoming era's agent symbol;
`<jwt>` = the incoming agent's API token; `<ts>` = timestamp; `<reset-date>` =
server `resetDate` (YYYY-MM-DD); `<db-conn>` = the live `spacetraders` Postgres
connection string; `<rig>` = the beads rig repo root.

> **Superseding verb (sp-nax3):** the era flip + new-agent adoption + player
> repoints + prior-era container drain are now performed by ONE idempotent,
> guarded command — `spacetraders universe transition --agent <NEW_AGENT>
> --token <jwt> --confirm`. It API-validates the token BEFORE any write
> (fail-closed), does **not** truncate the player-partitioned caches, and
> repoints both the CLI default and `captain.player_id`. It fuses Phases 2 + 6
> (and the container drain); the phase-by-phase breakdown below remains the
> reference decomposition and manual-recovery path. Always `--dry-run` first.

---

## Phase 0 — Freeze

**Actor: Watchkeeper (auto-detect) + Admiral (confirm)**

Confirm the reset, then halt the fleet before anything destructive is
reachable.

```bash
spacetraders universe status
# exit code / output signals MISMATCH: server resetDate != open era's
# recorded universe_reset_date
```

If MISMATCH: the Watchkeeper has already touched `captain/DISABLED`
(fail-safe halt) and mailed the Admiral. Admiral confirms fleet services are
down before proceeding.

**Reversal:** Read-only + a flag file (`captain/DISABLED`). Clearing it is
Phase 8, Admiral-only.

---

## Phase 1 — Safety snapshot

**Actor: Admiral (documented command)**

Full `pg_dump` of the live db, before anything destructive runs. This is the
hard ordering invariant of the whole design.

```bash
pg_dump "<db-conn>" -F c -f "archives/pg/<era>-final-<ts>.dump"

# sanity check the dump is restorable
pg_restore --list "archives/pg/<era>-final-<ts>.dump"
```

**Reversal:** Re-runnable; this dump is the master rollback for Phase 2 and
the full recovery path for the unscoped-write disaster case.

---

## Phase 2 — Era-flip preview (Postgres)

**ADMIRAL ONLY**

Preview the era flip — it mutates nothing — to confirm the plan before the
beads phases:

```bash
spacetraders universe transition --agent <NEW_AGENT> --token <jwt> --dry-run
```

The actual flip is applied atomically in **Phase 6** (`--confirm`), which
stamps `eras.closed_at` + `final_credits` (L28 anchor method) on the outgoing
era and opens the incoming one.

> **Correction (Admiral, 2026-07-12):** `market_data` and `system_graphs` are
> **NOT** truncated. History is player-partitioned by `player_id`
> (migration 032 — the agent symbol is intentionally non-unique across eras),
> so prior-era data coexists via a distinct `player_id` and stays queryable via
> `history <verb> --era <id>`. The legacy `spacetraders universe close` verb
> still `TRUNCATE`s `market_data, system_graphs` — that truncation is a known
> defect and the verb **must not** be used for a player-partitioned reset. Use
> `universe transition` (above), which never truncates. (`universe scrub` only
> deletes player-scoped WIPE-class rows for a dead era's player; it does not
> touch the caches and is unaffected.)

**Reversal:** `--dry-run` is read-only. The Phase 1 dump is the master rollback
for the Phase 6 apply.

---

## Phase 3 — Beads era-close

**Actor: Harbormaster (dry-run first)**

Date-window label sweep + `era:<era>` labeling + bulk-close of open scoped
beads + strategy-bead demotion, over `decision`/`consult`/`handoff` beads.

```bash
# dry-run: prints the planned bd commands + the phase-4 memory proposal
# table; executes nothing
era-close --era <era> --agent <AGENT> --reset-date <reset-date> \
  --window-start <era-start-date> --window-end <reset-date> \
  --bd bd --rig "<rig>"

# review the printed plan, then apply: executes ONLY the label/close/demote
# commands — never a memory action
era-close --era <era> --agent <AGENT> --reset-date <reset-date> \
  --window-start <era-start-date> --window-end <reset-date> \
  --bd bd --rig "<rig>" --apply
```

Equivalent manual `bd` sequence the tool plans (for reference / manual
recovery):

```bash
bd list --json
bd label add <id1> <id2> ... era:<era>
bd close <id1> <id2> ... --reason "era <era> ended (universe reset <reset-date>)"
bd close <strategy-bead-id> --reason "demoted to retrospective input"
```

**Reversal:** Dolt-versioned; label/close are non-destructive (beads remain
queryable via `bd list -l era:<era> --status closed`).

---

## Phase 4 — Memory-review gate

**Actor: Harbormaster proposes, ADMIRAL APPROVES**

The `era-close` dry-run above already printed the MEMORY PROPOSAL table
(KEY / ACTION / REASON, KEEP / REWRITE / RETIRE per spec §4.2). No
memory action executes automatically — ever. The classification table lands
as a note on the era-close checklist bead for Admiral review.

After Admiral approval, apply manually, one memory at a time:

```bash
# REWRITE: strip the universe-specific instance, keep the general rule
bd remember --key <key> "<rewritten universal text>"

# RETIRE: preserve the original text on the retro bead FIRST, then forget
bd note <retro-bead-id> "retired memory <key>: <original text>"
bd forget <key>
```

**Reversal:** RETIRE text is pre-copied to the retro bead before `bd forget`
runs; dolt history retains the memory-table edits themselves.

---

## Phase 5 — Retrospective

**Actor: Admiral + Harbormaster (later: outgoing captain drafts)**

```bash
spacetraders history summary --era <era>

bd create "retro: era <era>" -t design -l "era:<era>,retrospective" \
  --body-file <retro-draft.md>
```

Composed from: `history summary` output, the closed strategy bead's final
posture, decision-bead highlights, and the RETIRE-class memories preserved
verbatim as notes (Phase 4).

**Reversal:** Additive.

---

## Phase 6 — Transition to the new agent

**ADMIRAL ONLY**

One idempotent command performs the whole rollover (sp-nax3). Preview, then
apply:

```bash
spacetraders universe transition --agent <NEW_AGENT> --token <jwt> --dry-run
spacetraders universe transition --agent <NEW_AGENT> --token <jwt> --confirm
```

In order, on `--confirm`, it:

1. **API-validates `<jwt>` via `GetAgent` BEFORE any write** — a corrupt token
   is rejected with zero partial state (root-cause fix: `player register` never
   validated, so a transcription-corrupted JWT was silently stored this era).
2. **Flips the era table without truncation** — stamps `closed_at` +
   `final_credits` on the outgoing era and opens a new `eras` row for the server
   `resetDate` linked to a fresh `player_id` (`market_data` / `system_graphs`
   retained; see Phase 2 correction).
3. **Repoints BOTH** the CLI default player (`config set-player`) **AND**
   `captain.player_id` in `gobot/config.yaml` (comment-preserving edit) — so the
   supervisor does not wake as the dead prior-era player. **Closes sp-m602.**
4. **Drains the prior era's containers coordinators-first** (coordinators run
   `iterations=-1` reconcile loops that relaunch workers; `restart_policy=on-failure`
   makes an explicit stop terminal), reconciling any daemon-unknown orphan rows
   to `STOPPED` in the DB.

Re-running once `universe status` is "in sync" is a no-op.

> The daemon's in-memory "Active Containers" gauge may stay high after the drain
> until an Admiral daemon restart (Phase 8 bring-up) clears it. Verify the drain
> against DB truth (`spacetraders container list`), not the gauge.

**Reversal:** API-side registration is a one-shot; local rows are re-creatable,
and the Phase 1 dump restores the pre-transition state.

---

## Phase 7 — Seed fresh strategy

**Actor: Harbormaster drafts, Admiral blesses**

```bash
bd create "Fleet strategy: era <new-era>" -t design -l "strategy,era:<new-era>" \
  --body-file <strategy-seed.md>
```

Seeded with the universal KPI skeleton, pointers to all prior retro beads,
and a "priors to test early" section distilled from `history summary` /
`history goods`.

**Reversal:** Additive.

---

## Phase 8 — Bring-up

**ADMIRAL ONLY** (final step)

Dashboards repointed, smoke checks run, then:

```bash
rm captain/DISABLED
```

**The kill switch is never cleared by any automation, ever.** No verb,
agent, or runbook phase in this design clears it except this manual step.

**Reversal:** N/A — this is the terminal, confirming step.

---

## Ordering rationale

The era flip is applied once, atomically, in Phase 6 (`universe transition
--confirm`) — it both closes the outgoing era and opens the incoming one. The
beads phases (3–5) run before it against the outgoing era's **retained**,
player-partitioned history (`history <verb> --era <id>`); nothing is truncated,
so those reads are valid whether or not the era row is formally closed yet.
Phase 2 previews the flip (`--dry-run`, read-only) so the Admiral confirms the
plan before the beads work. The memory gate (Phase 4) runs before any new
captain session so false priors never prime even once. The `captain.player_id`
repoint is part of the same Phase 6 command, so the supervisor can never wake as
the dead prior-era player (sp-m602).
