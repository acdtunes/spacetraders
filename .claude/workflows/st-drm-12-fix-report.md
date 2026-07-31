# st-drm.12 remainder — twin digital-twin defect fixes (report)

Agent: nw-software-crafter (Crafty). Outside-In TDD, in-process (Fastify `inject`) only.
Branch: `worktree-agent-a4cc217d9dac9a01b` (fast-forwarded onto `feat/twin-digital-twin` @ b6cd9606).

## Result summary

| # | Defect | Verdict | Fix location |
|---|--------|---------|--------------|
| A | Purchase type-mapping bug | REAL src defect — FIXED | `twin/src/routes/ships.ts` |
| B | Navigate fuel burn "does not persist" | NOT a src defect — regression added + explained | `twin/src/routes/ships.ts` (comment) + new test |
| C | `/_twin/state` agent missing `shipCount` | REAL src defect — FIXED | `twin/src/routes/admin.ts` |
| D | Stale shipyard golden | Test golden refresh (not a twin defect) | `twin/tests/endpoints/shipyard.test.ts` + golden helper |
| E | `pollFleet` predicate race | Test hardening (not a twin defect) | `twin/tests/acceptance/ship-actions.e2e.test.ts` |

Verification (final):
- In-process suite: **224 passed / 33 files** (`vitest run --config vitest.unit.config.ts`). Baseline was 219; +5 new regressions.
- `tsc --noEmit`: **35 errors, ZERO new vs the pre-edit baseline** (all 35 are the pre-existing fastify-`inject` `void & Promise<Response> & Chain` overload quirk in test files). New test files mirror the tsc-clean inject idiom.
- No `.beads/issues.jsonl` / `package-lock.json` staged. All twin work committed.

## A — Purchase type-mapping bug (REAL defect, FIXED)

Root cause: `buildPurchasedShip` (ships.ts) cloned `[...world.ships.values()][0]` — the seeded COMMAND frigate — and only overrode role/nav/engine.speed/fuel.current/cargo. So a bought `SHIP_PROBE` kept `FRAME_FRIGATE`, `fuelCapacity 400`, frigate reactor/crew/modules, despite the correct 24,680 debit.

Fix: `buildPurchasedShip` now looks up the requested type's `ShipyardShip` listing (`listingForShipType`, scans yards — every X1-PZ28 yard carries identical per-type specs) and builds the hull from it: frame/reactor/engine/modules/mounts from the listing, tank sized to `frame.fuelCapacity` (full on purchase), hold summed from cargo-hold modules (`cargoCapacityOf`), crew from the listing (0 when absent). A type no yard lists (legacy un-prefixed hermetic callers e.g. `"LIGHT_HAULER"`) still falls back to the template clone; a minimal hull only when the world is empty. Both the 201 response ship and the stored world ship are now the correct per-type hull.

Regression `twin/tests/unit/purchase-ship-type-mapping.test.ts`: buys `SHIP_PROBE` and `SHIP_LIGHT_HAULER` at X1-PZ28-A2, re-GETs each and asserts distinct frame/fuel/engine (probe → FRAME_PROBE, cap 0, speed 3; hauler → FRAME_LIGHT_FREIGHTER, cap 400, speed 30) — proving per-type mapping, not a probe special-case — for BOTH the response and the stored ship. RED (`FRAME_FRIGATE`) → GREEN. The OpenAPI conformance sweep (shape.test.ts, purchases a probe) stays green, confirming the real probe (fuel 0, crew 0) still conforms to the vendored spec.

## B — Navigate fuel burn "does not persist" (NOT a src defect; regression added + −1 credit explained)

Investigation (empirical, in-process): the navigate handler already mutates the **stored** ship — `world.ships` is a plain `Map` returning the canonical reference, and `ship.fuel = { ...ship.fuel, current: current - consumed }` reduces it at departure. `settleArrival` commits **nav only** and never touches fuel; `resolveNav`/`serializeShip` preserve fuel. The only code that writes fuel-to-capacity is `refuel` and `buildPurchasedShip` — neither is triggered by navigate/arrival/settle.

Added the assertion the suite genuinely lacked (`twin/tests/unit/navigate-fuel-persistence.test.ts`): navigate A1→F55 (CRUISE, burn 50), poll the REAL wall clock **past the compressed arrival**, run the arrival-settle path (`POST /orbit` → `settleArrival`), then re-GET and assert `fuel.current === 350` and IN_ORBIT@F55. It is **GREEN on the current code** — the burn is durable and survives arrival settle.

Conclusion: the live "GET shows 400/400 after a voyage" is **not a lost burn**. It is the daemon's post-arrival auto-refuel restoring the tank. This also explains the observed **−1 credit with only a `navigate` log entry**: refuel is not mutation-logged, and topping up the 50 burned fuel buys exactly ONE market unit (100 ship-fuel) at ~1 cr/unit → −1 credit. A second regression locks that the twin's refuel charges **0 credits for 0 units** (full tank ⇒ `units:0, totalPrice:0, credits unchanged`), so a −1 can only ever be a real >0-fuel top-up, never a phantom 0-unit charge. A refuel-route comment records the invariant. No behavioral src change was warranted (TDD: the requested regression is green), so none was invented.

## C — `/_twin/state` agent missing `shipCount` (REAL defect, FIXED)

Root cause: `GET /_twin/state` returned the raw stored `Agent` (5 fields), while `GET /my/agent` returned `serializeAgent` (adds the spec-required `shipCount`). The live `tests/agent.test.ts` asserts the two deep-equal and failed.

Fix (`twin/src/routes/admin.ts`): the `/state` BASE view now projects `agent` through `serializeAgent(w)` (guarded for the cold `null` agent); added the `AgentView = Agent & { shipCount }` type for `TwinStateBase.agent`. Existing in-process readers (`admin-state-clock`, `register-route`) use `toMatchObject`/subset and stay green; the cold-start `agent:null` case is preserved.

Regression `twin/tests/unit/agent-state-parity.test.ts`: injects both endpoints and asserts `GET /my/agent .data` `toEqual` `/_twin/state .agent` (and `shipCount === 2`) — the live assertion, replicated in-process and locked. RED (`shipCount undefined`) → GREEN.

## D — Stale shipyard golden (test golden refresh)

`tests/endpoints/shipyard.test.ts` (live/CLI) strict-compared `GET .../shipyard` against the RAW reduced fixture, but the twin correctly enriches each listing to the full spec `ShipyardShip` (adds `symbol`/`supply`/`crew {required,capacity}` and `condition`/`integrity`/`quality`/`description` on frame/reactor/engine). The golden predated the enrichment, so the `toEqual` failed.

Refresh (NOT a weakening — still strict `toEqual`): added `twin/tests/helpers/shipyard-golden.ts` — a hand-written, spec-derived golden that never imports the production serializer. Verified byte-for-byte against the real endpoint output in-process by strengthening `tests/unit/shipyard-route.test.ts` from `toMatchObject` to strict `toEqual(expectedShipyardResponse(shipyard))` (GREEN — proves the golden equals what the endpoint emits). The live endpoints test now diffs against the same golden. The independent proof that the shape conforms to the vendored spec remains the OpenAPI conformance sweep (`shape.test.ts` "HAS TEETH").

Note: I cannot run the live/CLI `tests/endpoints/shipyard.test.ts` (no daemon/live stack per constraints). Its golden is verified transitively by the in-process `shipyard-route.test.ts`, which drives the identical endpoint via `buildServer().inject` and asserts the identical golden.

## E — `pollFleet` predicate race (test hardening)

`tests/acceptance/ship-actions.e2e.test.ts` Scenario 5 polled `rows.length > before.length`. A daemon sync-artifact row (the buyer upserting its own row into the daemon DB at ~+200ms) satisfies a bare length check long before the twin-side `PurchaseShip` at ~+2s, so the subsequent credit assertion read pre-debit credits and failed intermittently.

Fix (minimal): predicate now fires only when a symbol **not in `beforeSymbols`** appears — `rows.some((r) => !beforeSymbols.includes(String(r.symbol)))` — so it waits for the actual purchased hull. Every subsequent assertion (credits drop, exactly-one new hull, reconciled-at-yard) is unchanged. File is `*.e2e.test.ts` (excluded from the in-process config); typechecks clean (no new tsc errors). Cannot be executed here (live stack).

## Quality gates

- 100% green in-process suite (224/224); no test skips introduced; new tests are behavior-first through driving ports (HTTP inject), asserting observable outcomes; no mocks inside the hexagon.
- No existing assertion weakened. D strengthens (`toMatchObject`→`toEqual`); B/C add locking regressions; A fixes src under a RED→GREEN test.
- tsc: zero new errors vs baseline.

## Commits (feat/twin-digital-twin..HEAD)

```
8185a501 test(twin): fix pollFleet predicate race in ship-actions Scenario 5
f8e8b0fc test(twin): refresh stale shipyard golden to the spec-complete ShipyardShip shape
bff7bc7c fix(twin): /_twin/state agent projection includes shipCount (parity with /my/agent)
c8836edf test(twin): lock navigate burn durability through arrival settle + free 0-unit refuel
e144ed98 fix(twin): purchase builds hull from the requested type's shipyard listing
```

Diffstat: 9 files, 459 insertions(+), 28 deletions(-) — all under `twin/`.
