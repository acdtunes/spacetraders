# Probe Sensing Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the market-freshness sizer + frontier expansion pair with ONE probe-sensing coordinator implementing the budgeted model in `docs/superpowers/specs/2026-07-25-probe-sensing-model-design.md`.

**Architecture:** A new standing coordinator (`probe_sensing_coordinator`) declares scout posts from a whitelist+depth census, buys probes via the existing `GuardedProbeBuyer` under a hard budget `N`, propagates discovery by buying one probe per uncharted neighbour at frontier yards, and sheds scanning under API pressure by marking posts dormant (probes park in place). The scout-post coordinator (manning/movement) is UNTOUCHED. Legacy sizer + frontier are unwired (removed from launch paths and registry), source retained pending deletion.

**Tech Stack:** Go, GORM/Postgres, existing coordinator/container machinery, existing `probebuy`, `scouting` domain packages.

## Global Constraints

- **No feature flags. Ship ARMED.** No runtime switch between new and legacy coordinators.
- **RULINGS #4:** money guards fail closed; `GuardedProbeBuyer` guard stack reused unmodified.
- **RULINGS #5:** every operational number is container config with a code default, registered in the tune registry — never a bare constant read at call sites.
- **ENGINEERING.md §6:** no archaeological/changelog comments, no bead-ids in source, no dated notes.
- Protected paths, never touch: `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.
- Anti-theatre: every behaviour RED-first (behavioural failure, not compile error); adversarial stubs (wrong value alongside miss/error flag); after green, deliberately break each behaviour (`(cond || true)` style so it compiles) and confirm the right test fails; `go build ./... && go vet ./...`, gofmt clean on touched files, `go test ./... -race` green. Two pre-existing gofmt-dirty bootstrap test files are not yours.
- Fixtures must describe a fleet era 5 can have: opening fleet = 1 COMMAND frigate + probes. `IsScoutType()` is `role == roleSatellite` ONLY — a COMMAND hull is not a scout.
- Defaults locked by the Admiral: whitelist = 12 goods below; depth floor = 2,000,000; `N` = 150; second-probe threshold = 12 hot markets; purchase cooldown = **10s**; tick = 30s.
- The 12-good whitelist (order irrelevant): `CLOTHING, LAB_INSTRUMENTS, FABRICS, FOOD, ADVANCED_CIRCUITRY, MEDICINE, EQUIPMENT, URANITE, MICROPROCESSORS, SHIP_PLATING, MACHINERY, ELECTRONICS`.

---

### Task 1: Domain — sensing scope (whitelist, depth, hot markets, post plan)

**Files:**
- Create: `gobot/internal/domain/scouting/sensing.go`
- Test: `gobot/internal/domain/scouting/sensing_test.go`

**Interfaces:**
- Consumes: nothing new (pure).
- Produces (later tasks rely on these exact names):

```go
// MarketDepthRow is one (waypoint, good) observation from the market cache.
type MarketDepthRow struct {
	System      string
	Waypoint    string
	Good        string
	TradeVolume int
	MidPrice    int // (purchase+sell)/2, computed by the adapter
}

// SystemSensingProfile is one system's rollup against the whitelist.
type SystemSensingProfile struct {
	System     string
	Depth      int64 // Σ TradeVolume×MidPrice over whitelisted goods only
	HotMarkets int   // distinct waypoints carrying ≥1 whitelisted good
}

func BuildSensingProfiles(rows []MarketDepthRow, whitelist map[string]bool) []SystemSensingProfile
// Deterministic: sorted by System asc. Rows with Good not in whitelist contribute nothing.
// Negative/zero TradeVolume or MidPrice contribute nothing (fail closed on garbage rows).

// SensingPlan is the desired standing-post set for one tick.
type SensingPlan struct {
	// Hulls per in-scope system: 1, or 2 when HotMarkets > secondProbeThreshold.
	Hulls map[string]int
	// TotalHulls is Σ Hulls — reported to the buyer as demand (clamped by N there).
	TotalHulls int
}

func PlanSensing(profiles []SystemSensingProfile, depthFloor int64, secondProbeThreshold int) SensingPlan
// In scope ⇔ Depth >= depthFloor AND HotMarkets >= 1. No ranking: every in-scope
// system is equal. depthFloor <= 0 means floor disabled (everything with a hot market in scope).
```

- [ ] **Step 1: failing tests** — cases: whitelisted-only aggregation (a row with `FUEL` contributes 0); depth sums `tv×mid` across markets; HotMarkets counts distinct waypoints not rows (two whitelisted goods at one waypoint = 1 hot market); floor excludes below, includes at exactly floor; second probe at `HotMarkets == threshold+1`, single at `== threshold`; determinism (shuffled input, identical output); garbage rows (tv=0, mid=-5) contribute nothing. Adversarial: build one profile whose Depth is *just* under floor but HotMarkets huge — must be OUT (floor is not negotiable by market count).
- [ ] **Step 2: run, confirm behavioural FAIL** (`go test ./internal/domain/scouting/ -run Sensing -v`).
- [ ] **Step 3: implement** exactly the contracts above.
- [ ] **Step 4: green + `-race`.**
- [ ] **Step 5: mutations** — drop whitelist filter (`|| true` in the membership check) → aggregation test fails; count rows not waypoints → hot-market test fails; `>=` → `>` on floor → boundary test fails. Restore, re-green.
- [ ] **Step 6: commit** `feat(scouting): sensing scope domain — whitelist depth, hot markets, post plan`

### Task 2: Domain — rotation and pressure policy

**Files:**
- Create: `gobot/internal/domain/scouting/rotation.go`
- Test: `gobot/internal/domain/scouting/rotation_test.go`

**Interfaces:**
- Produces:

```go
// ActiveShare maps smoothed limiter wait to the fraction of in-scope systems
// that scan this cycle, and whether discovery may spend headroom.
// waitLow/waitHigh come from config (defaults 50ms / 1s).
//   wait <= waitLow            → share 1.0, discovery true
//   waitLow < wait < waitHigh  → share 1.0, discovery false   (discovery sheds FIRST)
//   wait >= waitHigh           → share scales linearly 1.0→0.5 over [waitHigh, 4×waitHigh], floor 0.5, discovery false
func ActiveShare(wait, waitLow, waitHigh time.Duration) (share float64, discovery bool)

// RotateDormant returns the systems that do NOT scan this cycle: the in-scope
// list (sorted asc for determinism) is rotated round-robin by cursor; the first
// ceil(len×share) stay active, the rest are dormant. cursor advances by the
// active count each call (caller persists it in memory only — a restart resets
// rotation harmlessly). Guarantee under constant share: every system active at
// least once every ceil(1/share)+1 calls.
func RotateDormant(inScope []string, share float64, cursor int) (dormant map[string]bool, nextCursor int)
```

- [ ] **Step 1: failing tests** — share boundaries (at waitLow, between, at waitHigh, at 4×waitHigh, beyond → floor 0.5); discovery false the moment wait > waitLow; rotation: share 1.0 → empty dormant; share 0.5 over 4 systems → 2 dormant, and over 4 consecutive calls every system appears active ≥2× (the starvation-freedom property, asserted explicitly); determinism for equal inputs.
- [ ] **Step 2: behavioural FAIL.**
- [ ] **Step 3: implement.** No clocks, no randomness — pure.
- [ ] **Step 4: green + `-race`.**
- [ ] **Step 5: mutations** — discovery `|| true` when wait high → shed-first test fails; cursor never advances → starvation test fails.
- [ ] **Step 6: commit** `feat(scouting): rotation + pressure policy domain`

### Task 3: Adapter — market depth rows read

**Files:**
- Modify: `gobot/internal/adapters/persistence/market_repository.go` (append new method; follow the `SystemsFreshness` pattern at :640 — flat select, Go rollup, dialect-agnostic)
- Test: `gobot/internal/adapters/persistence/market_depth_rows_test.go` (new file; copy the harness style of the existing `SystemsFreshness` tests)

**Interfaces:**
- Consumes: `scouting.MarketDepthRow` (Task 1).
- Produces:

```go
// MarketDepthRows returns one row per (waypoint, good) for playerID from market_data:
// System (via shared.ExtractSystemSymbol), Waypoint, Good, TradeVolume,
// MidPrice = (purchase_price + sell_price) / 2. No filtering here — the domain filters.
func (r *MarketRepositoryGORM) MarketDepthRows(ctx context.Context, playerID int) ([]scouting.MarketDepthRow, error)
```

- [ ] **Step 1: failing test** — seed market_data rows for two players; assert player scoping, mid-price arithmetic, system extraction. Adversarial: a row for the OTHER player with enormous depth must not leak in.
- [ ] **Step 2: FAIL** (method absent → add a compile-shim test first is NOT acceptable; write the test calling the method and let it fail to compile only momentarily, then stub returning `nil, nil` so the failure is behavioural).
- [ ] **Step 3–4: implement, green + `-race`.**
- [ ] **Step 5: commit** `feat(persistence): per-(waypoint,good) market depth rows read`

### Task 4: Adapter — API pressure reader

**Files:**
- First: `grep -rn "apibudget" gobot/internal/domain/apibudget/ gobot/internal/adapters/ --include="*.go" | grep -v _test | head` and `grep -rn "RecordAPIRateLimitWait\|rate_limit_wait" gobot/internal/adapters/api/ gobot/internal/adapters/metrics/ | grep -v _test | head` — the wait histogram is already recorded somewhere; hook THERE.
- Create: `gobot/internal/adapters/api/limiter_pressure.go` (or extend `internal/domain/apibudget` if it already holds a rolling-wait aggregate — prefer the existing seam; decide from the grep and say which in the commit message)
- Test: alongside.

**Interfaces:**
- Produces:

```go
// LimiterPressure is a concurrency-safe EWMA of rate-limiter wait durations,
// updated at the same site that records the wait histogram.
type LimiterPressure struct { /* mutex, ewma, halfLife */ }
func NewLimiterPressure(halfLife time.Duration) *LimiterPressure
func (p *LimiterPressure) Observe(wait time.Duration, at time.Time)
func (p *LimiterPressure) Current(at time.Time) time.Duration // decays toward 0 with no traffic

// Port consumed by the coordinator (define in gobot/internal/domain/scouting/ports.go):
type PressureReader interface{ Current(at time.Time) time.Duration }
```

- [ ] **Step 1: failing tests** — EWMA rises on observes, decays with silence, halfLife honoured (observe 100ms, advance one half-life silent → ~50ms ±ε), concurrency (`-race` with parallel Observe/Current).
- [ ] **Step 2–4: FAIL, implement, green.**
- [ ] **Step 5: wire the Observe call** at the histogram-record site found by the grep (one line + constructor plumbing in `main.go`); run the api package tests.
- [ ] **Step 6: commit** `feat(api): limiter-wait EWMA pressure reader`

### Task 5: Dormancy seam — scout tour parks in place

**Files:**
- Modify: `gobot/internal/domain/scouting/post.go` (add field), `gobot/internal/adapters/persistence/` scout-post model + migration (new `dormant boolean not null default false` column — follow the newest migration file's naming pattern in `gobot/migrations/`), scout-post repository mapping.
- Modify: `gobot/internal/application/scouting/commands/scout_tour.go` — at circuit START (the loop that begins a circuit; `circuitPaceInterval` at :94 is the end-of-circuit pacing, so the check goes where a new circuit is about to begin): if the post is dormant, sleep the effective scan interval (`sleepInterruptibly`, same contract as `waitStartJitter` :246) WITHOUT flying, then re-check. Probe stays parked; zero API.
- Test: `gobot/internal/application/scouting/commands/scout_tour_dormant_test.go`

**Interfaces:**
- Consumes: `ScoutPost.Dormant bool` (new).
- Produces: dormancy honoured by tours; `ScoutPostRepository.Upsert` persists it (caller owns every field — Upsert already never merges).
- The tour needs a post read: check what the handler already has — if it holds no post reader, add the narrowest port (`DormancyReader interface{ IsDormant(ctx, playerID int, system string) (bool, error) }`) implemented by the scout-post repository. **Read error ⇒ NOT dormant (fail toward scanning — sensing is the sensor; RULINGS #4 direction is "never silently blind").**

- [ ] **Step 1: failing tests** — dormant post: tour makes ZERO api/scan calls for a full interval (adversarial fake records every call; assert empty), then resumes when flag clears; read-error stub (returns `true, errors.New("db")`) ⇒ tour scans anyway (error wins over the wrong `true`).
- [ ] **Step 2–4: FAIL, implement (field + migration + mapping + tour check), green + `-race`.**
- [ ] **Step 5: mutation** — invert fail-open (`err != nil → dormant`) → error test fails.
- [ ] **Step 6: commit** `feat(scouting): dormant posts — probes park in place, zero API`

### Task 6: The coordinator

**Files:**
- Create: `gobot/internal/application/scouting/commands/run_probe_sensing_coordinator.go`
- Test: `gobot/internal/application/scouting/commands/run_probe_sensing_coordinator_test.go` (fixture/fake style: copy from `run_market_freshness_sizer_coordinator_test.go` — same repo fakes work)

**Interfaces:**
- Consumes: Tasks 1–5 (`BuildSensingProfiles`, `PlanSensing`, `ActiveShare`, `RotateDormant`, `MarketDepthRows` via a narrow reader port, `PressureReader`, `ScoutPostRepository`, `probebuy.GuardedProbeBuyer.MaybeBuy(ctx, playerID, demand, supply, dryRun, target)`), gate adjacency for discovery (Task 7 extends this handler).
- Produces:

```go
type RunProbeSensingCoordinatorCommand struct { // container command, maxIterations -1
	PlayerID    shared.PlayerID
	ContainerID string
	// resolved config (all tunable): GoodsWhitelist []string, DepthFloor int64,
	// ProbeBudget int (N), SecondProbeThreshold int, PurchaseCooldownSecs int,
	// TickSecs int, WaitLowMs, WaitHighMs int
}
type RunProbeSensingCoordinatorHandler struct { /* ports above */ }
func NewRunProbeSensingCoordinatorHandler(...) *RunProbeSensingCoordinatorHandler
```

Tick algorithm (one pass, pure inputs → diffs):
1. `rows := depthReader.MarketDepthRows` → `profiles := BuildSensingProfiles(rows, whitelist)` → `plan := PlanSensing(profiles, floor, threshold)`.
2. `posts := postRepo.ListActive`. Diff against `plan.Hulls`: Upsert new/resized standing posts (Kind=PostKindStanding, FreshnessTarget from config default, **never shrink Hulls below post.MinHulls** — bootstrap floor-protects home with MinHulls, so honouring it is the whole INCOME-phase home story); Remove standing posts for systems no longer in scope **except** posts with MinHulls > 0. Never touch `PostKindSweepOnce` posts here.
3. `share, discovery := ActiveShare(pressure.Current(now), waitLow, waitHigh)`; `dormant, cursor = RotateDormant(inScope, share, cursor)`; Upsert only posts whose Dormant bit CHANGES (write amplification guard — assert in tests: steady state ⇒ zero Upserts).
4. Buyer: `supply` = live probe count (reuse the sizer's fleet-count source — grep its `supply` derivation and reuse the same reader), `demand = min(plan.TotalHulls + discoveryDemand, N)`; one `MaybeBuy` per tick; `PurchaseCooldown` 10s via config. In THIS task `discoveryDemand` is the literal 0 with a short comment that discovery funds it — Task 7 replaces the 0.
5. One INFO heartbeat per tick: in-scope count, hulls desired, supply, share, dormant count, discovery on/off, bought.
- Empty census (`len(profiles)==0`) ⇒ declare/remove NOTHING, heartbeat says so (the era-gap fail-safe, same rationale as the sizer's guard).

- [ ] **Step 1: failing tests** — scope→posts diff (new system appears ⇒ Upsert with Hulls 1; crosses threshold ⇒ 2; drops below floor ⇒ Remove; MinHulls-protected post never shrunk/removed); dormancy writes only deltas; empty census ⇒ zero writes; buyer called with demand clamped at N (set N=3, plan wants 10 ⇒ MaybeBuy sees demand 3 — adversarial fake buyer records args); pressure high ⇒ discovery excluded from demand. Era-5 fixture: COMMAND frigate + 2 probes, one-system census.
- [ ] **Step 2–4: FAIL, implement, green + `-race`.**
- [ ] **Step 5: mutations** — remove MinHulls guard → home test fails; remove N clamp (`min` → plan total) → clamp test fails; empty-census guard `len<0` → fail-safe test fails.
- [ ] **Step 6: commit** `feat(scouting): probe sensing coordinator — budgeted, whitelist-scoped, pressure-rotated`

### Task 7: Discovery — branching purchase + sweep-once propagation

**Files:**
- Modify: `run_probe_sensing_coordinator.go` (+ its test file)
- Consumes: `gategraph` store adjacency for uncharted-neighbour counting — use the pure store read (`Adjacency()`-backed; the `StoredHopDistances` family shape). **Zero live API on this path**: a system absent from the stored adjacency is simply not counted. Yard selection: reuse `NearestYardsSelling`/`probebuy.ProbeTarget` exactly as the legacy buyers do (grep `ProbeTarget` construction in the sizer for the pattern).

Discovery pass (runs only when `discovery == true`):
1. Census systems = charted+scanned. For each, neighbours from stored adjacency; a neighbour not in census and with no sweep-once post ⇒ frontier candidate.
2. Declare `PostKindSweepOnce` posts for candidates (bounded per tick: config `DiscoveryDeclaresPerTick` default 4). The scout-post reconciler mans them with existing relay machinery; the tour's arrival scan IS the first scan (chart+scan atomic by existing behaviour).
3. `discoveryDemand` = count of open sweep-once posts, so the buyer funds one probe per open frontier direction, clamped by N (Task 6 step 4). Yard target = nearest selling to the candidate's parent system — probes bought AT the frontier.

- [ ] **Step 1: failing tests** — candidate derivation (charted A with uncharted neighbours B,C ⇒ 2 sweep-once declares; B already postered ⇒ 1); pressure high ⇒ zero declares AND discoveryDemand 0; per-tick declare bound; N exhausted ⇒ demand clamped. Adversarial adjacency fake: returns a neighbour that IS in census — must not redeclare.
- [ ] **Step 2–4: FAIL, implement, green.**
- [ ] **Step 5: commit** `feat(scouting): branching discovery — sweep-once propagation funded at the frontier`

### Task 8: Wiring in, wiring out, config

**Files:**
- Modify: `gobot/internal/adapters/grpc/command_factory_registry.go` — **remove** lines 365–366 (`frontier_expansion_coordinator`, `market_freshness_sizer_coordinator` build entries); **add** `{CommandType: "probe_sensing_coordinator", build: buildProbeSensingCoordinatorCommand}` + the build func (copy the sizer's build func shape: resolve config keys with defaults; keys: `goods_whitelist` CSV, `depth_floor`, `probe_budget`, `second_probe_threshold`, `purchase_cooldown_secs` default **10**, `tick_secs` default 30, `wait_low_ms` 50, `wait_high_ms` 1000, `discovery_declares_per_tick` 4).
- Modify: `gobot/internal/adapters/grpc/daemon_boot_standing.go` — replace the sizer's membership in `bootStandingCoordinatorTypes` with `probe_sensing_coordinator`.
- Find remaining legacy launch/registration sites: `grep -rn "market_freshness_sizer_coordinator\|frontier_expansion_coordinator" gobot/ --include="*.go" | grep -vE "_test|run_market_freshness|frontier_" ` — remove launch paths (e.g. `container_ops_frontier_expansion.go` verb: make it return a clear "retired" error rather than deleting the file), keep source files.
- Modify: `gobot/internal/adapters/grpc/container_ops_tune.go` — add the new operation's tune block (`--operation sensing`), all keys above with bounds; **remove** the freshsizer/frontier tune blocks (their containers can no longer run).
- Modify: `cmd/spacetraders-daemon/main.go` — construct handler + register (mirror :1147–1181), delete sizer/frontier constructions.
- Recovery: removing registry entries makes still-RUNNING legacy containers **fail closed at restart recovery** ("unknown command type" — the registry documents this exact pattern at :277). That is the intended unwire. Add one test asserting the registry no longer builds the two legacy types.

- [ ] **Step 1: failing tests** — registry builds `probe_sensing_coordinator`; registry REFUSES both legacy types; boot-standing list contains the new type and not the sizer.
- [ ] **Step 2–4: FAIL, implement, green. Full `go test ./... -race`.**
- [ ] **Step 5: commit** `feat(daemon): wire probe sensing coordinator; unwire freshness sizer + frontier (source retained)`

### Task 9: Home 3→1 at EXPANSION

**Files:**
- Modify: `gobot/internal/application/bootstrap/commands/run_bootstrap_gate.go` — in the confirmed-hand-off path of `actExpansion` (and `ensureStandingHandoff`'s confirmed branch), lower the HOME post's `MinHulls` to 1 via `ScoutPostRepository` (port already reachable from bootstrap — grep how bootstrap declares the home post today, sp-difa Option B, and reuse that seam). Idempotent: `MinHulls == 1` already ⇒ no write. The sensing coordinator then resizes home to the standard rule next tick — that IS the retirement of 2 probes; released probes become buyer supply.
- Test: extend `run_bootstrap_mature_noop_test.go` fixtures' shape — new test file `run_bootstrap_home_retirement_test.go`.

- [ ] **Step 1: failing tests** — confirmed hand-off lowers home MinHulls 3→1 exactly once (second tick: zero writes); UNCONFIRMED hand-off (bounded-exit WARN path) also lowers it (the world signal, not the hand-off, owns the phase — same doctrine as the exit itself); fresh fleet (no EXPANSION) never touches it.
- [ ] **Step 2–4: FAIL, implement, green + `-race` on bootstrap package.**
- [ ] **Step 5: commit** `feat(bootstrap): hand-off releases the home scout reinforcement — sensing owns home from EXPANSION`

### Task 10: Stage-2 market selection — the tour circuits only hot markets

**Files:**
- Modify: `gobot/internal/domain/scouting/post.go` + scout-post persistence model + migration — add `HotWaypoints []string` (persisted as JSONB/text; follow how the model stores any existing list field, else CSV in a text column).
- Modify: `run_probe_sensing_coordinator.go` — stamp each standing post's `HotWaypoints` with the waypoints carrying ≥1 whitelisted good (already known from `MarketDepthRow`s; sorted asc; Upsert only when the set CHANGES).
- Modify: `gobot/internal/application/scouting/commands/scout_tour.go` — when the tour's post is standing AND `len(HotWaypoints) > 0`, restrict the circuit to those waypoints. **Empty list ⇒ full circuit** (stage 1, and the fail-toward-sensing direction). Sweep-once posts always full-circuit (that IS the first scan).

**Interfaces:**
- Consumes: `MarketDepthRow`s (Task 1), the dormancy post-read seam (Task 5) — extend that same reader rather than adding a second.
- Produces: stage-1→stage-2 behaviour: first declaration may carry hot waypoints immediately (census already knows them); a system whose census shows NO whitelisted goods never got a standing post at all (Task 1 scope), so the empty-list case is cold-start/first-tick, not steady state.

- [ ] **Step 1: failing tests** — post with HotWaypoints {A,C} of {A,B,C}: tour visits A,C only (fake records visits); empty list: visits all three; sweep-once ignores the field; coordinator stamps sorted hot set and does NOT Upsert when unchanged. Adversarial: reader returns wrong non-empty list alongside an error ⇒ full circuit (error wins).
- [ ] **Step 2–4: FAIL, implement, green + `-race`.**
- [ ] **Step 5: mutation** — empty-list branch inverted (empty ⇒ skip all) → stage-1 test fails.
- [ ] **Step 6: commit** `feat(scouting): stage-2 circuits — standing tours visit only whitelisted-good markets`

### Task 11: Live verification (orchestrator-run, not the lane)

- [ ] Gate via captain-gate, deploy, then: `spacetraders tune --operation sensing` lists all knobs; heartbeat line appears with in-scope count and share 1.0; `SELECT COUNT(*) FROM containers WHERE container_type IN ('SCOUT'...) ` posts converge; **zero** running `market_freshness_sizer_coordinator`/`frontier_expansion_coordinator` containers post-restart; probe buys respect N and 10s cooldown; `spacetraders_daemon_api_rate_limit_wait_seconds` EWMA visible in heartbeat. Grade ≥30 min before claiming request-rate wins.
