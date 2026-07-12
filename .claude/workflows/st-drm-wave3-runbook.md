# st-drm WAVE 3 — RUNBOOK (bead st-drm.6): harness INCOME → GATE → COMPLETE to green

**This is a RUNBOOK, not a Workflow.** The live stack (twin `:8080`, daemon socket `/tmp/spacetraders-daemon-test.sock`, Postgres `:5434`) is a **global singleton**, and harness scenarios are **long-running** (real-compressed travel + async containers). A subagent blocking on one hits the 180 s no-tool-call stall — this is exactly how the first workflow burned 1.2 M tokens. So the **orchestrator (main loop) drives the runs**; parallelism is limited to *focused, in-process fix dispatches*.

Runs only after Wave 2 (`st-drm-wave2.js`) has merged and **all low-level acceptance is green** (one unknown per red).

## Trust hierarchy (the whole point)
harness > bootstrapper code > design — all fallible. A red harness scenario ⇒ **default-suspect the coordinator (gobot), fix it there.** Only suspect the twin if the twin demonstrably diverges from the real API (cross-check `gobot/api/openapi.json` + official docs). The twin is now shape- and behavior-verified, so most reds should be coordinator bugs.

## Stack (orchestrator runs these directly — never a subagent)
- Twin: `cd twin && ./node_modules/.bin/tsx src/main.ts` on `:8080` (fast: set the Wave-1 knobs — high `TWIN_ARRIVAL_COMPRESSION`, low `TWIN_MIN_TRAVEL_MS`, and the daemon `ST_CLOCK_DRIFT_BUFFER_MS`≈50 — exact names in the st-drm.8 report + `bootstrap-harness/tests/helpers/daemon.ts`).
- Harness boots its OWN isolated daemon (worktree gobot bin, `HARNESS_GOBOT_DIR`) pointed at the twin via `ST_API_BASE_URL`, DB `:5434` (`HARNESS_TEST_DATABASE_URL`).
- **HARD-RESET `:5434` (TRUNCATE all public tables CASCADE) + restart the twin between phase runs** — daemon-DB + twin state leak across back-to-back runs and cause phantom reds (learned the hard way).
- Seed: `spacetraders player register --new --agent TWINAGENT --faction COSMIC` (ST_API_BASE_URL→twin, DATABASE_URL→5434, `--socket` the test sock).

## The loop (per phase, in order: INCOME → GATE → COMPLETE)
1. **Run the phase suite**, one file at a time, Bash timeout up to 600 000 ms (orchestrator has no stall limit):
   `cd bootstrap-harness && ./node_modules/.bin/vitest run tests/income/<spec>.e2e.test.ts` (then `tests/gate/…`).
2. **On green:** next spec. **On red:** read the assertion + the daemon heartbeat/logs. Form ONE root-cause hypothesis (default: coordinator).
3. **Dispatch a focused fix** — a single `nw-software-crafter` in an **isolated worktree**, scoped to the specific coordinator/gobot (or, only if justified, twin) change, verified by **gobot unit tests / targeted `go test` + `go build`** (NOT the live harness — that's yours). Merge its branch (diffstat-verify non-empty), rebuild `gobot/bin/*`.
4. **Re-run the failing spec** (orchestrator). Iterate. Only advance when the spec is green **for the right reason** (not a widened budget masking a real bug — cross-check against the Wave-1 harness-audit findings, st-drm.10).
5. When a *class* of red needs competing hypotheses, you MAY fan out **read-only diagnostic** agents (each investigates one hypothesis, in-process, returns a verdict) — but the FIX is a single serial change and the RE-RUN is always yours.

## Phase specifics
- **INCOME (9 specs)** — income-entry seeds `world.haulers=[]`; the coordinator BUYS haulers 0→N (each PurchaseShip appends + the daemon tags `dedicated_fleet='contract'` locally). `obs.Haulers` reads that daemon-local tag (bootstrap_ports.go). **No D1 seeding needed for INCOME.** Watch: hauler buys need the `SHIP_LIGHT_HAULER` shipyard listing (present) + hub placement (`haulers[].parkedHub` on navigate) + the fleet-unassign / batch-contract report ops.
- **GATE (8 specs)** — gate-entry seeds `world.haulers` (control-plane array) but the repurpose-able hulls must also exist in `world.ships` **and** carry the daemon-local `dedicated_fleet='contract'` tag for `obs.Haulers` to see them → **D1 seeding IS needed here** (seed N `SHIP_LIGHT_HAULER` into `world.ships` + tag the test-DB rows after `startTestDaemon`). Gate site = `X1-PZ28-I67`. Watch: construction get/supply, worker sizing, executor-bounce/repurpose report ops.
- **COMPLETE** — the GATE golden-path + `gate-monitor-complete` specs assert the gate FINISHES (construction 100% → COMPLETE / hand-off). Confirm those actually assert completion, not just construction-started (this was a st-drm.10 audit item).

## Done
All 25 harness specs green ⇒ close st-drm.6 ⇒ the epic goal (bootstrapper flawless DATA→INCOME→GATE→COMPLETE against the twin) is met ⇒ Wave 4 (st-drm.7) min-wall-time optimization is optional polish. Push; consider merging `feat/twin-digital-twin` → main.
