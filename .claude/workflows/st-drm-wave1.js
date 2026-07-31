// st-drm WAVE 1 — authored 2026-07-12. Referenced by handoff bead st-drm.13. DO NOT edit meta casually.
//
// LAUNCH (next orchestrator session):
//   - cwd MUST be inside the feat/twin-digital-twin worktree
//     (/Users/andres.dandrea/IdeaProjects/cities/captain-worktrees/twin-digital-twin) so the four
//     isolated agent worktrees branch from the rebased twin+harness state.
//   - Branch must be rebased on main first (done 2026-07-12; re-check: git merge-base --is-ancestor main HEAD).
//   - Do NOT launch while any live twin/daemon test run is active (:8080, /tmp/spacetraders-daemon-test.sock,
//     Postgres :5434 are GLOBAL singletons — worktree isolation does not isolate them).
//   - Workflow({ scriptPath: '<abs path to this file>' })
//
// MERGE PROTOCOL (orchestrator, after the wave returns — agents CANNOT do this):
//   Each agent commits in ITS OWN isolated worktree and reports branch + worktree path + SHAs + diffstat.
//   1. Verify each reported diffstat is NON-EMPTY (an uncommitted worktree merges as EMPTY — known trap).
//   2. Merge sequentially into feat/twin-digital-twin, diffstat-verify AFTER EACH merge:
//        A (time-mock: twin/src + gobot) -> B (twin/tests/openapi) -> D (twin/tests) -> C (bootstrap-harness).
//      git merge --no-ff <agent-branch>. Paths are segregated by design; on any conflict prefer combining
//      (changes are additive). D and A may both touch twin/tests/helpers — reconcile by hand if so.
//   3. Post-merge gate: cd twin && ./node_modules/.bin/vitest run --config vitest.unit.config.ts (green);
//      cd gobot && go build ./... ; rebuild bin/spacetraders + bin/spacetraders-daemon.
//   4. Wire bootstrap-harness/tests/helpers/daemon.ts env with the knobs track A reports (harness now lives
//      in-branch; one commit).
//   5. git push origin feat/twin-digital-twin; update beads st-drm.8/.9/.10/.11 (close or wave-2 notes);
//      remove the merged agent worktrees/branches.
//   Then Wave 2 (st-drm.12 crafter) and Wave 3 (st-drm.6 harness INCOME->GATE->COMPLETE) per st-drm.13 —
//   live-stack + harness runs stay in the ORCHESTRATOR loop (singleton ports; slow runs stall subagents).
export const meta = {
  name: 'st-drm-wave1',
  description: 'Epic st-drm Wave 1 in 4 isolated worktrees: time-mock compression+clamp seam (st-drm.8, opus), OpenAPI construction shape cases (st-drm.9, sonnet), bootstrap-harness theatre audit+fix (st-drm.10, opus), low-level CLI acceptance audit+write-missing (st-drm.11, opus)',
  phases: [
    { title: 'time-mock', detail: 'st-drm.8 - twin compression rework + gobot clamp seam', model: 'opus' },
    { title: 'openapi', detail: 'st-drm.9 - construction shape cases into the has-teeth sweep', model: 'sonnet' },
    { title: 'harness-audit', detail: 'st-drm.10 - theatre audit of the 25-spec oracle + test-side fixes', model: 'opus' },
    { title: 'cli-acceptance', detail: 'st-drm.11 - audit low-level tests + write missing CLI acceptance', model: 'opus' },
  ],
}

const ISO = [
  'ISOLATION: you run in YOUR OWN fresh git worktree of the spacetraders repo, branched from the rebased',
  'feat/twin-digital-twin (twin/ + gobot/ + bootstrap-harness/ all present and current). Work ONLY inside',
  'your current working directory using RELATIVE paths. NEVER cd into other checkouts',
  '(/Users/andres.dandrea/IdeaProjects/cities/spacetraders or .../captain-worktrees/twin-digital-twin) and',
  'NEVER touch beads (bd). Your worktree has NO node_modules: run "npm ci" (in twin/ or bootstrap-harness/',
  'as needed) as your first step - one Bash call, timeout 120000; retry once if it times out.',
].join('\n')

const STALL = [
  'ANTI-STALL (non-negotiable; a prior workflow burned 1.2M tokens violating these):',
  '- Every Bash call: timeout <= 120000 ms. If a command could block or exceed it, restructure/split it.',
  '- NEVER "find /" or any filesystem-wide scan; Grep/Glob scoped to your worktree only.',
  '- NEVER boot a twin or daemon (no tsx src/main.ts, no spacetraders-daemon) and NEVER run live-stack',
  '  tests: no default vitest config, no globalSetup, no *.e2e.test.ts, no bootstrap-harness e2e specs.',
  '  Ports :8080, /tmp/spacetraders-daemon-test.sock and Postgres :5434 are GLOBAL - your worktree does',
  '  not isolate them. In-process only: ./node_modules/.bin/vitest run --config vitest.unit.config.ts <file>',
  '  (from twin/). bootstrap-harness/tests/unit (pure, no stack) is also allowed.',
].join('\n')

const GITP = [
  'GIT PROTOCOL: COMMIT ALL your work in YOUR worktree on YOUR current branch (git rev-parse',
  '--abbrev-ref HEAD), conventional messages, always --no-verify. NEVER stage .beads/issues.jsonl,',
  'issues.jsonl, or package-lock.json (exception: a lockfile change from a dependency YOU intentionally',
  'added). An uncommitted worktree merges as EMPTY - commit everything you intend to deliver.',
  'MANDATORY final report block:',
  '  branch: <git rev-parse --abbrev-ref HEAD>',
  '  worktree: <pwd>',
  '  commits: <git log --oneline main..HEAD or the SHAs you created>',
  '  diffstat: <git diff --stat feat/twin-digital-twin...HEAD>',
].join('\n')

const P_TIMEMOCK = [
  'You implement bead st-drm.8: the twin time-mock rework + the gobot arrival-clamp seam. The goal:',
  'the UNMODIFIED daemon runs against the twin exactly as against the real API (twin-blind - its real',
  'time.AfterFunc(time.Until(arrival)) wait strategy untouched), while navigation completes fast at ANY',
  'distance via configurable time compression.',
  '', ISO, '', STALL, '',
  'CURRENT STATE (twin/src/clock.ts):',
  '- Dual clock: a frozen/steppable WORLD clock (getNow/setNow/advanceClock) stamps mutations only.',
  '- SHIP ARRIVALS are REAL wall clock: makeTransit computes realTravelSeconds =',
  '  round(round(distance) * (multiplier/engineSpeed)) + 15 (CRUISE 25 / BURN 12.5 / DRIFT 250 / STEALTH 30),',
  '  then arrival = now + compressedMs(realSeconds), compressedMs = max(1000, realMs / ARRIVAL_COMPRESSION),',
  '  env TWIN_ARRIVAL_COMPRESSION default 20.',
  '- VESTIGIAL: TWIN_TIME_COMPRESSION (default 100) + parseCompression/getCompression/setCompression +',
  '  POST /_twin/time-compression (twin/src/routes/admin.ts) - a lever that currently drives nothing.',
  '- fuelCostFor also lives in clock.ts - do not regress it.',
  'GOBOT: the daemon ship-state scheduler clamps short waits to ~1s (grep gobot/internal for',
  'ClockDriftBuffer and nearby time.Until/clamp logic to find the exact site).',
  '',
  'REQUIREMENTS (Admiral decisions - implement exactly):',
  '1. ONE compression knob in the twin, env-configurable (1x/10x/100x/...), settable LIVE via the existing',
  '   POST /_twin/time-compression admin route. Unify the two knobs (pick the cleanest name, alias or delete',
  '   the other; update the admin route + its tests). Travel time INVERSELY proportional to it. DEFAULT',
  '   MUST STAY 20 - the existing twin e2e specs and the DATA harness budgets are tuned to ~20x.',
  '2. Compression 1x = true real-API timing (fidelity mode) - must work.',
  '3. The twin floor max(1000ms, ...) becomes configurable: env TWIN_MIN_TRAVEL_MS, default 1000',
  '   (unchanged behavior when unset).',
  '4. GOBOT SEAM: an env var for the ~1s clamp (name it after the code reality, e.g.',
  '   ST_CLOCK_DRIFT_BUFFER_MS), default = the current 1s so PROD IS BYTE-IDENTICAL; test stacks will set',
  '   ~50ms (any value parseable, sane floor >=1ms). NO other daemon behavior change - a config knob',
  '   exactly like the existing ST_API_BASE_URL seam in client.go.',
  '5. INVARIANT (document in code comments + your report): in any stack, TWIN_MIN_TRAVEL_MS >= the daemon',
  '   clamp value, else the daemon can miss arrivals inside its tolerance.',
  '6. Wire the TWIN test stack only: twin/tests/helpers/daemon.ts env + twin/tests/global-setup.ts pass the',
  '   daemon clamp env (50). Keep compression 20 there. Do NOT touch bootstrap-harness/ (another track owns',
  '   it this wave) - instead RETURN an exact wiring block for bootstrap-harness/tests/helpers/daemon.ts',
  '   (env names + recommended fast-run values, e.g. compression 100-500, TWIN_MIN_TRAVEL_MS 50, clamp 50).',
  '7. TESTS: twin unit tests for the knob (1x/10x/100x inverse proportionality; floor honored; arrival always',
  '   a real-future instant; admin lever takes effect live). Reconcile existing clock unit tests minimally if',
  '   names change - the full twin unit suite (207+) must stay green:',
  '   cd twin && ./node_modules/.bin/vitest run --config vitest.unit.config.ts.',
  '   gobot: a unit test for the seam (unset -> 1000ms; 50 -> 50ms; garbage -> default). go build ./...',
  '   green; targeted go test on the touched packages (timeout 120000 each, split packages if needed);',
  '   rebuild gobot/bin/spacetraders + gobot/bin/spacetraders-daemon.',
  '', GITP, '',
  'RETURN: knob names + defaults + file:line; the clamp site you found (file:line); twin unit count;',
  'go test/build results; the harness wiring block; risks; then the mandatory report block.',
].join('\n')

const P_OPENAPI = [
  'You implement bead st-drm.9: finish the OpenAPI response-shape sweep by adding the two CONSTRUCTION',
  'endpoints - the only bootstrapper endpoints missing from twin/tests/openapi/shape.test.ts (24 cases).',
  '', ISO, '', STALL, '',
  'HOW THE SWEEP WORKS: validateResponse(method, templatePath, status, body) from',
  'twin/tests/helpers/openapi.ts validates a REAL captured response against the vendored real-API spec',
  'gobot/api/openapi.json (SpaceTraders 2.3.0). Every case drives the real endpoint in-process via',
  'buildServer().inject() and validates res.json() - NEVER a hand-authored object.',
  '',
  'ADD:',
  '1. GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/construction - read the spec for the exact',
  '   status + response schema ({data: Construction}).',
  '2. POST /systems/{systemSymbol}/waypoints/{waypointSymbol}/construction/supply - spec status + schema.',
  'SETUP: copy from twin/tests/acceptance/construction.test.ts - registerAgent FIRST, then',
  'resetWorld({mode: "gate-entry"}) (site X1-PZ28-I67; manifest FAB_MATS 4000 + ADVANCED_CIRCUITRY 1200',
  'seeded by seedGateEntry); for supply, dock a ship at the site holding some required material.',
  '3. HAS-TEETH negative control: take the REAL captured construction response, delete a spec-required',
  '   field, assert valid === false AND the error names the field (mirror the existing Ship/Contract controls).',
  '4. Cross-check the sweep covers ALL of: register, agent, ships list/get/purchase, navigate, orbit, dock,',
  '   refuel, PATCH nav, cargo purchase/sell, contract negotiate/get/accept/deliver/fulfill, waypoints',
  '   list/get, market, shipyard, construction get/supply. Report any other gap (expected: none).',
  'IF a construction response fails validation: fix the TWIN minimally (twin/src/world/serialize.ts',
  'serializeConstruction and/or twin/src/routes/construction.ts) - the twin conforms to the spec; NEVER',
  'loosen the validator or the spec.',
  'RUN: your file, then the FULL unit suite (both via --config vitest.unit.config.ts, timeout 120000) -',
  'everything stays green.',
  '', GITP, '',
  'RETURN: cases added + spec statuses used; any twin fix made; sweep-coverage result; unit-suite count;',
  'then the mandatory report block.',
].join('\n')

const P_HARNESS = [
  'You audit and harden THE ORACLE for bead st-drm.10: bootstrap-harness/ (in YOUR worktree - the branch',
  'carries it, current with main). The epic trust hierarchy is harness > bootstrapper code > design; test',
  'theatre here poisons every downstream green. You own TEST code: fix test-side findings yourself; report',
  '(do not implement) anything needing twin/gobot/coordinator changes.',
  '', ISO, '', STALL, '',
  'INVENTORY: tests/data (8 e2e), tests/income (9 e2e), tests/gate (8 e2e), tests/unit (3 pure); helpers:',
  'config.ts daemon.ts drive.ts fixtures.ts fixtures-income.ts fixtures-gate.ts scenario.ts',
  'scenario-income.ts scenario-gate.ts twin-admin.ts twin-admin-income.ts twin-admin-gate.ts',
  'mutation-log.ts parse-metrics.ts; vitest.config.ts carries retry:2 (DELIBERATE - absorbs real-travel',
  'timing flake; do not remove without strong cause, but flag anything non-timing it could mask).',
  'You CANNOT run the e2e specs (they boot the live twin+daemon singleton). You MAY run tests/unit and',
  'typecheck/parse your edits (npm ci in bootstrap-harness/ first).',
  '',
  'THEATRE BAR - for every scenario ask: would this PASS if the behavior under test were broken/no-op?',
  '1. Tautologies / assertions that cannot fail / asserting a value just written by the test itself.',
  '2. REPORT-SEAM SELF-CERTIFICATION (the harness-specific trap): these /_twin/state signals flip ONLY',
  '   because the coordinator POSTs /_twin/report about itself: frigateContractTagged->false,',
  '   batchContractRunning, construction.started, construction.adopted, autosizerRunning,',
  '   standingCoordinators.siting/.workerRebalancer, scoutAssignment, gateWorkers[].source="repurposed".',
  '   A scenario whose ONLY teeth is such a flag proves "the coordinator called X", not "X worked".',
  '   For each use, classify: ACCEPTABLE-BY-DESIGN (the op has no /v2 footprint at all) vs THEATRE (an',
  '   independent observable exists but is not asserted - /v2-observable mutationLog entries like',
  '   PurchaseShip/navigate, haulers[].parkedHub, credits, ship counts, daemon Prometheus metrics). Fix the',
  '   fixable by ADDING the independent assertion alongside the flag.',
  '3. FIXTURE FACTS vs WORLD TRUTH: cross-check fixtures-*.ts + scenario seeds against',
  '   twin/fixtures/era2-X1-PZ28/*.json. Known bug class: gateSite default was X1-PZ28-I57 while the real',
  '   JUMP_GATE is X1-PZ28-I67 (fixed once - verify nothing else drifted). Hubs X1-PZ28-H1..H5 are LOGICAL',
  '   names, not captured waypoints - verify what hauler parkedHub matching actually requires end-to-end.',
  '   Check prices (haulerPrice 300000 vs shipyard listings), credits, coverage numbers.',
  '4. SELF-FULFILLING pollUntil PREDICATES: can the predicate already be true on the SEED state? (e.g.',
  '   gate-entry seeds 4 haulers - any assertion counting haulers must demand a DELTA or a distinct marker,',
  '   not >= seeded count). Audit every pollUntil/advanceTicks predicate in all 25 specs.',
  '5. METRICS: scrapeBootstrapMetric assertions - right metric name + labels? What does parse-metrics do on',
  '   an ABSENT metric (0 vs throw) and can that silently pass a broken phase gauge?',
  '6. GUARD SCENARIOS (fail-closed, disabled, dry-run, capital-gate, hauler-cap, worker-cap, sticky, the 3',
  '   restart-idempotency specs): do they prove the guard held (state UNCHANGED / exactly-once), or merely',
  '   absence-of-crash?',
  '7. Assertion messages + budgets: every expect carries enough context to diagnose a red without rerunning.',
  '',
  'DELIVER: fix test-side findings in bootstrap-harness/** (helpers included). Verify: tests/unit green +',
  'your changed files parse (tsc/esbuild). Do NOT edit twin/ or gobot/.',
  '', GITP, '',
  'RETURN: a 25-row verdict table (spec | SOUND / theatre-FIXED / needs-wave-2 | the teeth), ranked findings',
  'with file:line, wave-2 items (twin/gobot/coordinator), an explicit ruling "is the oracle trustworthy',
  'now?", then the mandatory report block.',
].join('\n')

const P_CLI = [
  'You execute bead st-drm.11 as two jobs over the twin low-level test layer (twin/tests/** ONLY - no',
  'src/ edits, no gobot/ edits): JOB 1 audit existing tests for theatre and fix them; JOB 2 write the',
  'MISSING low-level CLI acceptance specs.',
  '', ISO, '', STALL, '',
  'JOB 1 - AUDIT + FIX IN PLACE:',
  'Scope: tests/acceptance/{ship-actions.e2e,cargo-trade.e2e,contracts-lifecycle,construction}.test.ts,',
  'tests/ships/{list,show,cargo}.test.ts, tests/endpoints/*.test.ts, tests/{agent,agent-auth,market,',
  'register,server-status}.test.ts. Bar: assertions must prove OBSERVABLE EFFECTS (before/after deltas,',
  'read back through the real path - the daemon loop via tests/helpers/readback.ts for fleet state, or the',
  'twin /v2 with Bearer auth), pinned error codes, no exit-0-only or shape-only "behavior" claims.',
  'Known open findings to apply: (a) construction.test.ts over-carry (~line 241-254) asserts only a numeric',
  'error.code - pin it to 4218 (the implemented insufficient-cargo code); (b) cargo-trade oversell asserts',
  'only non-zero exit - upgrade only if the CLI surfaces the code (stderr), else document the limit inline;',
  '(c) tests/ships/cargo.test.ts is KNOWN exit-0-only theatre - upgrade it to read-back deltas or reduce it',
  'to a decode-smoke with a pointer comment to cargo-trade.e2e. DO NOT weaken the two intentionally-RED',
  'ship-actions assertions (refuel drained-tank precheck; purchase credit-drop) - they document real wave-2',
  'gaps (bead st-drm.12).',
  '',
  'JOB 2 - WRITE MISSING CLI ACCEPTANCE:',
  'Enumerate the CLI: grep "Use:" gobot/internal/adapters/cli/*.go (READ-ONLY). Map every LOW-LEVEL command',
  'that reaches the twin (direct client or daemon-mediated). EXCLUDE automation/orchestration (workflow',
  'bootstrap, contract start, container *, captain *, coordinator launchers) and the known NO-CLI endpoints',
  '(contract lifecycle, construction supply, flight-mode - do NOT invent commands). Candidates to check for',
  'coverage: player info, player register variants, universe status, ship refresh (used as a helper but',
  'never itself the subject), ship route, market-refresh/scout-style reads, config set-player, anything',
  'else the enumeration surfaces. For each genuinely-uncovered command write the acceptance spec in the',
  'house style (exemplar: tests/acceptance/ship-actions.e2e.test.ts): Given-When-Then, effects read back',
  'via readback.ts (resetCold/refreshShip/showShip/pollShip/listFleet) or authed /v2 GETs, before/after',
  'deltas, pinned codes. Live-stack specs (*.e2e.test.ts) are AUTHORED-NOT-RUN (the orchestrator runs them',
  'later); in-process specs you may run one file at a time via --config vitest.unit.config.ts.',
  'WORLD FACTS: TWINAGENT-1 (COMMAND frigate) + TWINAGENT-2 (SATELLITE) DOCKED at X1-PZ28-A1 on cold start;',
  'player-id 1; A1 sells SILICON_CRYSTALS 58 / FUEL 72 / ELECTRONICS 410; C42 sells IRON_ORE 46/40 and is a',
  'shipyard; yards A2/C42/H64 sell SHIP_PROBE 24680 / SHIP_COMMAND_FRIGATE 150000 / SHIP_LIGHT_HAULER',
  '300000; JUMP_GATE = X1-PZ28-I67; F55 = nearest non-orbital fuel market (d ~49.5); A2/A3/A4 are',
  'zero-distance orbitals of A1.',
  'Run the full in-process unit suite at the end (--config vitest.unit.config.ts) - keep it green.',
  '', GITP, '',
  'RETURN: JOB-1 fixes (file -> what changed and why it was theatre); JOB-2 new spec files (per scenario:',
  'GWT one-liner + expected RED or GREEN + why); the full coverage map (command -> spec | excluded: reason);',
  'wave-2 items; then the mandatory report block.',
].join('\n')

log('st-drm Wave 1: 4 tracks in isolated worktrees (merge protocol in the script header)')
const [timemock, openapi, harnessAudit, cliAcceptance] = await parallel([
  () => agent(P_TIMEMOCK, { label: 'st-drm.8 time-mock', phase: 'time-mock', model: 'opus', isolation: 'worktree' }),
  () => agent(P_OPENAPI, { label: 'st-drm.9 openapi-construction', phase: 'openapi', model: 'sonnet', isolation: 'worktree' }),
  () => agent(P_HARNESS, { label: 'st-drm.10 harness-audit', phase: 'harness-audit', model: 'opus', agentType: 'nw-acceptance-designer', isolation: 'worktree' }),
  () => agent(P_CLI, { label: 'st-drm.11 cli-acceptance', phase: 'cli-acceptance', model: 'opus', agentType: 'nw-acceptance-designer', isolation: 'worktree' }),
])
return { timemock, openapi, harnessAudit, cliAcceptance }
