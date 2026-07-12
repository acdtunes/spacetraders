# st-drm.14 — Bootstrap-harness restart-resilience audit + extension

**Bead:** st-drm.14 (Admiral directive 2026-07-12) — prove the bootstrapper is resilient, idempotent,
and impervious to DAEMON RESTARTS.
**Scope owned:** TEST code only (`bootstrap-harness/**`). Twin/gobot/coordinator gaps are REPORTED, not implemented.
**Mode:** e2e specs authored-NOT-run (orchestrator runs them later on the live stack with
`TWIN_TIME_COMPRESSION=200`, `TWIN_MIN_TRAVEL_MS=50`, daemon `ST_CLOCK_DRIFT_BUFFER_MS=50`).
**Verify (this worktree):** harness unit tests 11/11 green; `tsc --noEmit` 0 errors (baseline 0 → **zero new**;
the ~34 fastify-inject errors live in `twin/`, outside the harness tsconfig).

## THE BAR (restart invariants)

- **(a)** phase re-detection lands on the SAME phase (DATA/INCOME/GATE; GATE sticky once construction started)
- **(b)** NO duplicated side effects vs a single uninterrupted run (no double buy / contract / supply; report-seam flags exactly-once)
- **(c)** in-flight work survives (a hull mid-TRANSIT at the kill is re-adopted; its arrival is detected + acted on by the NEW daemon)
- **(d)** the run CONVERGES to its exit condition afterwards
- **(e)** double/rapid restart changes nothing beyond (a)-(d)

Report-seam note (drives every verdict below): the 6+ daemon-internal ops (`fleet-unassign`, `batch-contract`,
`construction-start`, `executor-bounce`, `launch-autosizer|siting|worker-rebalancer`, `scout-assign`, `repurpose`)
are POSTed to `/_twin/report`; the twin flips a paired flag EXACTLY-ONCE (a repeat report after a reboot is a pure
no-op — `twin/src/world/mutation-log.ts applyReport`). **A flag is therefore restart-idempotent BY CONSTRUCTION**;
asserting only a flag proves nothing about the op. Every restart assertion here is paired with an independent /v2
observable: `PurchaseShip`/`navigate` mutationLog entries, ship identity/counts, `agent.credits`, `nav.route.arrival`,
or the `spacetraders_daemon_bootstrap_phase` gauge.

---

## 1. Audit verdict — the 3 existing restart specs

### `tests/data/restart-idempotency.e2e.test.ts` → **HARDENED IN PLACE**
- **Was sound on:** (b) no double probe-buy (`PurchaseShip==2` across both lifetimes) + (d) convergence (3 SATELLITE).
- **Gap at the bar:** (a) never asserted phase re-detection; (b) treasury never checked (a re-buy would still leave 2 SATELLITE if one was lost, but credits would betray it).
- **Teeth added:** post-restart `bootstrap_phase{DATA}==1` (a); `agent.credits === 175000 - 2*40000` i.e. **95000 exact** (b) — an independent /v2 observable that a re-buy would drop to 55000. Matches the `data/golden-path` treasury assertion.

### `tests/income/income-restart-idempotency.e2e.test.ts` → **HARDENED IN PLACE**
- **Was sound on:** (b) `PurchaseShip==3` (mid-flight hauler not re-bought), `fleet-unassign<=1` (no re-retire), `batch-contract<=1` (no re-launch), `frigateContractTagged==false`; (d) convergence (3 parked).
- **Gap at the bar:** (a) no phase assertion; (c) never tracks the SPECIFIC mid-flight hull (counts only).
- **Teeth added:** post-restart `bootstrap_phase{INCOME}==1` (a). Explicit (c) is delivered by the NEW `income-restart-mid-transit` spec (below), keeping this spec as the mid-PURCHASE case.

### `tests/gate/gate-restart-idempotency.e2e.test.ts` → **HARDENED IN PLACE**
- **Was sound on:** (b) `construction-start==1`, `launch-autosizer==1`, `executor-bounce<=1`, and the recently-added independent `PurchaseShip<=2` (no double worker-buy, paired with the report-seam `gateWorkers`); (d) `done`.
- **Gap at the bar:** (a) — the headline GATE property "sticky once construction started" was proven only by `gate-sticky` WITHOUT a restart. This spec drove to `done` but never asserted the phase held GATE ACROSS the reboot.
- **Teeth added:** after the reboot and before forcing completion — re-derive `construction.started` then `bootstrap_phase{GATE}==1 && {INCOME}==0`. First assertion that sticky-GATE **survives a reboot** (re-derived from persisted construction state, not daemon memory).

---

## 2. New spec files (authored-NOT-run)

### `tests/income/income-restart-mid-transit.e2e.test.ts` — class 1, bar (c)  [EXPECTED: GREEN, RED-if-gap-#1]
- **GWT:** GIVEN a hauler bought + dispatched and still EN ROUTE; WHEN the daemon is killed mid-flight and a new one boots on the same DB+twin; THEN the new daemon re-adopts that exact hull — no re-buy, no re-dispatch (same `nav.route.arrival`), arrival acted on, hull parks, INCOME converges.
- **Teeth (independent observables):** captures the first hull by IDENTITY; `PurchaseShip==3` (not re-bought); hull present exactly once + `parkedHub` set post-reboot; **when the leg is a real topology transit**, `nav.route.arrival` is UNCHANGED across the reboot (adopted, not superseded by a fresh navigate that would re-mint a later arrival); `phase{INCOME}==1`; 3 distinct hubs.
- **Mechanism:** dials the live compression lever to 6x (a >=15s real leg → ~2.5-7s wall, catchable; 200x = ~50-300ms, not) to land the kill mid-flight, restores 200x for fast convergence, restores it again in `finally` (compression is sticky across `/reset`).
- **Expected GREEN** if the coordinator re-arms arrival detection from `GET /my/ships` (DB/twin) for hulls it did not dispatch this lifetime. **RED (the gap this exists to expose)** if arrival timers are armed only for same-lifetime navigations → convergence stalls, or `route.arrival` moves, or `PurchaseShip>3`. See gap #1.

### `tests/income/income-restart-batch-contract.e2e.test.ts` — class 2, bar (b)  [EXPECTED: GREEN]
- **GWT:** GIVEN the batch-contract fleet coordinator has been launched; WHEN the daemon is killed after the launch and a new one boots; THEN the coordinator is NOT relaunched, the treasury is not double-charged, no spurious fleet churn fires, INCOME holds.
- **Teeth:** `batch-contract==1` ACROSS the reboot (exactly-once) — PAIRED with independent observables: `PurchaseShip` count stable (no spurious buys from a phase-thrash), `agent.credits` never regresses below the launch snapshot (contract income only ADDS; the twin's ONE-active guard blocks a double-accept), `fleet-unassign<=1`, `phase{INCOME}==1`.
- **Expected GREEN** — a reboot cannot manufacture a second launch (exactly-once flag) or a double payment (one-active guard, `world/contracts.ts negotiate → 4511`).
- **Deliberately at the ACHIEVABLE bar** — see gap #2: contract accept/deliver/fulfill are invisible to the harness, so "no re-accept / no double-deliver / fulfillment-exactly-once / treasury-EXACT" cannot be asserted yet, only launch-idempotence + treasury-NON-REGRESSION. Documented inline in the spec.

### `tests/gate/gate-restart-double.e2e.test.ts` — class 4, bar (e)  [EXPECTED: GREEN, RED-if-gap-#1/#5]
- **GWT:** GIVEN GATE construction started + first worker sized; WHEN the daemon is killed and rebooted TWICE back-to-back; THEN nothing changes beyond a single run — sticky GATE across BOTH reboots, every exactly-once op stays once, no worker re-bought, construction completes.
- **Teeth:** sticky `phase{GATE}==1 && {INCOME}==0` re-asserted after EACH of the two reboots; `construction-start==1`, `launch-autosizer<=1`, `executor-bounce<=1` across both; `PurchaseShip<=2` (the `haulers:2/chains:4` fixture's exact 2-bought delta — a real /v2 no-double-buy, not a flag); `done`.
- **Placed in GATE** for the densest exactly-once guard set. Incidentally exercises (c) if workers/supply are in flight to the real `I67` site across the reboots (see gap #4).
- **Expected GREEN**; **RED** if a second rapid reboot thrashes the phase or re-fires a guard (i.e. stickiness/idempotence held in memory, not re-derived from DB+twin each boot).

---

## 3. Coverage map — bar (a)-(e) × phase

| Bar | DATA | INCOME | GATE |
|-----|------|--------|------|
| **(a)** phase re-detect | data/restart-idempotency `phase{DATA}==1` *(hardened)* | income/restart-idempotency `phase{INCOME}==1` *(hardened)*; income/restart-mid-transit; income/restart-batch-contract | gate/restart-idempotency sticky `phase{GATE}==1 && {INCOME}==0` *(hardened)*; gate/restart-double (after BOTH reboots) |
| **(b)** no dup side-effects | data/restart-idempotency `PurchaseShip==2` + `credits==95000` | income/restart-idempotency `PurchaseShip==3`, `fleet-unassign<=1`, `batch-contract<=1`; income/restart-batch-contract `batch-contract==1` + credits-non-regress; income/restart-mid-transit hull-not-duplicated | gate/restart-idempotency `construction-start==1`,`autosizer==1`,`executor-bounce<=1`,`PurchaseShip<=2`; gate/restart-double (same, across 2 reboots) |
| **(c)** in-flight survives | **JUSTIFIED SKIP** — probes are bought at HQ with no travel (data/golden-path: "at HQ (no travel)"); DATA has no in-flight leg to orphan | income/restart-mid-transit **(PRIMARY)** — specific hull IN_TRANSIT→adopted+parked, conditional `route.arrival` adoption | gate/restart-double (INCIDENTAL — supply/workers to real `I67` may be in flight across reboots; convergence proves re-adoption). Dedicated gate-worker mid-transit deferred → gap #4 |
| **(d)** converges | data/restart-idempotency (3 SATELLITE; `phase{DATA}` holds) | income/restart-idempotency (3 parked); income/restart-mid-transit (3 distinct hubs + batch); income/restart-batch-contract (batch holds) | gate/restart-idempotency (`done`); gate/restart-double (`done`) |
| **(e)** double/rapid restart | *skip* | *skip* | gate/restart-double **(PRIMARY)** |

**Justified skips:**
- **(c) DATA** — no in-flight leg exists in DATA (probes bought at HQ; scouting abstracted via the coverage lever). Nothing to re-adopt.
- **(e) DATA + INCOME** — one strong double-restart spec in GATE (densest guard set) covers (e); DATA/INCOME variants would be strictly lower-value guard density.
- **(c) dedicated GATE-worker mid-transit** — folded into gate-restart-double (incidental) + income-restart-mid-transit (explicit, same daemon arrival-rearm mechanism), pending gap #4 confirmation that gate workers issue real navigate legs.

**Totals:** 8 restart scenarios (3 hardened-in-place + 3 new + the pre-existing batch/golden siblings referenced for convergence). Restart-specific: 3 hardened + 3 new = 6 focused scenarios (within the 4-8 budget), each with before/after deltas across the reboot — not absence-of-crash.

---

## 4. Implementation-gap list (wave-2/3) — REPORTED, not implemented

1. **[COORDINATOR — SUSPECTED HOLE · HIGH] Arrival-timer re-arm on restart.**
   The daemon detects arrival via an in-memory `time.AfterFunc(time.Until(arrival))` (twin `clock.ts` header; gobot `ship_state_scheduler`). A reboot loses those timers; the new daemon must re-arm from `GET /my/ships` `route.arrival` (twin) / its DB for hulls it did NOT dispatch this lifetime. If it arms timers only for same-lifetime navigations, a hull IN_TRANSIT at the kill is orphaned.
   **Exposed by:** `income-restart-mid-transit` (convergence stall / `route.arrival` re-mint / `PurchaseShip>3`). Highest-priority suspected hole.

2. **[TWIN OBSERVABILITY — GAP · MEDIUM] Contract ops are invisible.**
   `accept`/`deliver`/`fulfill` mutate `world.contracts` + `credits` but are absent from BOTH the mutationLog AND `GET /_twin/state`. The harness cannot assert no-re-accept / no-double-deliver / fulfillment-exactly-once / treasury-EXACT.
   **Fix (twin — not this task's scope):** add a `contracts` view to `/_twin/state` (`activeContractId`, `accepted`, `fulfilled`, per-line `unitsFulfilled`) and/or log `AcceptContract`/`DeliverContract`/`FulfillContract` to the mutationLog. Then upgrade `income-restart-batch-contract` from launch-idempotence to the full class-2 bar.

3. **[TWIN OBSERVABILITY — GAP · MEDIUM] Construction supply is invisible.**
   `construction.percent` is a test LEVER (`POST /_twin/construction`); worker material deliveries (`SupplyConstruction`) are not twin-observable, so "deliveries monotonic / no over-supply / progress derived-not-forced" cannot be asserted across a GATE restart.
   **Fix (twin):** track `world.construction.suppliedUnits` (append per supply call) and expose it in `/state`. Then a GATE restart can assert supply-monotonic across the reboot.

4. **[COORDINATOR/TWIN — INVESTIGATE · MEDIUM] In-flight leg reality.**
   Whether bought gate workers (and income haulers to hubs) issue real `/v2` navigate legs between CAPTURED waypoints (catchable IN_TRANSIT carrying `nav.route`) vs teleporting to LOGICAL symbols (the navigate route mints no transit when a hub symbol is outside the topology) is unconfirmed. This determines whether `income-restart-mid-transit`'s adopt-not-redispatch sub-assertion bites, and whether a dedicated gate-worker mid-transit spec is worth adding.
   **Investigate:** seed hubs as real waypoints, or route haulers/workers through a real staging waypoint; then the mid-transit adoption teeth become unconditional.

5. **[COORDINATOR — FIRST-TESTED-HERE · likely OK] Sticky-phase persistence across a reboot.**
   Sticky-on-construction-started must be re-derived from the persisted `construction.started` on EVERY boot (twin re-observe), not held in daemon memory.
   **Exposed by:** hardened `gate-restart-idempotency` + `gate-restart-double`. Expected OK (the daemon re-syncs from the API on boot) but this is the first assertion of it under a restart.

6. **[HARNESS — FIXED THIS TASK] TwinShip type + compression lever.**
   `TwinShip.nav` omitted `route` though `GET /_twin/state` serves the resolveNav'd route — extended it so mid-transit adoption is assertable. Added `twin-admin.setCompression(factor)` (the live `POST /_twin/time-compression` lever) to deterministically widen the IN_TRANSIT window. Both additive; harness `tsc --noEmit` stays 0-error, unit tests 11/11.

---

## Provenance

- **branch:** `worktree-agent-ad0c8eb242d200038` (base for deltas: `feat/twin-digital-twin` @ 59c5f326)
- **worktree:** `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/agent-ad0c8eb242d200038`
- Commit SHA / diffstat: recorded in the commit that carries this report (see the task's final report block).

### Files changed (all under `bootstrap-harness/**`)
- `tests/helpers/twin-admin.ts` — extend `TwinShip.nav.route`; add `setCompression` lever.
- `tests/data/restart-idempotency.e2e.test.ts` — hardened (a)+(b).
- `tests/income/income-restart-idempotency.e2e.test.ts` — hardened (a).
- `tests/gate/gate-restart-idempotency.e2e.test.ts` — hardened (a) sticky-across-restart.
- `tests/income/income-restart-mid-transit.e2e.test.ts` — NEW (class 1, bar c).
- `tests/income/income-restart-batch-contract.e2e.test.ts` — NEW (class 2, bar b).
- `tests/gate/gate-restart-double.e2e.test.ts` — NEW (class 4, bar e).
