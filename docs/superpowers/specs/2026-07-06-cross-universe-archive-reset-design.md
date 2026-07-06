# Cross-Universe Archive & Reset — Design

**Date:** 2026-07-06
**Status:** Draft — requires Admiral sign-off (Tier 3: schema changes + a destructive reset path)
**Beads:** requirements seed `sp-4uyh` (rig db); hard prerequisite to `st-wm7` (new-universe bring-up, city db)
**Related:** `docs/superpowers/specs/2026-07-06-ai-engine-city-bridge-design.md` (the engine this feeds),
`docs/refactoring/2026-07-06-gobot-ai-enablement-features.md` (scout feature #8, reframed here from
wipe-hygiene to archive-then-reset)

## Purpose

SpaceTraders resets its universe every ~2-3 weeks: new map, new agent registration, all
server-side state gone. The fleet has ZERO reset handling today — universe-1 (TORWIND,
X1-PZ28) died on 2026-07-05 and its rows still sit in the live Postgres, where they would
poison a fresh start (stale waypoints, dead ship instances, a jump-gate that no longer
exists).

The Admiral's requirement: do **not** just wipe. Each universe's history is a learnable
corpus. The loop is:

1. **Archive** — on reset, copy the cross-universe-valuable tables into an era-stamped
   archive before any truncation; era-close the universe-scoped beads.
2. **Analyze** — the Admiral (design-time) and the crew (play-time) query the corpus
   across eras: which goods ran thin, which contract types paid, how the ramp curve bent.
3. **Prime** — the new era's captain and specialists cold-start with priors, not amnesia:
   "last universe, ADVANCED_CIRCUITRY ran thin — expect the same class."

Two state stores are affected, each with its own half of the design:

- **Postgres** (`spacetraders` db) — quantitative history. Archive schema + `history` verbs.
- **Beads** (`sp-` rig ledger, dolt-backed) — qualitative memory. Era labels + close ritual
  + the memory-review gate.

---

## 1. Era identity

### What keys a universe

The server tells us. `GET https://api.spacetraders.io/v2/` (the API root) returns
`resetDate` — the date the current universe was created — plus `serverResets.next` and
`serverResets.frequency`. `resetDate` is the canonical universe identifier: it changes
if and only if the universe resets. The gobot API client does not call this endpoint
today (no `resetDate` reference anywhere in `gobot/internal/`); a thin
`GetServerStatus()` on the API adapter is a prerequisite (see §7 Dependencies).

On our side, one agent plays one universe (fleet policy), so the **agent symbol is the
era's human name**. A new universe means a new registration, which means a new
`players` row — `players.id` is already the foreign key on almost every live table, so
each era's data naturally hangs off its player row.

### The era registry

A small table anchors everything (lives in the `archive` schema, §2):

```sql
CREATE TABLE archive.eras (
    era_id              SERIAL PRIMARY KEY,
    name                TEXT UNIQUE NOT NULL,     -- lowercase agent symbol: 'torwind'
    agent_symbol        TEXT NOT NULL,            -- 'TORWIND'
    faction             TEXT,                     -- from players.metadata starting_faction
    player_id           INT NOT NULL,             -- players.id (no FK: archive is self-contained)
    universe_reset_date DATE,                     -- server resetDate while this era lived
    registered_at       TIMESTAMPTZ,              -- players.created_at
    archived_at         TIMESTAMPTZ,              -- when universe archive ran
    verified_at         TIMESTAMPTZ,              -- when universe verify passed (truncate gate)
    closed_at           TIMESTAMPTZ,              -- when the era-close ritual finished
    final_credits       BIGINT,                   -- last known treasury (L28 anchor method)
    notes               TEXT
);
```

- **Era name**: lowercase agent symbol (`torwind`, then whatever universe-2's agent is).
  `era_id` gives ordering; `universe_reset_date` gives absolute anchoring.
- **Bead label form**: `era:torwind` (already the convention seeded in `sp-4uyh`).
- **Row creation moments**: at registration for future eras (`player register --new`
  creates the era row alongside the players row, §6 phase 8); backfilled once for
  `torwind` during the one-time cleanup (§5).

### Reset detection

`spacetraders universe status` (new verb, §2 command surface) prints: server
`resetDate` + next expected reset, the open era's recorded `universe_reset_date`, and
**MISMATCH** when they differ — the universe reset under us. Two consumers:

- **Play-time**: the Watchkeeper polls it daily (cheap: one API call). On mismatch it
  (a) touches `captain/DISABLED` — fail-safe halt; the dead universe's token 401s on
  every call anyway, so halting is strictly correct — and (b) mails the Admiral. It
  never clears the switch; clearing is Admiral-only, always. Touching the Watchkeeper
  is itself Tier 3 (safety rail) — this detector ships under the same Admiral sign-off
  as the rest of this design.
- **Runbook-time**: phase 0 of the reset runbook (§6) confirms the mismatch before
  anything destructive is even reachable.

---

## 2. Postgres layer

### 2.1 Per-table classification

Every production table, including the two that exist only as hand migrations (no GORM
model). Source of truth for models: `gobot/internal/adapters/persistence/models.go`;
for model-less tables: `gobot/migrations/013/014_*.sql`.

**Classes**: **ARCHIVE** = copy into the archive schema, then truncate live.
**WIPE** = truncate live, no copy. **KEEP** = never touched by reset.

| Table | Class | Reasoning |
|---|---|---|
| `players` | KEEP | The era anchor. One row per agent per universe; new era appends a row. Old rows stay (era registry references `player_id`). On era close the dead token is blanked (`token = ''` — column is NOT NULL) since the server invalidated it at reset. |
| `transactions` | ARCHIVE | The P&L corpus — the single most valuable table. Category/type/amount/operation_type per row answers what earned, at what margin, on what mix (L41 payout lumpiness, L56 per-stream isolation, s84 $/h archaeology). |
| `contracts` | ARCHIVE | Contract economics: type, faction, both payments, deliveries JSON (goods + units), deadlines, fulfilled flag. First-contract evaluation is cold-start decision #1 (scout #1); priors on what contract classes pay come from here. |
| `market_price_history` | ARCHIVE | THE priors table: GOOD-level supply/volatility/trade-volume behaviour repeats across universes even as maps differ (L18 fuel-is-volatile, L13 saturation, the ADVANCED_CIRCUITRY-ran-thin class). Exists in prod via migration 016 despite the AutoMigrate gap (§7). |
| `captain_events` | ARCHIVE | Incident/event frequency corpus: how often crash bursts, credit thresholds, workflow failures fired per era — grounds detector tuning and the retrospective. |
| `manufacturing_pipelines` | ARCHIVE | Chain outcomes: product good, total cost/revenue/net profit, status, pipeline_type (includes the CONSTRUCTION gate campaign — the whole s83-s91 saga is legible here). |
| `manufacturing_tasks` | ARCHIVE | Per-task economics: task type, good, quantity, cost/revenue, retry counts. Which links of a chain failed and what acquisition actually cost. |
| `manufacturing_task_dependencies` | ARCHIVE | Tiny; needed to reconstruct archived task graphs. |
| `goods_factories` | ARCHIVE | Legacy engine but carries real outcomes (target good, TotalCost, QuantityAcquired, speedup). Cheap to keep. |
| `arbitrage_execution_logs` | ARCHIVE | Built explicitly "for ML training" (migration 013): opportunity features at decision time + actual outcome + price drift (migration 014). Model-less in Go — archived via raw SQL copy (§2.3). |
| `waypoints` | ARCHIVE (as dimension), then wipe | Map data is per-universe, but archived market/contract rows reference waypoint symbols; a skeletal copy (symbol, system, type, traits, x, y) makes the corpus interpretable ("the thin exporter was an ORBITAL_STATION"). Few hundred rows. Live table wiped: no player scoping, symbol collisions across universes are possible in principle, and stale map rows are exactly the poison class. |
| `market_data` | WIPE | Current-price snapshot only; the time series lives in `market_price_history`. Nothing here that the history table doesn't hold better. |
| `ships` | WIPE | Ship instances die with the universe. Cross-universe ship knowledge ("LIGHT_HAULER = 2× cargo, 0.4× speed", L49) is beads material; purchase economics are already in `transactions` (PURCHASE_SHIP rows). |
| `system_graphs` | WIPE | Pathfinding caches for a dead map. |
| `containers` | WIPE | Execution-run records for dead operations. The learnable residue (incident patterns) is in `captain_events` + beads lessons. |
| `container_logs` | WIPE | High-volume log lines; diagnostic value expired with the incidents. Lessons already distilled to beads. |
| `manufacturing_factory_states` | WIPE | Transient factory feed-state (delivered inputs, ready flags). Economics live in tasks/pipelines. |
| `gas_operations` | WIPE | Operational state (ship lists, status, errors) with no outcome columns; economics are in `transactions`. Gas evaluated immaterial in era 1 anyway (sp-s2q). |
| `storage_operations` | WIPE | Same class as gas_operations. |

**What the fresh universe starts with**: a new `players` row + `config set-player`,
an open `archive.eras` row, and otherwise empty live tables. Waypoints, markets,
graphs repopulate through normal scouting; the daemon's additive startup AutoMigrate
(`cmd/spacetraders-daemon/main.go:111-120`) plus hand migrations guarantee schema.
All priors come through the `history` verbs and beads — never through leftover live rows.

### 2.2 Archive mechanism — recommendation

**Recommended: a dedicated `archive` Postgres schema in the same database, with
era-stamped copies of the ARCHIVE-class tables.**

Shape: each archive table mirrors its live column set plus `era_id INT NOT NULL`
(join key to `archive.eras`) and `archived_at TIMESTAMPTZ NOT NULL`. Primary keys
become composite `(era_id, <original pk>)` — serial ids restart per era after
`TRUNCATE ... RESTART IDENTITY`, so original ids alone would collide across eras.
**No foreign keys** out of the archive schema: the archive must stay self-contained
and immune to live-table surgery; `agent_symbol` is denormalized onto `archive.eras`.

Why this beats the alternatives:

| Option | Verdict | Why |
|---|---|---|
| **`archive` schema, era-stamped tables** | **RECOMMENDED** | Queryable live for priming (same connection, same config, one `database.NewConnection`); cross-era queries are plain SQL (`GROUP BY era_id`); invisible to live code paths (nothing in gobot queries `archive.*` unless it means to); GORM handles schema-qualified names (`TableName() "archive.transactions"`), and under SQLite tests that string is just a quoted table name — the existing `NewTestConnection` TDD flow keeps working. |
| Era-stamped archive tables in `public` (e.g. `hist_transactions`) | Workable, weaker | Same properties, but clutters the working namespace and loses the one-line "everything under `archive.` is immutable history" invariant (and the matching one-line pg_dump of just the corpus). |
| `universe_id` column on live tables, never delete | REJECTED (per `sp-4uyh`) | Unbounded live tables, and one forgotten filter anywhere (repo, verb, coordinator) silently feeds dead-universe rows into live decisions — the exact poison this design exists to prevent. |
| Separate archive database | REJECTED | Second connection/config; Postgres cannot join across databases, so "compare current market vs archived priors" queries die; more ops surface for zero isolation benefit over a schema. |
| pg_dump per era | REJECTED **for priming**, KEPT as safety layer | A dump is a backup, not a queryable corpus — the captain cannot prime from it. But phase 1 of the runbook (§6) always takes a full pg_dump before anything destructive: it is the rollback for truncation. |

DDL lands twice, per the s73/L52/L54 lesson (test DBs AutoMigrate from tags; production
needs hand migrations): a hand-written `migrations/031_add_archive_schema.up.sql`
(`CREATE SCHEMA IF NOT EXISTS archive` + tables + indexes) **and** the archive models
registered in AutoMigrate (§7 makes the registration structural).

Useful indexes (query-driven, from the §3 verb list):
`archive.transactions (era_id, category, timestamp)`,
`archive.market_price_history (good_symbol, era_id, recorded_at)`,
`archive.contracts (era_id, type)`,
`archive.manufacturing_pipelines (era_id, product_good)`.

### 2.3 Archive-then-truncate ordering

The copy is `INSERT INTO archive.<t> (era_id, archived_at, <cols>) SELECT :era, now(),
<cols> FROM public.<t>`, one transaction per table, `ON CONFLICT DO NOTHING` on the
composite PK so re-runs are idempotent. `arbitrage_execution_logs` (no GORM model) is
copied by the same raw-SQL path — the archive command works at SQL level throughout,
so a model gap can never silently exclude a table.

**Hard ordering invariant: nothing truncates until verification passes.** The
`universe truncate` verb refuses to run unless `archive.eras.verified_at` is set for
the era, and verification (`universe verify`) recomputes, per table: live row count vs
archived-for-this-era row count, plus domain checksums (e.g. `SUM(amount)` and
min/max timestamp on transactions; `SUM(net_profit)` on pipelines). Counts live in the
verification report printed and stored in `archive.eras.notes`.

Truncation is `TRUNCATE ... RESTART IDENTITY CASCADE` over every table in §2.1 except
`players` and the `archive` schema. CASCADE resolves the container/ship/log FK web in
one statement.

### 2.4 Command surface (Postgres phases)

New cobra group `spacetraders universe`, registered in
`gobot/internal/adapters/cli/root.go` beside the existing commands, built in a new
`gobot/internal/adapters/cli/universe.go` following the `ledger.go` pattern
(LoadConfig → NewConnection → repo/handler → tabwriter output):

```
spacetraders universe status
    Server resetDate + next reset vs the open era's recorded reset date.
    Exit code signals MISMATCH (scriptable by the Watchkeeper detector).

spacetraders universe archive --era torwind [--player-id 1]
    Creates/finds the era row (backfill path), runs the INSERT...SELECT copies,
    stamps archived_at, prints per-table copied counts. Idempotent.

spacetraders universe verify --era torwind
    Read-only count+checksum comparison; on success stamps verified_at and
    prints the report. Idempotent.

spacetraders universe truncate --era torwind --confirm torwind
    Refuses without verified_at; refuses unless --confirm repeats the era name;
    refuses if any archive table is empty while its live source is non-empty.
    Truncates per §2.3, blanks the dead player token, prints what it did.
    Admiral-run by policy (and the captain's allowlist never includes it).
```

---

## 3. `history` query verbs — the priming surface

New cobra group `spacetraders history` (same `universe.go`/`ledger.go` construction
pattern; reads **only** `archive.*`). Distinct from the existing live-universe verbs —
`market history` and `market volatility` (`market.go:287,384`) keep their current
semantics over live `market_price_history`; the `history` group is the cross-era lens.
Default era scope: `--era all` for pattern queries, latest closed era for `summary`.

Grounded in what era 1 actually needed (lessons/friction as evidence):

```
spacetraders history eras
    The registry: era_id, name, agent, faction, reset date, duration,
    final credits. Orientation for every other query.

spacetraders history goods --good ADVANCED_CIRCUITRY [--era torwind]
    Per era: # markets exporting/importing it, median buy/sell, supply-level
    distribution (how often SCARCE/LIMITED), trade-volume stats, volatility.
    Answers: "did this good run thin last universe?" (the sp-4uyh headline
    example), "is fuel volatile everywhere or was that TORWIND?" (L18),
    "what volume ceiling should I expect?" (L13 saturation at 6-20 SCARCE).

spacetraders history contracts [--era N] [--good G]
    Count by type/faction/good, avg + variance of total payout, payout per
    delivered unit, fulfillment rate, accept-to-deadline slack stats.
    Answers cold-start decision #1 (scout #1: first-contract evaluation) with
    priors instead of a blind accept; variance materializes L41 (payouts are
    LUMPY — never annualize one draw).

spacetraders history pnl [--era N] [--by-category | --by-operation]
    Era P&L rollup from archive.transactions: net by category (CONTRACT vs
    TRADING vs SHIP_INVESTMENTS...), by operation_type (contract/arbitrage/
    factory), and a daily net curve — the ramp shape. Answers "what income mix
    won, and how fast did treasury compound?" — the cross-era ramp comparison
    is the Admiral's design-time KPI baseline (s84's manual ledger archaeology,
    done once, structurally).

spacetraders history manufacturing [--era N] [--good G]
    Pipeline outcomes: per product good — count, success rate, avg cost,
    avg net profit; construction pipelines included (pipeline_type). Answers
    "which chains were worth running?" before the Trade Analyst re-derives it
    from nothing.

spacetraders history events [--era N] [--type T]
    captain_events frequencies and timing (crash bursts, threshold crossings,
    workflow failures per week). Feeds detector tuning and the retrospective's
    incident section.

spacetraders history summary [--era N]
    The cold-start brief, one screen: duration, final treasury, income mix %,
    top-5 goods by net trading profit, contract stats one-liner, goods that ran
    thin (dominant supply SCARCE + low trade volume), fuel price band, event
    highlights. This is what the new captain reads at first wake, and the raw
    material for the retrospective bead (§4.4). Directly answers st-wm7 item
    (3)'s "reset-context brief".
```

Consumers: **play-time** — the captain's first-wake ritual and Trade Analyst /
Fleet Architect consults run these via CLI (they are read-only, allowlist-safe);
**design-time** — the Admiral uses the same verbs or raw SQL over `archive.*`.

All output is prior, not fact — the captain template's cold-start clause (§4.5) says
so explicitly, because a new universe can genuinely differ.

---

## 4. Beads layer

The `sp-` rig ledger survives resets — that is its virtue. The problem is that it
survives *indiscriminately*: post-migration it holds era-1 tactics as if they were
current. The design splits every bead and memory into two classes.

### 4.1 The two bead classes

**Universe-INDEPENDENT (persist untouched, no era label):** about the BOT, not the
game state. Code-improvement `feature` beads (the scout backlog), `bug` beads, the
Shipwright queue (`-l shipwright`), process/tooling beads, and universal lessons
("fuel is volatile", "frozen ledger IS the alarm" — L61). Reset does not touch them.

**Universe-SCOPED (era-label + close on reset, preserved queryable):** about the
game state. Nearly all `decision` beads (they name ships, waypoints, the gate),
consult and handoff beads, wake-summary notes' parent beads, universe-specific
lessons ("D45 is the only ADVANCED_CIRCUITRY exporter"), and **the living strategy
bead**. On reset these get `era:<name>` labels; the open ones are bulk-closed
(`bd close <ids...> --reason "era <name> ended (universe reset <date>)"`). Nothing
is deleted — history stays queryable forever via
`bd list -l era:torwind --status closed`, and the dolt backing means even the
labeling operations themselves are versioned and reversible.

**Forward convention (prevents every future cleanup):** universe-scoped beads are
born era-labeled. The captain template's decision ritual adds the current era label
to every `decision`/consult/handoff bead at creation; the strategy bead is created
with `-l strategy,era:<name>`. The safety net at era close is a created-date window
sweep (everything of the scoped types created between era start and end gets the
label if missing) — discipline helps, the sweep guarantees.

### 4.2 The memory-review gate (the sharpest risk)

`bd remember` memories are auto-primed into **every** session (`bd prime`). A
universe-specific memory therefore hands a fresh captain FALSE priors as if they were
standing truth — worse than amnesia, because it arrives with the authority of memory.
There are 62 memories today (61 migrated lessons + 1 bd-quirk); memories have keys but
no labels, so the era-label mechanism cannot reach them. Hence a gate, run at every
era close, with a human in the loop:

1. **Sweep**: dump `bd memories --json`; classify each memory into
   - **KEEP** — universal as written (game heuristics like L1-L18 seeds; bot
     mechanics like L19 two-backends, L25 launch-one-at-a-time, the bd-quirk note);
   - **REWRITE** — universal heuristic wrapped in universe-specific evidence: strip
     the instance, keep the rule and the decision-id citations. L47 keeps "phantom
     cache recurs after each contract; `ship refresh` is the first move" and drops
     "TORWIND-3 cached 44/80 IRON_ORE"; L58 keeps "availability claims are
     time-stamped; your own pipelines wake factory exports" and drops
     "FAB_MATS @ F56, ADVANCED_CIRCUITRY @ D45";
   - **RETIRE** — irreducibly universe-specific ("D45 is the only ADV_CIRCUITRY
     exporter"). Its text is first preserved as a note on the era's retrospective
     bead (§4.4), then `bd forget <key>` removes it from priming.
2. **Approve**: the classification table lands as a note on the era-close checklist
   bead, flagged for the Admiral (`bd human`-style). ~60 rows, minutes to eyeball.
   No forget/rewrite executes before approval.
3. **Apply**: mechanical — `bd remember --key <key> "<rewritten>"` updates in place
   (same key = update semantics); `bd forget <key>` after the retro-note copy.

**Forward hygiene rule** (captain template, Tier 3): `bd remember` is for
universe-independent heuristics ONLY; universe-specific observations belong in
decision-bead notes or the strategy bead. This keeps the gate's workload flat instead
of growing per era.

### 4.3 The strategy bead lifecycle

The living strategy bead is universe-scoped by nature (sp-s2q is saturated with
X1-PZ28 premises, gate bills, TORWIND hauler counts). Lifecycle: born era-labeled →
edited in place all era (dolt holds its history) → at era close, closed with reason
"demoted to retrospective input" → its final text becomes the opening section of the
era retrospective → a **fresh** strategy bead is created for the new era, seeded from
the retrospective (§4.4). The `strategy` label always identifies exactly one OPEN
bead: the current era's.

### 4.4 Cross-era retrospective → fresh-strategy seeding

At each era close, one `design` bead `retro: era <name>` (labels
`era:<name>,retrospective`) is written, composed from:

- `spacetraders history summary --era <name>` (the quantitative story: treasury
  curve, income mix, thin goods, incident stats);
- the closed strategy bead's final posture;
- decision-bead highlights (the era's biggest wins/losses by outcome notes);
- the RETIRE-class memories (preserved verbatim as notes).

The new era's strategy bead is then created seeded with: the universal KPI skeleton,
pointers to all prior retro beads, and a "priors to test early" section distilled
from `history summary`/`history goods` (e.g. "era-1: ADVANCED_CIRCUITRY thin
everywhere — verify within first scout sweep"). For era 2 the Admiral+harbormaster
write the retro (the era-1 captain is gone); from era 3 on, writing the retro draft
can be the outgoing captain's last-wake ritual when a reset is announced in advance
(`serverResets.next` is public), with the Admiral finalizing.

### 4.5 Captain cold-start priming

The captain template (Tier 3 to change) gains a cold-start clause: on the first wake
of a new era — detectable because the strategy bead's era label differs from the last
handoff — run `spacetraders history summary` and `history goods` for the first
contract's goods; read the current strategy bead's priors section; treat every prior
as a hypothesis with a cheap early test, never as fact. Specialists get the mirror
instruction for consults (Trade Analyst answers "is X viable?" with live data first,
archive priors second, clearly separated).

---

## 5. One-time cleanup: the already-dirty state

The bridge migration (2026-07-06) imported the era-1 corpus into the LIVE `sp-`
ledger **un-tagged**: 216 TORWIND decision beads (50 still open, the rest closed at
import where outcomes existed), 61 lessons as auto-priming memories, friction/backlog
items as `feature` beads, and strategy bead `sp-s2q` still OPEN with `-l strategy`.
Left as-is, the universe-2 captain wakes believing TORWIND, 7.7M credits, and a
half-built gate at X1-PZ28-I67 exist. This cleanup is the era-close ritual (§6 phases
5-7) run once, manually, Admiral present, BEFORE `st-wm7` brings up universe 2:

1. **Era-tag the corpus**: `bd label add <ids...> era:torwind` over all `decision`
   beads (all 216 are era-1 by construction — the fleet has never played another),
   plus consult/handoff-labeled beads, plus `sp-s2q`. `bd label add` accepts multiple
   ids; drive it from `bd list -t decision --json`.
2. **Close the open decisions**: the ~50 open `decision` beads →
   `bd close <ids...> --reason "era torwind ended (universe reset 2026-07-05)"`.
   They remain queryable: `bd list -l era:torwind --status closed`.
3. **Triage migrated friction/backlog features**: `feature` beads labeled
   `friction`/`backlog` that describe dead-universe situations (gate campaign state,
   TORWIND ship specifics) → era-tag + close; pure tooling asks (already distilled
   into the scout's ranked list) stay open as the Shipwright intake. This is a
   judgment sweep — small, done once.
4. **Demote `sp-s2q`**: era-tag, close with reason "demoted to retrospective input";
   its content opens the `retro: era torwind` bead (§4.4).
5. **Run the memory-review gate** (§4.2) over the 62 memories: classify, Admiral
   approves the table, apply. Expectation from sampling: seeds L1-L18 mostly KEEP;
   mechanics lessons mostly KEEP; the handful naming TORWIND ships/waypoints/markets
   REWRITE; pure map facts RETIRE onto the retro bead.
6. **Write the era-1 retrospective + seed the era-2 strategy bead** — blocked only on
   the Postgres archive existing first (the retro wants `history summary`), which is
   why the runbook (§6) orders Postgres phases before beads phases.

Everything here is reversible: beads are dolt-versioned, and `bd forget` casualties
are pre-copied onto the retro bead.

---

## 6. Reset orchestration — the runbook

One ordered, idempotent sequence; each phase names its actor, its tool, and its
reversal. It lives as `docs/runbooks/universe-reset.md` plus a per-reset checklist
bead cloned from a template (st-wm7 is the ur-instance). Postgres phases are the
`spacetraders universe` verbs; beads phases are `bd` sequences with a dry-run helper
(prints commands without executing — the `captain-migrate` pattern). The whole
runbook is Admiral-triggered; nothing in it ever runs autonomously.

| # | Phase | Actor / tool | Idempotency & reversal |
|---|---|---|---|
| 0 | **Freeze**: confirm reset (`universe status` MISMATCH), `captain/DISABLED` set (Watchkeeper auto-set on detection, §1), fleet services down | Watchkeeper auto + Admiral confirm | Read-only + a flag file |
| 1 | **Safety snapshot**: full `pg_dump` of the live db → `archives/pg/<era>-final-<ts>.dump`; sanity via `pg_restore --list` | Admiral (documented command) | Re-runnable; this dump is the master rollback for phase 4 |
| 2 | **Archive**: `universe archive --era <name>` — era row + INSERT...SELECT copies | Admiral or harbormaster | Idempotent (ON CONFLICT DO NOTHING); reversal = drop era's archive rows |
| 3 | **Verify**: `universe verify --era <name>` — counts + checksums, stamps `verified_at` | Same | Read-only; HARD GATE for phase 4 |
| 4 | **Truncate live**: `universe truncate --era <name> --confirm <name>`; blanks dead token | **Admiral only** | Guarded triple (verified_at + name echo + non-empty-archive check); reversal = pg_restore of phase-1 dump |
| 5 | **Beads era-close**: date-window label sweep + `bd label add ... era:<name>` + bulk `bd close --reason` on open scoped beads + strategy demotion | Harbormaster (dry-run first) | Dolt-versioned; label/close are non-destructive |
| 6 | **Memory-review gate**: sweep → classification note → **Admiral approves** → apply (rewrite/forget after retro-copy) | Harbormaster proposes, Admiral approves | RETIRE text pre-copied to retro bead; dolt history |
| 7 | **Retrospective**: write `retro: era <name>` bead from `history summary` + closed strategy + decision highlights | Admiral + harbormaster (later: outgoing captain drafts) | Additive |
| 8 | **Register new agent**: `player register --new --agent <SYM> --faction <F>` (scout #8: calls the API itself using the account token; stores agent token, creates players row + OPEN era row with server resetDate); `config set-player` | **Admiral only** (st-wm7: "registration is the Admiral's call") | API-side one-shot; local rows re-creatable |
| 9 | **Seed fresh strategy**: new strategy bead `-l strategy,era:<new>` from retro + priors (§4.4) | Harbormaster drafts, Admiral blesses | Additive |
| 10 | **Bring-up** per st-wm7: dashboards repointed, smoke checks, **Admiral clears `captain/DISABLED`** | **Admiral only** | The kill switch is never cleared by any automation, ever |

Ordering rationale: Postgres before beads because the retrospective (phase 7)
consumes `history summary`, which needs the archive (phase 2); truncate before
registration so the new agent never coexists with poisoned live rows; memory gate
before any new captain session so false priors never prime even once.

---

## 7. Testing, dependencies, and pipeline placement

### The AutoMigrate prerequisite (latent bug — fix first)

`database.AutoMigrate` (`gobot/internal/infrastructure/database/connection.go:87-104`)
hand-lists 15 models; `GasOperationModel`, `StorageOperationModel`,
`MarketPriceHistoryModel` (all defined in `persistence/models.go`) are missing. This
is the s73/s77 schema-drift class (L52/L54, three recurrences). It gates this design
twice: the fresh-universe DB and every test DB must materialize ALL tables (a
truncated-then-rebuilt or fresh dev environment leans on AutoMigrate at daemon boot),
and the archive models themselves must be registered or the archive breaks in test
envs the same silent way.

**Structural fix, not another list-edit**: introduce `persistence.AllModels() []any`
as the single canonical model registry; `AutoMigrate` consumes it; a unit test walks
the `persistence` package (or the registry vs a literal count) so a model added
without registration fails CI. Archive models join the same registry. Note
`arbitrage_execution_logs` stays model-less deliberately — the archive copies it at
SQL level (§2.3), so it needs no model; the test that pins archive completeness
enumerates TABLES (information_schema against a migrated test db), not models.

### TDD per piece (patterns already in the repo: fake repos as in
`captain_ops_test.go`, sqlite via `NewTestConnection`)

- **Era registry / status**: fake API client returns a scripted `resetDate`; test
  MISMATCH detection against a seeded era row; exit-code contract.
- **Archive copy**: seed live fixtures (2 players/eras worth) → archive era 1 →
  assert per-table counts, era stamping, and that era 2's rows were NOT copied;
  re-run archive → counts unchanged (idempotence).
- **Verify**: tamper one archived row → verify fails; untampered → `verified_at` set.
- **Truncate guards**: without `verified_at` → refused; wrong `--confirm` → refused;
  after truncate → live tables empty, `players` intact, `archive.*` intact, dead
  token blanked.
- **History verbs**: fixture archive rows with known aggregates → each verb's numbers
  asserted (thin-good detection, contract payout variance, P&L mix). Pure
  read-path tests, sqlite-friendly.
- **Watchkeeper reset detector**: existing detector test pattern
  (`internal/captain`): scripted status mismatch → DISABLED touched + one Admiral
  mail, and never a clear.
- **Beads phases**: the dry-run helper is tested like `captain-migrate` (recording
  fake exec: `apply=false` executes nothing, command shapes pinned; `apply=true`
  executes each). The judgment steps (memory classification, friction triage) are
  deliberately human — the tooling only proposes and applies.
- **Runbook rehearsal**: before universe 2 goes live, the one-time TORWIND cleanup
  (§5) IS the first full execution of phases 1-7 — Admiral present, which doubles as
  the attended acceptance test.

### Pipeline placement

This is a **Shipwright Tier 3 feature** by definition (new schema, a destructive
verb, Watchkeeper touch, captain-template edits): this document is the spec that goes
on the feature bead; the Admiral approves it BEFORE any code (bridge-spec
self-modification rule). Suggested build order, each its own gated merge:

1. `AllModels()` registry + missing-model registration + migration check test
   (unblocks everything; fixes the live latent bug).
2. `GetServerStatus()` API call + `universe status` + era registry migration 031.
3. `universe archive` / `verify` / `truncate` (+ archive models/indexes).
4. `history` verb family.
5. Watchkeeper reset detector (Tier 3 rail, same sign-off).
6. `player register --new` era-aware registration (scout #8's other half).
7. Runbook doc + dry-run helper for beads phases.

Relationship to `st-wm7`: items (1) and (3) of that bead's checklist ARE this design;
`st-wm7` remains the bring-up gate and stays blocked until the one-time cleanup (§5)
and phases 1-4 have run for `torwind`. The kill-switch clause is restated here
deliberately: **clearing `captain/DISABLED` is the Admiral's act alone** — no verb,
agent, or runbook phase in this design clears it.
