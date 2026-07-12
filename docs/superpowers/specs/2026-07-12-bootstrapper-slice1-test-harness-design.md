# Bootstrapper Slice-1 (DATA) e2e Test Harness — Design

**Date:** 2026-07-12
**Status:** Draft — brainstorm complete, written-spec review pending
**Scope:** An end-to-end test harness that exercises **Slice 1 (the DATA phase)** of the captain-bootstrap coordinator (`sp-ysgb` / `sp-3nbe`, merged) by driving the real `spacetraders` daemon + CLI against the **digital twin** (`docs/superpowers/specs/2026-07-11-spacetraders-digital-twin-design.md`). The twin is treated as an **unmodified substrate**: this harness does not build or change it. Where the harness needs to configure fixtures, it depends on `/_twin/` admin endpoints whose **contracts** are specified here (method, request, response, observable guarantee) — **not their implementation**.

---

## Purpose

Slice 1 of the bootstrapper is a long-running reconciler that, from a cold agent, buys probes to `probe_target` (3) — staged and capital-gated — and assigns each to `scout-all-markets`, holding at DATA-complete once market coverage clears `coverage_bar`. It claims to be idempotent and recovery-safe (observe → derive phase → guarded act; no persisted progress cursor).

Those claims need proof against a fast, disposable, deterministic API. This harness drives the already-hardened daemon/CLI against the twin so we can: run the DATA arc end-to-end, assert every DATA guard (capital gate, staging, dry-run, disabled, fail-closed, coverage exit), and prove restart-idempotency at a precise mid-run point — all deterministically, with no wall-clock flakiness.

## Non-goals

- **INCOME/GATE (Slices 2/3).** Out of scope. Scenario 4 asserts only that DATA correctly *hands off* to the INCOME stub; it does not exercise INCOME.
- **Building or modifying the twin.** The twin is a given. This spec defines the admin-endpoint **contracts** the harness relies on; implementing them (if not already present) is twin work, not harness work.
- **Twin-internal behavior.** How the twin stores time, computes coverage, or injects faults is not specified — only the observable contract at the HTTP boundary.
- **Observability stack.** No Prometheus/Grafana bring-up; this is a test-only harness (metrics are scraped in-process for assertions, not dashboarded).

---

## Architecture & test topology

A **Vitest e2e suite** in `spacetraders/twin/tests/bootstrap/`, a consumer of the twin. Four pieces:

- **The twin** — unmodified; boots once in global-setup; serves `/v2` (the API) + `/_twin` (admin). The harness assumes it honors the §"Admin-endpoint contracts" below.
- **The test daemon** — the same `spacetraders-daemon` binary, isolated per the twin spec's `--force` trap: test `pid_file`/`socket_path`, metrics port `9092`, a `spacetraders_test` Postgres, `ST_API_BASE_URL` → the twin, and a low `tick_seconds`. **Booted and killed per scenario.**
- **The Vitest harness** — global-setup boots + seeds the twin; each scenario owns its fixture → daemon lifecycle → assertions → teardown.
- **The admin-controlled clock** — tests freeze and advance the twin's world-clock via `POST /_twin/clock`; no wall-clock sleeps.

### Three truth surfaces (assertions triangulate all three)

1. **World truth** — `GET /_twin/state`: ships (symbol, role, `nav.status`, `nav.waypoint`, scout assignment), `agent.credits`, per-market scouted/fresh flags, coverage fraction, **and the mutation log**.
2. **Daemon truth** — the bootstrap heartbeat lines + the `spacetraders_bootstrap_phase{phase}` / `bootstrap_probes_total` metrics on port `9092` + the capital-gate decision lines in `daemon.log`.
3. **CLI truth** — `spacetraders ship list` / `agent` stdout, for human-facing field correctness.

**The mutation log is the key idempotency primitive.** Rather than infer "no double-buy" from a final ship count (which could mask a buy-then-refund), `GET /_twin/state` exposes an ordered log of world-changing calls (`PurchaseShip`, `navigate`, …) with tick correlation. Tests assert `PurchaseShip` appears **exactly `probe_target − 1` times** across an entire run — including a daemon restart — and use tick correlation to prove one-buy-per-tick staging.

---

## Admin-endpoint contracts

The harness depends on the following `/_twin/` endpoints. `reset` and `state` **extend** contracts the twin spec already defines; `clock`, `agent`, `markets/coverage`, and `fault` are **additional** contracts. Each is specified as method + request + response + **observable guarantee** — the twin's implementation is out of scope.

### `POST /_twin/reset` (extend)
Rebuild the cold-start world, optionally overriding entry state.

- **Request** (all fields optional; omitted → captured cold-start default):
  ```json
  { "credits": 175000, "probes": 1, "frigates": 1,
    "probePrice": 40000, "preScoutedMarkets": ["X1-PZ28-XX"], "coverage": 0.0 }
  ```
- **Response:** `200 { "ok": true }`.
- **Guarantee:** after the call, `GET /v2/my/agent`, `/my/ships`, the shipyard listing, and per-market scouted flags reflect exactly the requested entry state; the mutation log is empty; the world-clock is left **frozen** (see `POST /_twin/clock`).

### `POST /_twin/clock`
Deterministic control of the twin's world-clock — the sole time authority the harness relies on.

- **Request:** `{ "mode": "frozen" | "running", "advanceMs": 0, "setNow": "<rfc3339>" }` (send `mode`, or `advanceMs`, or `setNow`).
- **Response:** `200 { "now": "<rfc3339>" }` — the twin's world-`now` after the call.
- **Guarantee:** `frozen` halts the twin's world-clock at the current instant; `advanceMs` moves world-`now` forward N ms; `setNow` sets it absolutely. On any subsequent read, ship `nav.status`/location and cooldown expirations resolve against world-`now` (a ship whose stored `arrival ≤ now` reads as arrived at its destination; a cooldown whose `expiration ≤ now` reads as clear). Navigate/cooldown responses compute their target instants relative to world-`now`.

### `POST /_twin/agent`
Mutate a **running** world's treasury (the one lever that edits mid-run rather than reseeding).

- **Request:** `{ "credits": 600000 }`.
- **Response:** `200 { "credits": 600000 }`.
- **Guarantee:** subsequent `GET /v2/my/agent` reports the new balance; nothing else in the world changes.

### `POST /_twin/markets/coverage`
Force scouting coverage so the reconciler's coverage observation crosses `coverage_bar` without flying probes to every waypoint.

- **Request:** `{ "fraction": 0.95 }` **or** `{ "scoutWaypoints": ["X1-PZ28-A1", "X1-PZ28-B2"] }`.
- **Response:** `200 { "coverage": 0.95 }` — the resulting coverage fraction.
- **Guarantee:** the named markets (or enough markets to reach `fraction`) read as scouted **and fresh**, such that the daemon's coverage observation returns ≥ the requested value. `fraction` and `scoutWaypoints` are two ways to express the same target; the response reports the achieved fraction.

### `POST /_twin/fault`
Bounded fault injection for fail-closed assertions.

- **Request:** `{ "endpoint": "GET /my/ships", "code": 500, "count": 1 }` (`endpoint` is `METHOD /path` under `/v2`; `code` is the HTTP status; `count` is how many matching calls fail before the fault self-clears).
- **Response:** `200 { "armed": true }`.
- **Guarantee:** the next `count` calls matching `endpoint` return the given `code` with a correctly-shaped SpaceTraders error envelope; after `count` matches, the fault is gone and calls succeed normally. Faults do not persist across `POST /_twin/reset`.

### `GET /_twin/state` (extend)
Introspect the world for assertions.

- **Response** `200`:
  ```json
  { "agent": { "credits": 95000 },
    "ships": [ { "symbol": "…", "role": "SATELLITE",
                 "nav": { "status": "IN_ORBIT", "waypoint": "…" },
                 "scoutAssignment": "scout-all-markets" | null } ],
    "coverage": 0.42,
    "markets": [ { "waypoint": "…", "scouted": true, "fresh": true } ],
    "clock": { "now": "<rfc3339>", "mode": "frozen" },
    "mutationLog": [ { "seq": 1, "call": "PurchaseShip",
                       "detail": { "shipType": "SHIP_PROBE" },
                       "at": "<rfc3339>" } ] }
  ```
- **Guarantee:** `mutationLog` contains **one entry per world-changing API call** since the last `reset`, in call order, each with a monotonic `seq` and the world-`now` at which it occurred (for tick correlation). Read-only endpoints do not appear.

---

## The scenario matrix

Eight e2e scenarios; each admin-seeds one world and asserts one behavior, all through the test daemon + `workflow bootstrap`, with the admin clock.

| # | Scenario | Fixture | Key assertion |
|---|---|---|---|
| 1 | **Golden path** (headline acceptance) | cold: 175k, 1 probe, 1 frigate | Buys exactly 2 probes → 3 total; each assigned to `scout-all-markets`; `agent.credits` debited 2×`probePrice`; `bootstrap_probes_total` = 3; mutation log = exactly 2 `PurchaseShip`; after `markets/coverage {≥bar}`, phase derives DATA-complete and holds. |
| 2 | **Capital gate — block then release** | credits below what a probe costs within `reserve_margin` | Over a bounded tick budget: **zero** `PurchaseShip`; a `daemon.log` decision line shows the ≤`reserve_margin`-treasury block arithmetic (price, treasury, the check). Then `POST /_twin/agent {credits↑}` → advance → a buy occurs. |
| 3 | **Staging (one buy per tick)** | cold needing 2 probes | Mutation log's two `PurchaseShip` entries carry **different** tick correlations — never two buys within one reconcile tick. |
| 4 | **Coverage-bar exit → INCOME hand-off** | cold | With coverage < bar, phase stays DATA across ticks; after `POST /_twin/markets/coverage {≥bar}`, the next tick derives DATA-complete and logs the INCOME "not-yet-implemented, holding" stub. (Does not exercise INCOME.) |
| 5 | **Dry-run** | cold, `workflow bootstrap --dry-run` | Mutation log stays **empty** (no buy, no navigate); heartbeat logs the decisions it *would* take; `GET /_twin/state` shows the world unchanged. |
| 6 | **`bootstrap_disabled` escape** | test-config `bootstrap_disabled: true` | Every tick no-ops: mutation log empty, phase metric absent/idle; the escape hatch holds. |
| 7 | **Fail-closed on a read fault** | cold + `POST /_twin/fault {"GET /my/ships", 500, 1}` armed before a buy-eligible tick | That tick does **not** buy (fails closed); the observe failure is logged; the fault self-clears; the next tick resumes and buys. Proves the RULINGS #4 money-guard fails closed. |
| 8 | **Restart idempotency (mid-purchase)** | cold | No double-buy across a daemon restart — detailed below. |

## Idempotency / restart mechanics (scenario 8)

The deterministic clock + per-scenario daemon control make "mid-purchase" a precise, repeatable point. The restart pivots on a **reconcile-tick boundary**, not on arrival timing:

1. Boot the daemon; launch `workflow bootstrap`. Let it run to the tick that issues `PurchaseShip` #1 — the mutation log records it; probe #2 now exists in `GET /_twin/state`.
2. **Before** the next reconcile tick re-observes, **kill the daemon** (SIGTERM). The twin world is left intact (the probe was really bought). The daemon's Postgres holds only the operation record — no progress cursor (bootstrap keeps none by design).
3. **Reboot** the daemon (same isolated config, same test Postgres, same twin). It re-observes `GET /my/ships` → 2 probes → derives "need 1 more" (not 2) → buys exactly 1.
4. **Assert:** the mutation log shows `PurchaseShip` **exactly twice** across both daemon lifetimes — no re-buy of the probe that existed at restart; final fleet = 3 probes.

The bug this catches: an in-memory "currently buying probe #2" flag would be lost on restart and cause a double-buy. The test proves the observe → derive → guarded-act loop is genuinely restart-safe — the central idempotency claim of the bootstrap design.

---

## Harness mechanics

- **Location & runner:** `spacetraders/twin/tests/bootstrap/*.e2e.test.ts`, run via `rtk vitest run`.
- **Global setup:** boot the twin once; seed the test player by running `spacetraders player register` against the twin (writes the `players` row + JWT into the test Postgres — no hand-rolled DB seed); leave the clock frozen.
- **Per-scenario lifecycle** (a `withScenario(fixture, fn)` helper):
  1. `POST /_twin/reset` with the scenario's fixture body.
  2. Reset the daemon-owned tables in the test Postgres to empty (so no prior-scenario state leaks; the `players` row is preserved/re-seeded).
  3. Boot the test daemon with env `ST_API_BASE_URL=http://127.0.0.1:8080/v2`, `SPACETRADERS_CONFIG=twin/test-config.yaml` (low `tick_seconds`, isolated `pid_file`/`socket_path`, `metrics.port: 9092`, test `database.url`, `captain.player_id` = the seeded player). Bootstrap `[bootstrap]` knobs sit at defaults unless the scenario overrides them.
  4. Run: launch `spacetraders workflow bootstrap --agent <A>`; drive the loop with `advanceClock(ms)` between reconcile ticks; poll `GET /_twin/state` + metrics until the asserted condition or a bounded tick budget (then fail loud).
  5. Assert across the three truth surfaces.
  6. Teardown: SIGTERM the daemon; leave the twin up for the next scenario.
- **Helpers:** `twin.reset(fixture)`, `twin.state()`, `twin.mutationLog()`, `twin.advanceClock(ms)`, `twin.setCredits(n)`, `twin.forceCoverage(f)`, `twin.injectFault(...)`, `daemon.boot(opts)`, `daemon.kill()`, `daemon.restart()`, `metric(name)`, `runBootstrap(flags)`.
- **Isolation:** the isolated pid/socket/metrics port + `spacetraders_test` Postgres guarantee a test daemon never touches production (the twin spec's `--force` trap). The twin listens on its own `:8080`.

### Minimizing dependence on arrival timing

DATA's probe **purchases happen at the HQ shipyard** (the cold-start fixture places ships at HQ), so buys need no travel. **Coverage is admin-forced** (`markets/coverage`), so the DATA→exit transition needs no real scout flights. Consequently the guard matrix and the idempotency pivot depend on **reconcile-tick boundaries** (daemon-side, governed by `tick_seconds` and when the harness kills/advances), not on precise arrival timing. `POST /_twin/clock` advances world-`now` past any nav/cooldown the reconciler triggers so ticks make deterministic progress.

---

## Delivery slices

1. **Harness scaffold + golden path.** The Vitest global-setup (boot + seed twin), the `withScenario` daemon-lifecycle + clock + state helpers, `twin/test-config.yaml`, and scenario 1 (golden path) green. Landing this proves the whole rig end-to-end.
2. **Guard scenario matrix.** Scenarios 2–7 (capital gate, staging, coverage exit, dry-run, disabled, fail-closed), each a focused test on the scaffold.
3. **Restart idempotency.** Scenario 8 (mid-purchase restart) + the tick-boundary kill/reboot mechanics.

---

## Open questions

- **Daemon arrival-timer vs. admin clock.** The daemon arms a local `time.Until(arrival)` timer off the twin's returned timestamp using the *daemon's* wall clock, while the twin's world-clock is admin-controlled. Because the harness buys at HQ and admin-forces coverage, no scenario currently hinges on a probe physically arriving — but if a future assertion needs a real in-test arrival, the plan must settle how the daemon's local timer is made to fire deterministically (high time-compression for a near-immediate real ETA, vs. an admin clock-advance the daemon re-reads against). Deferred to the implementation plan; not needed for Slices 1–3 of *this harness*.
- **Test-Postgres reset granularity.** Whether per-scenario isolation truncates the daemon-owned tables or drops/recreates the schema (auto-migrate on boot) — a plan-level mechanics choice; both satisfy the isolation contract.
