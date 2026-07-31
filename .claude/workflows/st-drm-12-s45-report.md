# st-drm.12 (remainder) — S4/S5 honest reshape

**Scope**: fix the ARRANGE/premise of two live-stack scenarios in
`twin/tests/acceptance/ship-actions.e2e.test.ts`. No other files touched.
**Authored-not-run**: the orchestrator runs the file live after merge; here it was
validated statically (`tsc`) and against the in-process suite only.

## Why (live evidence — the system is correct, the premises raced it)

- **S4 (refuel)**: `navigate A1→F55` correctly burns 50 fuel at departure, but the daemon
  coordinator **auto-refuels on arrival**. By the time the test observes the ship at F55 the
  tank is already 400/400 and credits already dropped by the top-up. The old drained-tank
  precheck (`fuelBefore < capacity` via `ship refresh`) is therefore unreachable — the
  coordinator's own fuel management (desired bootstrapper behaviour, and true on the real API)
  defeats a manual drained-tank arrange every time.
- **S5 (purchase)**: the new-symbol predicate's **baseline** was the daemon's LOCAL roster,
  which on a fresh test DB is EMPTY. The buyer's own row (`TWINAGENT-2`), upserted by the
  purchase container's first sync at ~+100ms, counted as "new", so the credit assertion read
  PRE-debit credits (the twin-side `PurchaseShip` lands ~+2s).

Correct twin behaviour is regression-locked in-process and cited in the reshaped scenarios:
- `tests/unit/navigate-fuel-persistence.test.ts` — departure burn 350/400 survives arrival-settle;
  a full-tank refuel is a free 0-unit no-op (0 units cost 0).
- `tests/unit/purchase-ship-type-mapping.test.ts` — a bought `SHIP_PROBE` is a real `FRAME_PROBE`
  DOCKED at the yard with the exact 24,680 debit.

## S4 reshape — "a voyage drains the tank and the coordinator restores it (and pays)"

- **Given** the frigate DOCKED at A1 with a FULL tank; `readCredits()` **before** the voyage
  (the pre-voyage baseline the paid top-up must drop below).
- **When** it navigates A1→F55 (dispatch exit 0).
- **Then**, in a bounded poll: (a) it arrives at F55 — `location` flips to F55 only on arrival
  (`resolveNav` keeps it at the origin while IN_TRANSIT, `clock.ts:209-221`), teeth: moved off
  origin; (b) `fuelCurrent === fuelCapacity` at F55 — the coordinator's restore is observable;
  (c) credits are **strictly below** the pre-voyage baseline — the top-up was paid via the
  coordinator's real path (the twin's refuel is atomic fuel+debit, `ships.ts:338-339`, so once
  the tank reads full the debit has already landed and the read cannot race ahead of it).
- **And** a manual `ship refuel` on the now-full tank is a genuine **0-unit no-op**. The twin
  requires DOCKED to refuel (`ships.ts:329`, `ERR_SHIP_NOT_DOCKED`), so we DOCK first — that makes
  the top-up genuinely EXECUTE against a full tank (missing 0 → 0 units → 0 cost, `ships.ts:334-339`)
  rather than fail for being in orbit. Re-reading credits **unchanged** locks the twin's
  0-units-costs-0 invariant end-to-end.
- The transient drained tank is honestly documented as unobservable by design; the drain itself
  is proven in-process. `describe`/`it` titles renamed truthfully (the pilot no longer refuels
  manually — the coordinator does).

Observables preserved with before/after deltas: **arrival** (location HOME→F55), **restore**
(tank full at F55), **payment** (credits strictly down vs pre-voyage baseline), **0-cost no-op**
(credits unchanged across the manual refuel). Nothing weakened.

## S5 reshape — fix the baseline, keep every observable

- **Given** PRIME the daemon roster: `refreshShip(FRIGATE)` + `refreshShip(PROBE_BUYER)` so
  `listFleet()` reports the real 2-hull starting fleet; arrange sanity asserts
  `beforeSymbols ⊇ {FRIGATE, PROBE_BUYER}`; `readCredits()` before the purchase.
- **Then** the new-symbol predicate is kept, plus an explicit `STARTERS` exclusion (defence in
  depth): a candidate hull must be ∉ `beforeSymbols` AND ∉ `{FRIGATE, PROBE_BUYER}`. With the
  primed baseline the first genuinely-new symbol can only be the purchased hull, which exists only
  after the twin-side debit.
- All existing teeth kept **exactly**: roster grows by exactly one (`before.length + 1`),
  exactly-one new hull, credits strictly drop (asserted AFTER new-hull detection), new hull
  reconciles at YARD via `pollShip`.

## Verification

- `tsc --noEmit`: **35** total errors == the pre-existing fastify-inject baseline; **ZERO**
  reference the edited file → zero NEW errors introduced.
- In-process suite (`vitest run --config vitest.unit.config.ts`): **33 files / 224 tests passed,
  exit 0**. (The edited file is a `*.e2e.test.ts`, excluded from this config; run purely as a
  no-regression gate.)
- Did NOT run any `*.e2e.test.ts` / the default (live) vitest config, and did NOT boot any
  daemon — those live singletons are the orchestrator's to drive.

## Mandate compliance

- **Hexagonal boundary (CM-A)**: tests enter through the CLI driving port (`runCli`) and observe
  via the daemon read-back path (`refreshShip` / `listFleet` / `player info`); no twin internals
  are invoked.
- **Observable behaviour**: every `Then` asserts an observable delta (location, fuel, roster,
  credits); no internal-state assertions, and dispatch `exitCode === 0` is used only as a
  container-started sanity, never as proof of effect.
- **Business language (CM-B)**: user-goal framing ("a voyage drains … the coordinator restores it
  and pays"; "a captain expands the fleet by purchasing a new hull").

## Files changed

`twin/tests/acceptance/ship-actions.e2e.test.ts` — S4 body (Given/When/Then/And + titles), S5
arrange (baseline priming) + predicate (STARTERS exclusion), footer RED-notes 4 & 5, and one
header teeth-summary clause. Plus this report.
