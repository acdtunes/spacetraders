# ARCHIVED — this is the frozen era-1 captain corpus. Do not read as live doctrine.

**Every file in this directory (`captain/state/`) is a historical snapshot.**
It froze at the city-bridge cutover (commit `903a9c2`, 2026-07-06 ~14:28,
pre-bridge) when the file-based `claude -p` captain loop — the only writer of
these files — was deleted and replaced by the bd-native Watchkeeper/city-agent
captain. Nothing has written here since, and nothing ever will again: there is
no remaining code path that appends to `captain-log.md`, `decisions.jsonl`,
`lessons.md`, `friction.md`, `strategy.md`, or `improvement-backlog.md`.

## The load-bearing problem this file exists to stop

This corpus documents **universe X1-PZ28**, not the live universe. Verified
directly in this directory: `strategy.md` carries 8 references to `X1-PZ28`
and zero to any later universe; `captain-log.md` carries 29 `X1-PZ28`
references. **The live universe is X1-KA42.** A cold-start captain, a
surveyor, or the Admiral who opens `strategy.md` or `captain-log.md` expecting
current doctrine will silently orient to the wrong map, the wrong treasury,
the wrong fleet, the wrong contracts — every concrete fact in this directory
is stale by at least one full universe reset.

**Do not act on anything in this directory as if it were current.** Read it
only as history (era-1 retrospective material, "how did we used to do this").

## Where the live version of each file actually lives now

| Era-1 file | Live successor |
|---|---|
| `strategy.md` | [`sp-4m2s`](../../) — live "Fleet strategy" design bead, re-seeded per era. The frozen text here was also migrated verbatim into a `Fleet strategy`-titled bead for the historical record. |
| `decisions.jsonl` | bd `decision`-type beads. All 216 historical rows were already migrated 1:1 into `migrated`-labeled beads (verify: `bd list -l migrated`) — this file is a redundant, read-only copy of that history, not an independent source. New decisions since the bridge are tracked directly as decision beads going forward (thin so far, e.g. `sp-jqf8` — a known watch item, not something this README needs to fix). |
| `lessons.md`, `lessons-archive.md` | `bd memories` (`bd remember`). Already migrated — every `L<n>` entry has a corresponding live memory. Query with `bd memories`. |
| `friction.md` | Shipwright-labeled beads filed directly at creation time — **not** a file anyone appends to anymore. All 36 historical friction entries were migrated into `friction`-labeled beads. The rule going forward is captured in the live memory `friction-beads-must-carry-a-queue-label-e`: *file a friction/engine bead with a real queue label (e.g. `shipwright`) in the same breath you create it, or it's invisible to every polled queue and rots unseen* — this is exactly the failure mode era-1's `friction.md` had (unlabeled beads discovered stale by the `sp-tme0` sweep). |
| `improvement-backlog.md` | `feature`-type, `backlog`-labeled beads (`bd list -l backlog`). Already migrated (11 entries). |
| `captain-log.md`, `captain-log.archive.md` | No direct successor file. The session transcript (this agent's own conversation history) is the log now. Whether that is sufficient, or whether a durable narrative-log equivalent should exist in bd, is an open question for the Admiral — not decided by this README. |
| `last-meta-review` | A single timestamp (`2026-07-03T11:10:34Z`) marking the last file-era meta-review. No successor needed — meta-review is a session-scoped ritual now, not a tracked file. |

All of the above migrations ran via `gobot/cmd/captain-migrate` (see
Provenance below) and are independently verifiable in the live beads corpus —
this README does not ask you to take era-1's word for it.

## Rules for this directory, going forward

- **Do not delete these files.** They are the only surviving era-1 record;
  history verbs and future retrospectives may still read them.
- **Do not move or rename these files**, and do not edit their contents. This
  applies even though they're stale — editing frozen history destroys the
  record of what era-1 actually believed at the time.
- **Do not add new files here that anyone expects to be "live."** If you need
  a new durable fact, it goes in a bd memory or a bead, not a file in this
  directory.
- This README is additive only. If a future era needs its own frozen-corpus
  marker, add another README next to this one — don't overwrite this one.

## Provenance (why this gap existed, for whoever asks "how did this happen")

Two different pipelines both have "close"/"migrate" in their name and it is
easy to conflate them — worth being explicit that neither was the miss:

- `gobot/internal/captain/eraclose.go` (`EraClose`, run via `cmd/era-close`,
  Phase 3 "Beads era-close" of `docs/runbooks/universe-reset.md`) is a
  **recurring, per-universe-reset** tool. It only ever calls `bd list`,
  `bd label add`, `bd close`, and `bd memories` — it has zero filesystem
  interaction with `captain/state/*`, and per the 8-phase runbook it was
  never scoped to. Re-checked directly for this bead: no reference to this
  directory anywhere in `eraclose.go`, `eraclose_test.go`, or
  `cmd/era-close/main.go`.
- `gobot/internal/captain/migrate.go` (`Migrate`, run via
  `gobot/cmd/captain-migrate`) is the tool that actually reads this
  directory. It is a **one-time, bridge-cutover** data migration (introduced
  alongside the bridge work, called out as "a separate data step (in
  progress)" in the cutover commit `903a9c2`), and it already ran
  successfully — that's why the live-successor table above is populated, not
  aspirational. What it never had was a step to mark its *own source*
  archived once it finished consuming it, because by design it only reads
  and creates beads; it never writes to `captain/state/`. This README is that
  missing step, added once, by hand, now that the one-time migration is
  confirmed done. It does not need to be a recurring pipeline step: nothing
  can make this directory "freshly stale" again, because nothing writes to
  it anymore.

Filed as `sp-j5kf`.
