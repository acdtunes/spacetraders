# Gobot AI-Enablement Feature Discovery — 2026-07-06

Source: `ai-feature-scout` (Fable, read-only) mining the old captain's lived
experience — `captain/state/improvement-backlog.md`, `friction.md`, `lessons.md`,
`captain-log.archive.md` — against the current CLI/daemon surface and the new
bridge engine. Evidence citations reference the pre-bridge state files (now
migrated to the `sp-` beads ledger + archived under `captain/state.pre-bridge`).

## Obsolete — do NOT rebuild
- **P1** gate fix — shipped (b4a465f).
- **P2** ship refresh — shipped (cli/ship.go, L47).
- **P5** awaiting_human visibility + **P8** pipeline priority — superseded by beads
  (gate log as bead notes; `bd` priority native; file driver deleted).
- **P11** health --wait — ops artifact (L30); the standing session gets re-nudged.
- **ship-reservation flag** (s58/s67 "top tooling ask") — rejected as phantom by the
  captain itself, reverted 6fee4f1 (assignments already give mutual exclusion). Only
  the VISIBILITY need survives (→ #4).
- **P6** credits readout — half-obsolete (player info exists); surviving need is
  derivation fragility (→ #3).

## Latent bug found (file now)
Three models — `GasOperationModel`, `StorageOperationModel`, `MarketPriceHistoryModel`
— are still unregistered in AutoMigrate (`database/connection.go:87-104` vs
`persistence/models.go`). This is the s73 schema-drift class (merged-green tests
missing migrations → hours of silent zero income; L52/L54, 3 recurrences).

## Ranked opportunities (impact 1-5 / effort S-M-L, by impact-per-effort)

1. **`contract list` / `contract get`** — observability, 5/S. Deadlines unobservable
   today; data already in Postgres (ContractModel Deadline/payments/deliveries).
   First-contract evaluation is cold-start decision #1.
2. **`market find --good G --system S [--side buy|sell]`** — decision-support, 5/S.
   "Where does X trade" = ~30-market sweep today; L58: a stale-availability premise
   flipped a whole gate plan.
3. **Anchored credits + player balance verb** — observability + detector fix, 4/S.
   CurrentCredits returns last-transaction BalanceAfter; one corrupt PURCHASE_CARGO
   row poisoned it 5× (s39/s51/s55/s63/s70), each firing spurious threshold events.
   Fix: L28 anchor to last CONTRACT_* row + subsequent amounts.
4. **Assignment/role/cache-age in `ship list`** — observability, 4/S. Assignments are
   the exclusion mechanism yet invisible; s81 phantom-benched hauler took multi-step
   inference to diagnose — a silent $/h leak that emits no event.
5. **Emit `contract.completed`/`failed` with P&L payload** — event/detector, 4/S-M.
   Event types defined with ZERO emitters (events.go:19-20). Each completed contract
   → one self-contained wake. Touches coordinator emit path (Shipwright feature).
6. **Income-stall + stream-down detectors** — event/detector, 5/M. THE wake-driven
   gap: costliest failures were silent zero-income with green telemetry (18h loop,
   s88; L61 "frozen ledger IS the alarm"). Watchkeeper-side, no daemon changes.
7. **`fleet report` one-shot snapshot verb** — observability/cold-start, 4/M. The
   deleted snapshot.go gave a free per-wake picture; the wake ritual now costs N CLI
   calls vs the spec's "idle wakes deliberately cheap."
8. **`player register --new` + universe-reset hygiene** — cold-start, 4/M,
   CALENDAR-URGENT. register demands a pre-fetched token; ZERO reset handling in
   gobot, so old-universe DB rows poison a fresh start. Feeds st-wm7.
9. **Per-stream/per-container P&L (`ledger report --by-container/--by-ship`)** —
   decision-support, 4/M. s84 stream $/h = manual ledger archaeology (UTC-3 vs UTC
   mismatch, L45/L56). Sub-fixes: print UTC; drop the --player-id-only restriction.
10. **Event dedup at Watchkeeper mail composition** — event/detector, 3/S. One retry
    burst = 4× container.crashed + 1× workflow.failed, same container_id (P10, L23) —
    now straight into wake mail. Collapse by container_id+window in wake.go.
11. **`operations status/stop` reconcile from container DB** — automation-primitive,
    3/S-M. Verbs track in-memory state, miss coordinators after restart (s75) and
    normally-launched ones (s83); safety-relevant kill path.
12. **Pipeline task visibility + `construction stop`** — observability/automation,
    3.5/M. construction status shows only "EXECUTING 0.0%"; no cancel verb (s87 mission
    blocker). Re-plan shipped s91; `operations tasks <pipeline-id>` + stop still missing.
13. **Dry-run/cost previews** (`construction start --dry-run/--budget`, `goods produce
    --plan`) — decision-support, 3/M. Gate build (biggest spend) launches blind; feeds
    Trade Analyst consults.
14. **`contract.deadline_risk` detector** — event/detector, 3/S after #1. Deadline −
    remaining-deliveries × cycle-time below margin → event.
15. **Gate deepening: schema-drift check + coordinator boot smoke** — automation-
    primitive, 4/M-L, TIER 3 (gate is a safety rail — Admiral sign-off). Gate blind to
    merged-green-tests missing migrations (s73) and daemon-killing launches (s75).
    Contains the latent bug above.

## Sequencing
#1-4 are S-effort pure reads that transform first-hour play. #8 is calendar-urgent
(before re-registration). #6 is the highest-leverage bridge-native work. #15 carries a
live bug fixable now. Enabling dependency: the shipwright agent must exist first — it
does (city/agents/shipwright, committed 2026-07-06).
