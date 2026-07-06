# Universe Reset Runbook

Source of truth: `docs/superpowers/specs/2026-07-06-cross-universe-archive-reset-design.md`
(rev 2 — in-place, player-partitioned history; no archive schema copy).

One ordered, idempotent sequence. Each phase names its actor, exact copy-paste
commands, and its reversal. **The whole runbook is Admiral-triggered; nothing
in it ever runs autonomously.** Phases marked **ADMIRAL ONLY** must not be run
by any agent, script, or automation — ever.

Placeholders: `<era>` = lowercase agent symbol (e.g. `torwind`); `<AGENT>` =
agent symbol (e.g. `TORWIND`); `<ts>` = timestamp; `<reset-date>` = server
`resetDate` (YYYY-MM-DD); `<db-conn>` = the live `spacetraders` Postgres
connection string; `<rig>` = the beads rig repo root.

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

## Phase 2 — Era close (Postgres)

**ADMIRAL ONLY**

```bash
spacetraders universe close --era <era> --confirm <era>
```

Stamps `eras.closed_at` + `final_credits` (L28 anchor method), blanks the
dead player's token, truncates `market_data` + `system_graphs`
(`TRUNCATE ... RESTART IDENTITY`), backfills `waypoints.era_id` where NULL.
Refuses without `--confirm` echoing the era name; refuses if the era row is
already closed (idempotent re-run prints what is already done).

Optional, any time after this phase, never a gate:

```bash
spacetraders universe scrub --era <era> --confirm <era>
```

Deletes player-scoped WIPE-class junk rows (containers, container_logs,
ships, factory states, gas/storage ops) for the dead era's player. Never
touches ARCHIVE-class history or other players' rows.

**Reversal:** Guarded (name-echo + already-closed check). Reversal = restore
the Phase 1 dump with `pg_restore`.

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

## Phase 6 — Register new agent

**ADMIRAL ONLY**

```bash
spacetraders player register --new --agent <NEW_AGENT> --faction <FACTION>
spacetraders config set-player --agent <NEW_AGENT>
```

Calls the SpaceTraders API with the account token, stores the new agent
token, creates the `players` row and an OPEN `eras` row with the server
`resetDate`.

**Reversal:** API-side one-shot; local rows are re-creatable.

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

`universe close` (Phase 2) runs before the beads phases because the
retrospective (Phase 5) consumes `history summary`, which needs the closed
era row. `universe close` runs before registration (Phase 6) so the new
agent never coexists with unscoped stale caches. The memory gate (Phase 4)
runs before any new captain session so false priors never prime even once.
