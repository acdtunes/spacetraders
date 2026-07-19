# ENGINEERING.md — gobot engineering reference (shipwright's field manual)

Reference, not doctrine — the shipwright and its build agents READ this when the task
touches the area; it is not primed every wake. Doctrine and process rules live in
`RULINGS.md`, `PLAYBOOK.md`, and the shipwright template; the CLI surface lives in
`CLI-PRIMER.md`. This book carries the durable gobot code-facts and traps that cost real
time to rediscover. The code is always truth; this is the map.

---

## 1. Worktree & lane hazards

- **Worktree isolation is SOFT.** Each lane gets a separate checkout + cwd, but NOT a
  write-sandbox — an agent can still write to the main checkout via an absolute path. Give
  build agents WORKTREE-ABSOLUTE edit paths (a bare `gobot/…` relative resolves against
  main, not the worktree). After every lane, verify the main working tree is pristine
  (`git status --porcelain gobot/`) before deploying.
- **Never run two lanes on the SAME FILE concurrently** — the one true corruption path.
  Serialize same-file lanes; allow concurrency across disjoint files (grep call sites to
  confirm disjoint territory).
- **Spawned lanes share the session's job tmp.** A prior task's scratch file can be read by
  a later lane — brief agents to use a unique per-bead filename or inline the content, and
  verify the final commit SUBJECT before merge (a crossed scratch file can carry the wrong
  message).
- **Commit `--no-verify` unconditionally; never stage `.beads/issues.jsonl`.** The beads
  pre-commit hook re-sweeps the jsonl at commit time — staging it sweeps tracker churn into
  code commits (RULINGS #12 family).
- **NEVER `git reset --hard` a shared checkout.** It reverts tracked `.beads/issues.jsonl`,
  making bd abandon its real dolt db for a fresh empty shadow — looks like total data loss
  (the dolt data is safe under `.beads/dolt/`). Abort a bad merge with `git merge --abort`
  or restore only your own files.

## 2. Deploy & recovery

- **Deploy surfaces:** `make build-cli` (CLI verbs), `make restart-daemon` (daemon logic),
  `make build-watchkeeper` + kickstart (watchkeeper). Batch daemon restarts by content
  (RULINGS-driven deploy doctrine in the shipwright template). Every binary self-reports its
  commit (`spacetraders version`).
- **Routing-service is a SEPARATE deploy step.** Its Python proto stubs are gitignored —
  regenerate both proto sides in the service venv or it serves the old proto; kickstart
  routing FIRST, then the daemon.
- **launchd restart-throttle wedge.** If a deploy leaves the daemon down and `launchctl
  print` shows `state = spawn scheduled` + repeated `exit code 78 EX_CONFIG`, that is a
  restart-throttle WEDGE, not a broken binary (confirm by running the binary directly).
  Recover with `launchctl bootout gui/<uid>/<label>` then `bootstrap gui/<uid> <plist>` — a
  plain deploy retry does NOT clear the throttle.
- **The watchkeeper plist must carry `BD_REAL=<abs path to the real bd>`.** The bd-router
  shim shadows the real bd and the plist PATH lacks it, so without it every captain wake
  fails silently (gc → bd → exit 127). Re-add after any plist regen. Launchd templates are
  committed source-of-truth precisely so BD_REAL and the watchkeeper name can't vanish on
  regen.
- The daemon plist needs `ExitTimeOut ≥ 35` so launchd honors the drain on restart.
- **Go build-cache corruption:** phantom "package X is not in std" errors from parallel jobs
  are environmental — `go clean -cache` and retry; do not chase your diff.

## 3. gobot data-model gotchas

- **Market columns are stored INVERTED vs the API.** `market_data.PurchasePrice` = the
  market's BUY column = what you RECEIVE selling = the API's `sellPrice`; `market_data.SellPrice`
  = the ask = what you PAY. A test or consumer that maps the API's `purchasePrice`/`sellPrice`
  straight through is the inverted-margin trap — it overstates spreads ~2× (market.go:679-683,
  market_spreads_test.go:42).
- **`ship list` reads the daemon's LOCAL Postgres, not the API** (`FindAllByPlayer`,
  ship_repository.go). The daemon populates it via `syncAllShipsOnStartup` (on boot) or per-
  ship `ship refresh`. A test/coordinator needs a daemon (re)start or a refresh to see
  fleet-from-twin; a bare reset won't sync it.
- **`dedicated_fleet` tags are local-DB only** — there is NO API field. The coordinator's own
  dedication write sets them; a `resetDaemonDb` + re-sync from the API blanks them to NULL (a
  recurring restart trap). Teleport/test fixtures must seed dedication tags in the test DB
  after the daemon starts.
- **Daemon tables are MULTI-PLAYER.** `ships`, `market_data`, `shipyard_inventory` carry a
  `player_id` — always scope aggregates by the current era's player_id or you count
  competitors and manufacture phantom bugs. `gate_edges` is universe-wide and correctly
  unscoped.
- **`container get` serializes LAUNCH-FROZEN metadata.** Live mutations (`fleet hub
  add/remove`, `tune`, `goods factory`, `construction override`) do NOT appear there until a
  daemon restart re-syncs from the DB. Verify a live-mutated value by its behavioral effect or
  the DB row, never by `container get`.

## 4. Testing & the digital twin

The twin (`twin/`, branch `feat/twin-digital-twin` — unmerged; merge + run the INCOME/GATE
harness before trusting the bootstrap loop) is an in-memory Fastify fake of the /v2 API that
drives coordinators deterministically. gobot points at it via `ST_API_BASE_URL`. Invariants
learned the hard way:

- **Fake-blindness.** When a feature writes rows with relational constraints, the test plan
  MUST include at least one end-to-end path through the REAL persistence layer (test-DB with
  FKs live) exercising the write ORDER — port/boundary fakes validate logic but never schema
  or insert-ordering.
- **`resolveNav`: an IN_TRANSIT ship's `nav.waypointSymbol` MUST be the DESTINATION, not the
  origin** (real-API contract; the daemon maps it to `Ship.currentLocation` and its domain
  invariant is "while IN_TRANSIT, CurrentLocation() is the destination"). A twin that serves
  the origin in flight makes a rebooted daemon re-adopt an in-flight hull as parked at the
  origin → the coordinator re-dispatches a second hull → two hulls on one hub. Only status
  flips at arrival; waypointSymbol is the destination the moment a transit exists.
- **Seeded ships must be a SUBSET of `/my/ships`.** A seed that inserts ships only into the
  daemon's local Postgres (not the twin's `/my/ships`) breaks any flow that NAVIGATES a real
  hull: prod syncs the local DB FROM `/my/ships`, so a local-only hull 404s on
  navigate/set-flight-mode. Have the twin seed create ships in `world.ships`, not just the
  projection.
- **GATE-entry teleport concern.** `seedGateEntry` pre-seeds `world.haulers[]` (the control-
  plane array) but NOT `world.ships` — so gate repurpose-able haulers are invisible to the
  daemon's `/my/ships` sync and need `dedicated_fleet='contract'` set post-sync. Golden-path
  fix: seed N LIGHT_HAULER ships into `world.ships` + tag them in the test DB after
  `startTestDaemon`. (INCOME-entry, by contrast, correctly seeds `world.haulers=[]` — the
  coordinator buys 0→N during INCOME, so `obs.Haulers=0` at entry is correct.)
- **No manual background twins.** Agents running the live-stack vitest suite stall when they
  spawn a manual repro twin (`tsx src/main.ts &`) that holds `:8080` — the next `globalSetup`
  can't bind and hangs. The vitest suite self-manages and self-terminates (~24s); run it with
  the Bash-tool timeout as the backstop.

## 5. Structural-fix heuristics (judgment, not law)

- Three or more bugs sharing one root cause = you are fighting the architecture; file the
  structural bead instead of patch N+1 (the CLI-runner→daemon-container migration is the
  canonical example).
- When a fix unblocks a previously-masked code path, the newly-reachable path is exactly
  where the next bug hides — verify THAT path. One worker's bug must never panic the daemon
  (constructor nil-guards, degrade-to-relaunchable).
- **Coordinator defaults must match fleet practice.** When a standing coordinator takes over
  a manual operator workflow, its config DEFAULTS must reproduce the operator's STANDING
  practice (e.g. the captain's always-flown reserve), not the code's per-run defaults — a
  silent default swap can legally draw the fleet down. Encode the values the human actually
  flew.

## 6. Comment discipline (binding — never regress this)

A comment exists for the NEXT reader of the code, not to narrate your change to a reviewer.

- **NEVER add archaeological / historic / changelog prose.** No "previously we…", no "this
  was added because…", no dated narration, no "the old code did Y", no bead-ids in the
  source — neither as war-stories nor as bare `(sp-xxxx)` provenance tags. History lives in
  git and beads; in the source it is noise the moment the PR merges. A bead-id may appear
  only when load-bearing inside a live-constraint sentence — almost never.
- **Only add a short, focused WHY** the code itself cannot show: a non-obvious constraint,
  an ordering requirement, a guard's rationale, a real gotcha. One or two lines. If the code
  already says it, say nothing.
- Keep godoc, compiler directives, and license headers. Match the surrounding comment
  density; when in doubt, fewer.
- The test before you write a comment: *does this help someone understand the code, or am I
  explaining my edit?* If it's the edit, it goes in the commit message or the bead, never
  the source.
