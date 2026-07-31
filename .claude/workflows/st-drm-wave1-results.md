# st-drm Wave-1 results (orchestrator-collected, 2026-07-12)

Merged into `feat/twin-digital-twin` @ `29f73ae3` (merge commits 60add86b / ad9d8347 / 1e0657a7 / fa0c5969 + wiring f77f8bfa + comment fix 29f73ae3).
Post-merge gates: twin in-process unit suite **217/217 green (29 files)**; `go build ./...` green; drift-seam go tests green; `gobot/bin/spacetraders{,-daemon}` rebuilt.

## st-drm.8 time-mock (CLOSED)

| Knob | Env (default) | Site |
|---|---|---|
| Compression (inverse travel, live-settable) | `TWIN_TIME_COMPRESSION` (**20**; alias `TWIN_ARRIVAL_COMPRESSION`) | `twin/src/clock.ts` (`compressedMs` reads `getCompression()` live) |
| Travel floor | `TWIN_MIN_TRAVEL_MS` (**1000**) | `twin/src/clock.ts` |
| Daemon arrival/cooldown clamp | `ST_CLOCK_DRIFT_BUFFER_MS` (**1000** = prod byte-identical; test stacks **50**) | `gobot/internal/adapters/grpc/ship_state_scheduler.go` |
| Live lever | `POST /_twin/time-compression {compression:N}` | `twin/src/routes/admin.ts` |

- INVARIANT (doc'd in code): `TWIN_MIN_TRAVEL_MS >= ST_CLOCK_DRIFT_BUFFER_MS`, else the daemon can miss arrivals.
- Compression **1 = true real-API timing** (fidelity mode).
- Default compression stays **20** — twin e2e specs + DATA harness budgets are tuned to ~20×.
- Wired: twin test stack (clamp 50 in `twin/tests/helpers/daemon.ts` + `global-setup.ts`) and `bootstrap-harness/tests/helpers/daemon.ts` (clamp **50**, commit f77f8bfa). Twin-side knobs for harness runs go on the twin process env at launch (orchestrator launches the twin) or via the live admin route. Fast-run reco: compression 200, floor 50, clamp 50.

## st-drm.9 openapi construction (CLOSED)

`twin/tests/openapi/shape.test.ts` 24→**27** cases: construction GET (200) + supply (201) + has-teeth `isComplete`-deletion negative control. **No twin fix needed** (serializeConstruction already spec-conformant). Sweep verified to cover **all 23 bootstrapper endpoints** — no gaps.

## st-drm.10 harness-oracle audit (CLOSED, caveat)

Fixed (f0b0f3c4):
1. **Unit gate was RED**: `tests/unit/fixtures.test.ts` still pinned the OLD `X1-PZ28-I57` gateSite after the fixtures-gate fix — repaired to I67; gate restored always-green.
2. `gate-worker-sizing` tautology (`bought === length − repurposed` can't fail) → concrete expected delta.
3. `gate-restart-idempotency` claimed "no double-worker-buy" but never asserted it → now asserts the **/v2 PurchaseShip count** (independent observable) alongside the report-seam flag.

World truth verified vs `twin/fixtures/era2-X1-PZ28/`: **I67 is the unique JUMP_GATE** (I57 nonexistent; only a clarifying comment remains); **H1..H5 are logical hub names, not waypoints**; prices PROBE 24680 / FRIGATE 150000 / LIGHT_HAULER 300000 match harness income/gate fixtures; DATA `probePrice: 40000` is a deliberate boundary-clean choice with a teeth-bearing credit assertion. `parse-metrics` returns **null** on an absent metric (equality assertions safe; avoid null-tolerant comparisons).

CAVEAT: the agent died before emitting its 25-row verdict table (narration recovered from transcript; fixes above are the complete found-set). Deferred to wave-2: classify `parkedHub` (computed twin-internally — is hauler hub-parking asserted end-to-end on something real?). Per-spec re-scrutiny continues in the Wave-3 red-loop + st-drm.14.

## st-drm.11 CLI acceptance (CLOSED)

JOB 1 (theatre fixes, 668f947d — orchestrator salvage commit; agent verified 207 in-process green + 0 new tsc errors, died pre-commit):
- `construction.test.ts` over-carry pinned to **4218** (empirically validated 9/9 green); stale "everything RED" header fixed (routes ARE wired).
- `cargo-trade.e2e` oversell → asserts deterministic `insufficient cargo` **stderr** (CLI short-circuits pre-twin; numeric code not observable at CLI boundary — documented inline).
- `tests/ships/cargo.test.ts` exit-0 theatre → honest wire-decode smoke pointing at cargo-trade.e2e.

JOB 2: **era-guard error path** added to `register.test.ts` (second `register --new` rejected with "OPEN era already exists", no player/era rows written, twin untouched) — the ONLY uncovered twin-reaching command variant.

Coverage map (excluded with reasons): `ship route` / `system gates` = cross-system, infeasible on the single-system fixture (wave-2 class); `market get/list`, `contract list/get/demand` = local-DB reads, no twin call; plain `player register` (no `--new`) = local persistence; `ship info` = daemon cache (twin-reaching counterpart `ship refresh` covered by ships/show.test.ts).

## KNOWN LIVE-STACK REDS → wave-2 (st-drm.12)

1. `twin/tests/acceptance/ship-actions.e2e.test.ts` **refuel**: drained-tank precheck at a remote waypoint (dock timing) — st-drm.8's time-mock may have resolved this; RE-CONFIRM live before fixing anything.
2. `ship-actions.e2e` **purchase**: credit-drop assertion — daemon `player info` credit read-back path (gobot side, suspected daemon-local player cache not resyncing after twin-side purchase).
3. Any authored-RED from the era-guard spec (expected GREEN — guard short-circuits locally; confirm).

Note: `server.ts` ship-action scaffold comments were stale and have been fixed (29f73ae3) — navigate/orbit/dock/refuel/PATCH-nav/purchase all live in `routes/ships.ts` via `shipRoutes(v2)`.
