# Parked-Probe Sensing Implementation Plan (sp-k6v8z)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **House process:** each Task lands as its own build-lane commit series in a fresh worktree, gated to main via captain-gate (RULINGS #13). Run `go test ./... ` from `gobot/` (NOT `make test` — it swallows exit codes via `|| true`). Pre-merge sweep adds `-race`. Track work in bd (bead sp-k6v8z), never TodoWrite.

**Goal:** Replace the touring core of `probe_sensing_coordinator` with the parked-probe model: one docked probe per whitelisted market, a treasury-residual buy queue, a paced-parallel scan scheduler, and a chart-tour expansion engine — per spec `docs/superpowers/specs/2026-07-26-parked-probe-sensing-design.md`.

**Architecture:** The existing coordinator (container type `PROBE_SENSING_COORDINATOR`, command type `probe_sensing_coordinator`, same boot-standing wiring) keeps its `Handle`/`ReconcileOnce` shell and composes four new engines: screen (slot discovery), buy+place (capex + placement state machine), scan (pacer goroutine), expand (BFS chart tours). Durable state lives in two new GORM tables (`sensing_systems`, `sensing_slots`) — RULINGS #2: everything re-derivable from domain tables, nothing load-bearing in memory. Ship commands go through mediator ports declared in the application layer and implemented in adapters (the bootstrap template, recon §6.3).

**Tech Stack:** Go 1.24 (module `github.com/andrescamacho/spacetraders-go`, root `gobot/`), GORM (postgres live / sqlite `:memory:` tests via `database.NewTestConnection()`), mediator pattern (`internal/application/mediator`), Prometheus metrics (`internal/adapters/metrics`), existing API client `internal/adapters/api`.

## Global Constraints

- RULINGS #4: money guards fail closed; `common.ImmutableReserveFloor` (=50000, `internal/application/common/reserve_floor.go:11`) is never weakened, never made configurable.
- RULINGS #5: every knob has a const default in the coordinator file; resolver falls back on `<=0`.
- RULINGS #2: coordinator survives restart by re-deriving from DB tables; only `containers.config` JSON persists as config.
- No feature flags / arming seams (Admiral standing order). Everything here is live on deploy; `expansion_enabled` is an operator kill-switch knob, not an arming seam.
- **No O(fleet) API enumeration anywhere** (Admiral directive): fleet reads come from the `ships` table via repository ports; no port defined in this plan may expose `ListShips`-style API calls.
- Tune keys are int-only (`container_ops_tune.go` mechanism); the goods whitelist stays in config.yaml `[sensing]` (authoritative re-inject at `container_ops_probe_sensing.go:72`).
- Sensing API calls carry `api.WithPriority(ctx, api.PriorityLow)` + `api.WithSource(ctx, ...)` (Task 1); they must never ride NORMAL/HIGH.
- Commit style: `rtk git add <paths> && rtk git commit --no-verify -m "..."` (the bd hook auto-stages `.beads/issues.jsonl`; `--no-verify` + explicit paths keeps commits code-only). Reference sp-k6v8z in messages.
- gofmt clean; adversarial fakes in tests (wrong value alongside every error — house style, `run_probe_sensing_coordinator_test.go:24-27`).

## Spec divergences discovered during recon (already true in tree; the plan encodes reality)

1. The limiter priority scheduler is **already armed unconditionally** (`client.go:147`, Admiral 2026-07-17). Spec §7's "is armed by this design" is stale. Work item = tag sensing calls LOW + fix two stale comments claiming "default OFF" (`priority_classifier.go:51`, `retry_policy.go:128`).
2. `Get Market` classifies **NORMAL** today (`priority_classifier.go:64-84` lists it in neither map). We do NOT demote the endpoint (trade paths read markets too); the scheduler demotes its own calls via ctx.
3. `apibudget.Purpose` = {poll, transact, retry}, method-inferred (`budget_purpose.go:11`). The spec's source taxonomy is **new build** (Task 1), not an extension of existing constants.
4. No pending-capex intent seam exists (recon flag #1). The floor's third term becomes knob `capex_reserve_credits` (default 100_000 — matches `NonContractWorkingCapitalFloor − ImmutableReserveFloor`), replaceable by a real intent ledger later (file follow-up bead at Task 5).
5. `GetMarket` mapping **drops goods absent from `tradeGoods`** (`client.go:1376-1401`) — a presence-less GET would map to zero goods. Task 4 adds `TradedGoodSymbols` to the mapped result (additive).
6. Sensing today declares scout posts (`standing` @ `run_probe_sensing_coordinator.go:519`, `sweep_once` @ `:821`) manned by the **live** scout-post coordinator. Cutover (Task 8) removes sensing's posts but preserves the home-system post (bootstrap's, spec §8) and leaves the scout-post coordinator + `probe_buyer_coordinator` running (the latter self-quiesces: its `GuardedProbeBuyer` cooldown reads any `SHIP_PROBE` purchase from the shared ledger).

---

### Task 1: API source tagging + non-source rate

**Files:**
- Modify: `gobot/internal/domain/apibudget/report.go` (add `Source` type + `Event.Source`)
- Create: `gobot/internal/adapters/api/source_tag.go`
- Modify: `gobot/internal/adapters/api/retry_policy.go:149-153` (Record call site)
- Modify: `gobot/internal/adapters/metrics/api_budget_tracker.go` (Record signature + `NonSourceRate`)
- Modify: `gobot/internal/adapters/api/priority_classifier.go:51`, `gobot/internal/adapters/api/retry_policy.go:128` (stale "default OFF" comments)
- Modify: `docs/superpowers/specs/2026-07-26-parked-probe-sensing-design.md` §7 (one line: "already armed 2026-07-17; this design tags sensing LOW")
- Test: `gobot/internal/adapters/metrics/api_budget_tracker_test.go`, `gobot/internal/adapters/api/source_tag_test.go`

**Interfaces:**
- Consumes: `apibudget.Event`, `APIBudgetTracker.Record` (single call site `retry_policy.go:152`), `shared.Clock`.
- Produces (later tasks rely on these exact names):
  - `apibudget.Source` (string type) with constants `SourceScanning`, `SourceCharting`, `SourceTrading`, `SourceContract`, `SourceNavigation`, `SourceBootstrap`, `SourceUnspecified Source = ""`
  - `api.WithSource(ctx context.Context, s apibudget.Source) context.Context` and `api.sourceFromContext(ctx) apibudget.Source`
  - `func (t *APIBudgetTracker) NonSourceRate(window time.Duration, excluded ...apibudget.Source) float64` — req/s of events whose Source is NOT in `excluded`, over `window` (untagged `""` counts as non-excluded, i.e. conservative: sensing never under-counts others).
  - `func (t *APIBudgetTracker) Record(hull string, purpose apibudget.Purpose, source apibudget.Source, rateLimited bool)` (signature change; sole caller updated in same commit).

- [ ] **Step 1: Write the failing tracker test**

```go
// api_budget_tracker_test.go
func TestNonSourceRate_ExcludesTaggedSource(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	tr := NewAPIBudgetTracker(2.0, clock)
	for i := 0; i < 30; i++ { // 30 scanning calls
		tr.Record("PROBE-1", apibudget.PurposePoll, apibudget.SourceScanning, false)
	}
	for i := 0; i < 12; i++ { // 12 trading calls
		tr.Record("HAULER-1", apibudget.PurposeTransact, apibudget.SourceTrading, false)
	}
	for i := 0; i < 6; i++ { // 6 untagged calls — MUST count as non-sensing
		tr.Record("SHIP-9", apibudget.PurposePoll, apibudget.SourceUnspecified, false)
	}
	clock.Advance(60 * time.Second)
	got := tr.NonSourceRate(60*time.Second, apibudget.SourceScanning, apibudget.SourceCharting)
	want := 18.0 / 60.0 // trading + untagged
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("NonSourceRate = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `cd gobot && go test ./internal/adapters/metrics/ -run TestNonSourceRate -v` → FAIL (`SourceScanning` undefined, Record arity).

- [ ] **Step 3: Implement.** In `apibudget/report.go`: add `type Source string`, the seven constants above, and `Source Source` field on `Event`. In `api_budget_tracker.go`: extend `Record` to stamp `Source`, add `NonSourceRate` (lock, prune to `retentionWindow`, count events with `ev.At.After(now.Add(-window))` and `!slices.Contains(excluded, ev.Source)`, divide by `window.Seconds()`). Note `retentionWindow = 5m` (`api_budget_tracker.go:14`) already bounds memory; reject `window > retentionWindow` by clamping to it.

- [ ] **Step 4: Write the failing ctx-propagation test**

```go
// source_tag_test.go (package api)
func TestWithSource_RoundTrips(t *testing.T) {
	ctx := WithSource(context.Background(), apibudget.SourceCharting)
	if got := sourceFromContext(ctx); got != apibudget.SourceCharting {
		t.Fatalf("sourceFromContext = %q, want charting", got)
	}
	if got := sourceFromContext(context.Background()); got != apibudget.SourceUnspecified {
		t.Fatalf("untagged ctx = %q, want empty", got)
	}
}
```

- [ ] **Step 5: Implement `source_tag.go`** (mirror `WithPriority` at `priority_classifier.go:52-58`: unexported key struct, exported With, unexported from). Update the single Record call site `retry_policy.go:152` to `tracker.Record(hull, purpose, sourceFromContext(ctx), outcome.statusCode == http.StatusTooManyRequests)`. Fix the two stale comments (state: "armed unconditionally since 2026-07-17; ctx tag orders acquisition"). Amend spec §7 line.

- [ ] **Step 6: Full package runs** — `go test ./internal/adapters/metrics/ ./internal/adapters/api/ -v` → PASS; `go build ./...` clean (the Record arity change must compile everywhere — one caller).

- [ ] **Step 7: Commit** — `rtk git add gobot/internal/domain/apibudget gobot/internal/adapters/api gobot/internal/adapters/metrics docs/superpowers/specs/2026-07-26-parked-probe-sensing-design.md && rtk git commit --no-verify -m "feat(api): source-tagged budget events + NonSourceRate for the sensing residual (sp-k6v8z T1)"`

---

### Task 2: Pure domain — budget arithmetic, weights, next-due scheduling, floor

**Files:**
- Create: `gobot/internal/domain/parkedsensing/budget.go`, `weights.go`, `schedule.go`, `floor.go`
- Test: `gobot/internal/domain/parkedsensing/budget_test.go`, `weights_test.go`, `schedule_test.go`, `floor_test.go`

**Interfaces:**
- Consumes: nothing outside stdlib + `internal/domain/shared` (pure package — no GORM, no mediator, no api).
- Produces (exact signatures later tasks import):

```go
package parkedsensing

// budget.go
type BudgetInputs struct {
	CeilingReqPerSec float64 // api.RateLimitPerSecond = 2.0, passed in
	TargetUtilPct    int     // knob, default 92
	MinScanRateMilli int     // knob, default 100 (=0.1 req/s)
	NonSensingRate   float64 // tracker.NonSourceRate(...)
	ChartingRate     float64 // recent charting spend, subtracted from pacer share
	BrakeFactor      float64 // (0,1], from ApplyBrake
}
func SensingRate(in BudgetInputs) float64        // clamp(util*ceiling - nonsensing, min, ceiling) * brake
func PacerRate(in BudgetInputs) float64          // max(SensingRate - ChartingRate, min)
func ApplyBrake(prev float64, waitEWMA, waitLow, waitHigh time.Duration) float64
	// waitEWMA > waitHigh → prev*0.5 ; waitEWMA < waitLow → min(prev*1.2, 1.0) ; else prev. Floor 0.1.

// weights.go
func ScanWeight(spreadEWMA, fleetMedianSpread float64, clampR int) float64
	// spreadEWMA<=0 or median<=0 → OptimisticPriorWeight(clampR) ; else clamp(spread/median, 1, R)
func OptimisticPriorWeight(clampR int) float64   // = min(2, float64(clampR)) — p75-ish stand-in, documented
// EWMA update used by the scanner: alpha 0.3
func UpdateSpreadEWMA(prev, observed float64) float64

// schedule.go
type SlotSchedule struct{ Waypoint string; Weight float64; LastScan time.Time }
func Interval(totalWeight, weight, ratePerSec float64) time.Duration // Σw/(rate*w), guard rate<=0 → 1h cap
func NextDue(s SlotSchedule, totalWeight, rate float64) time.Time
// heap: NextDueHeap implementing container/heap over SlotSchedule by NextDue (stable tiebreak: Waypoint)

// floor.go
func ProbeBuyFloor(immutable int64, capexReserve int64, cargoSpendPerHour int64, k int) int64
	// immutable + capexReserve + k*cargoSpendPerHour ; every term max(0,·); NEVER below immutable (fail closed)
func CargoSpendPerHour(sumAbsAmountLastHour int64) int64 // identity, named for call-site readability
```

- [ ] **Step 1: Write failing tests — budget.** Cases: quiet fleet (`NonSensingRate 0.1, util 92` → `SensingRate ≈ 1.74`), busy fleet (`NonSensingRate 1.9` → clamps to min 0.1), brake halves under high wait and recovers ×1.2 capped at 1.0, `PacerRate` subtracts charting but never below min.
- [ ] **Step 2:** `go test ./internal/domain/parkedsensing/ -run TestSensingRate -v` → FAIL (package absent).
- [ ] **Step 3: Implement `budget.go`** exactly per signatures. No I/O, no clock.
- [ ] **Step 4: Failing tests — weights + schedule.** `ScanWeight`: below-median clamps to 1; 10×median clamps to R; zero-history returns `OptimisticPriorWeight`. `Interval`: 3000 slots weight 1, rate 1.8 → ≈1667s; weight R=4 slot → ≈417s at Σw=3000 (assert within 1s). Heap pops in NextDue order; renormalisation = recompute Interval with new totals (assert ordering preserved when rate halves).
- [ ] **Step 5: Implement `weights.go` + `schedule.go`.** Run: `go test ./internal/domain/parkedsensing/ -v` → PASS.
- [ ] **Step 6: Failing tests — floor.** `ProbeBuyFloor(50_000, 100_000, 300_000, 2)` = 750_000; zero-trading era start = 150_000; negative inputs clamp so result ≥ 50_000 always (RULINGS #4 pin test named `TestProbeBuyFloor_NeverBelowImmutable`).
- [ ] **Step 7: Implement `floor.go`.** Full package green.
- [ ] **Step 8: Commit** — `rtk git add gobot/internal/domain/parkedsensing && rtk git commit --no-verify -m "feat(parkedsensing): pure domain — residual budget, spread weights, next-due schedule, dynamic buy floor (sp-k6v8z T2)"`

---

### Task 3: Ledger persistence — `sensing_systems` + `sensing_slots`

**Files:**
- Modify: `gobot/internal/adapters/persistence/models.go` (two models + `AllModels()` registration ~line 902)
- Create: `gobot/internal/adapters/persistence/sensing_ledger_repository.go`
- Create: `gobot/migrations/045_add_parked_sensing_ledger.up.sql`, `gobot/migrations/045_add_parked_sensing_ledger.down.sql`
- Test: `gobot/internal/adapters/persistence/sensing_ledger_repository_test.go`

**Interfaces:**
- Consumes: `database.NewTestConnection()` (`internal/infrastructure/database/connection.go:131`), GORM conventions of `models.go`.
- Produces:

```go
// models.go
type SensingSystemModel struct { // TableName() "sensing_systems"; PK (player_id, system_symbol)
	PlayerID       int        `gorm:"primaryKey;column:player_id"`
	SystemSymbol   string     `gorm:"primaryKey;column:system_symbol;size:50"`
	Verdict        string     `gorm:"column:verdict;size:20;not null;default:'PENDING'"` // PENDING|IN_SCOPE|NO_WHITELIST
	ScreenedAt     *time.Time `gorm:"column:screened_at"`
	UnchartedCount int        `gorm:"column:uncharted_count;not null;default:0"`
	SeedShip       *string    `gorm:"column:seed_ship;size:50"`
	SeedState      *string    `gorm:"column:seed_state;size:20"` // DISPATCHED|CHARTING|DONE
	DepthCredits   int64      `gorm:"column:depth_credits;not null;default:0"`
	EraID          *int       `gorm:"column:era_id;index"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}
type SensingSlotModel struct { // TableName() "sensing_slots"; PK (player_id, waypoint_symbol)
	PlayerID       int        `gorm:"primaryKey;column:player_id"`
	WaypointSymbol string     `gorm:"primaryKey;column:waypoint_symbol;size:50"`
	SystemSymbol   string     `gorm:"column:system_symbol;size:50;index;not null"`
	SlotKind       string     `gorm:"column:slot_kind;size:10;not null"`  // MARKET|YARD|SPARE
	State          string     `gorm:"column:state;size:12;not null;index"` // WANTED|QUEUED|BOUGHT|IN_TRANSIT|PARKED
	AssignedShip   *string    `gorm:"column:assigned_ship;size:50;index"`
	PurchaseYard   *string    `gorm:"column:purchase_yard;size:50"`
	WhitelistGoods string     `gorm:"column:whitelist_goods;type:text;not null;default:'[]'"` // JSON array
	SpreadEWMA     float64    `gorm:"column:spread_ewma;not null;default:0"`
	LastScanAt     *time.Time `gorm:"column:last_scan_at"`
	DepthCredits   int64      `gorm:"column:depth_credits;not null;default:0"`
	EraID          *int       `gorm:"column:era_id;index"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}
// sensing_ledger_repository.go — NewSensingLedgerRepository(db *gorm.DB) *SensingLedgerRepository
UpsertSystem(ctx, m SensingSystemModel) error
SystemsByVerdict(ctx, playerID int, verdict string) ([]SensingSystemModel, error)
UpsertSlot(ctx, m SensingSlotModel) error
SlotsByState(ctx, playerID int, states ...string) ([]SensingSlotModel, error)
SlotsBySystem(ctx, playerID int, system string) ([]SensingSlotModel, error)
TransitionSlot(ctx, playerID int, waypoint, fromState, toState string, mutate func(*SensingSlotModel)) error
	// optimistic: UPDATE ... WHERE state=fromState; 0 rows → ErrSlotStateConflict (exported)
CountOwnedProbes(ctx, playerID int) (int64, error) // states QUEUED..PARKED with AssignedShip OR BOUGHT+; the probe_cap read
MarkScanned(ctx, playerID int, waypoint string, at time.Time, spreadEWMA float64) error
```

- [ ] **Step 1: Failing repo test** — sqlite round-trip: UpsertSlot → SlotsByState(WANTED) finds it; `TransitionSlot` WANTED→QUEUED succeeds once, second concurrent-style call (stale fromState) returns `ErrSlotStateConflict`; `CountOwnedProbes` counts BOUGHT/IN_TRANSIT/PARKED with ship, ignores WANTED. Use `database.NewTestConnection()`.
- [ ] **Step 2:** `go test ./internal/adapters/persistence/ -run TestSensingLedger -v` → FAIL.
- [ ] **Step 3: Implement models + repo.** Register both in `AllModels()`. Write migration 045 as idempotent `CREATE TABLE IF NOT EXISTS` mirroring the GORM tags exactly (copy the 041/044 header convention: boot AutoMigrate is non-fatal, so the SQL must match the model or the drift test fails).
- [ ] **Step 4: Run** repo tests + the drift gates: `go test ./internal/adapters/persistence/ -run 'TestSensingLedger|Drift|ModelsRegistry' -v` → PASS.
- [ ] **Step 5: Commit** — `rtk git add gobot/internal/adapters/persistence gobot/migrations/045_add_parked_sensing_ledger.up.sql gobot/migrations/045_add_parked_sensing_ledger.down.sql && rtk git commit --no-verify -m "feat(persistence): parked-sensing ledger tables + repo, migration 045 (sp-k6v8z T3)"`

---

### Task 4: Remote screen + slot planner

**Files:**
- Modify: `gobot/internal/adapters/api/client.go:1346-1420` region (additive: `TradedGoodSymbols` on mapped market data)
- Modify: the `domainPorts.MarketData` struct it maps into (follow `GetMarket`'s return type to `internal/domain/ports/` — add `TradedGoodSymbols []string`)
- Create: `gobot/internal/application/parkedsensing/screen.go` (package `parkedsensing`, application layer)
- Test: `gobot/internal/adapters/api/client_market_mapping_test.go`, `gobot/internal/application/parkedsensing/screen_test.go`

**Interfaces:**
- Consumes: `MarketRepositoryGORM` reads (goods + trade_type live in `market_data`, recon §5), `GormWaypointRepository.ListBySystemWithTrait(ctx, system, "MARKETPLACE"|"SHIPYARD")` (`waypoint_repository.go:158`), `shared.Waypoint.HasTrait("UNCHARTED")` (`internal/domain/shared/waypoint.go:73`), `ShipyardInventoryRepository.ListSavedYards` (`shipyard_inventory_repository.go:166`) for probe-selling yards, Task 3 repo.
- Produces:

```go
// screen.go — ports declared here, implemented over existing repos in Task 8 wiring
type ScreenPorts struct {
	Waypoints    WaypointCatalog   // ListMarketWaypoints(ctx, system) / ListUnchartedCount(ctx, system) / ListProbeYards(ctx, system)
	MarketGoods  MarketGoodsReader // GoodsAt(ctx, playerID, waypoint) ([]string, bool /*known*/, error)  — DB first
	RemoteMarket RemoteMarketFetcher // FetchGoods(ctx, playerID, system, waypoint) ([]string, error)     — API gap fill, tagged charting/LOW
	Ledger       SlotLedger        // thin over Task 3 repo
}
func ScreenSystem(ctx context.Context, p ScreenPorts, playerID int, system string, whitelist map[string]bool) (ScreenResult, error)
type ScreenResult struct{ Verdict string; Slots []PlannedSlot; UnchartedCount int }
type PlannedSlot struct{ Waypoint, Kind string; WhitelistGoods []string; DepthCredits int64 }
```

Rules encoded (unit-tested): verdict `NO_WHITELIST` iff every charted market's goods ∩ whitelist = ∅ AND `UnchartedCount == 0`; systems with uncharted waypoints stay `PENDING` until charted (expansion's job); one YARD slot per system at the first probe-selling yard (from shipyard_inventory, else waypoint SHIPYARD trait as unpriced fallback); depth from `market_data` rows where present (`Σ trade_volume × (purchase+sell)/2` over whitelist goods), 0 where unknown (blind prior — ordering only).

- [ ] **Step 1: Failing client-mapping test** — fixture a GetMarket JSON body with `imports:[FOOD]`, `exports:[IRON]`, `exchange:[FUEL]` and **empty `tradeGoods`** (no presence); assert mapped result's `TradedGoodSymbols` = {FOOD, IRON, FUEL} and existing goods rows unchanged when `tradeGoods` present. (Test the mapping function the same way existing client tests fixture HTTP — see `client_retry_test.go` harness.)
- [ ] **Step 2:** run → FAIL. **Step 3:** implement additive mapping in the `client.go:1376` region (union of the three arrays, ordered, deduped). Run → PASS.
- [ ] **Step 4: Failing screen tests** — fake ports: (a) all-dark system with one whitelisted market in DB → IN_SCOPE, MARKET slot with matched goods; (b) no whitelist matches + 0 uncharted → NO_WHITELIST; (c) 3 uncharted waypoints → PENDING with count 3, no verdict flip; (d) DB-unknown charted market triggers exactly one RemoteMarket.FetchGoods and its result is persisted via Ledger upsert (call-count assert); (e) probe yard present → YARD slot planned.
- [ ] **Step 5: Implement `ScreenSystem`.** Run package → PASS.
- [ ] **Step 6: Commit** — `rtk git add gobot/internal/adapters/api gobot/internal/domain/ports gobot/internal/application/parkedsensing && rtk git commit --no-verify -m "feat(parkedsensing): remote whitelist screen + slot planner; GetMarket maps goods lists without presence (sp-k6v8z T4)"`

---

### Task 5: Buy queue + dynamic floor + placement state machine

**Files:**
- Create: `gobot/internal/application/parkedsensing/buyqueue.go`, `gobot/internal/application/parkedsensing/placement.go`
- Create: `gobot/internal/adapters/parkedsensing/ship_ports.go` (mediator-backed implementations; template = `internal/adapters/expansion/adapters.go` ProbePurchaser + `internal/adapters/grpc/bootstrap_ports_gate.go`)
- Test: `gobot/internal/application/parkedsensing/buyqueue_test.go`, `placement_test.go`

**Interfaces:**
- Consumes: `parkedsensing.ProbeBuyFloor` (T2), Task 3 repo, `ledger.TransactionRepository.FindByPlayer` with `QueryOptions{TransactionType: PURCHASE_CARGO, StartDate: now-1h}` (`internal/domain/ledger/ports.go:19,:26`), `probebuy.TreasuryReader.LiveCredits` (`guarded_probe_buyer.go:39`; impl `internal/adapters/expansion/adapters.go:66` — live API, cache-invalidated on spend, per `client.go:47-53`), mediator commands `shipyardCmd.PurchaseShipCommand{PurchasingShipSymbol, ShipType:"SHIP_PROBE", PlayerID, ShipyardWaypoint}` (`purchase_ship.go:28` — handler navigates+docks the purchasing ship itself, `:197-273`), `shipNav.NavigateRouteCommand` (in-system, `navigate_route.go:34`), `shipNav.RouteShipCommand` (cross-system, `route_ship.go:35`), `types.DockShipCommand` (`ship/types/commands.go:47`), `ShipRepository.AssignFleet` + ship-row reads (`internal/domain/navigation/ports.go:76`).
- Produces:

```go
const SensingParkedFleetTag = "sensing_parked" // dedicated_fleet for every probe this engine owns

type BuyPorts struct {
	Treasury  TreasuryReader      // LiveCredits
	CargoSpend CargoSpendReader   // AbsCargoBuySpendSince(ctx, playerID, since) (int64, error) — over transactions
	Purchaser ProbePurchaser      // Buy(ctx, playerID, purchasingShip, yardWaypoint) (BoughtProbe, error)
	Ledger    SlotLedger
	Ships     ParkedShipReader    // DockedProbeAt(ctx, playerID, waypoint) (string, bool, error); ShipAt(ctx, playerID, ship) (ShipPos, error) — DB reads ONLY
}
type BuyKnobs struct{ ProbeCap int; CapexReserve int64; K int; MaxActionsPerTick int /*const 10*/ }
func DrainBuyQueue(ctx context.Context, p BuyPorts, playerID int, k BuyKnobs, clock shared.Clock) (BuyReport, error)
func AdvancePlacements(ctx context.Context, pl PlacementPorts, playerID int, maxActions int) (PlacementReport, error)
```

Drain order (encoded + tested): fills (`SlotKind` MARKET|YARD, state WANTED, IN_SCOPE systems) sorted by system `DepthCredits` desc then FIFO — **before** any seed buys (expansion requests arrive as WANTED SPARE-kind slots targeted at frontier yards, Task 7). Each pop re-checks `LiveCredits − price ≥ ProbeBuyFloor(50_000, CapexReserve, k×spend, k)` and `CountOwnedProbes < ProbeCap` — both fail closed on error (skip tick, log). SPARE reuse: before buying for a slot, `SlotsBySystem(system, SPARE)` → reassign instead of purchase. Purchasing ship selection: a `PARKED` YARD-slot probe at the purchase yard (quartermaster), else any DB-visible docked probe at that yard, else buy at nearest yard **with presence** (`PurchaseYard` recorded on the slot at planning time; nearest = same system, else skip until expansion establishes presence — never a blind cross-map buy).

Placement machine per tick (max `maxActions` transitions): `QUEUED→BOUGHT` (purchase succeeded; new ship symbol recorded, `AssignFleet(ship, SensingParkedFleetTag)`); `BOUGHT→IN_TRANSIT` (issue NavigateRoute if same system — RouteShip if not — toward slot waypoint); `IN_TRANSIT→PARKED` (ship row shows `location_symbol == slot.WaypointSymbol` and not in transit: if `nav_status == IN_ORBIT` send Dock; if `DOCKED` mark PARKED). All ship position reads from the `ships` table via ports — the port interfaces expose no fleet-listing API method (structural no-O(fleet) guarantee).

- [ ] **Step 1: Failing floor-integration test** — fake CargoSpend returns 300_000/h, CapexReserve 100_000, k=2 → drain refuses a 23_540 probe at treasury 770_000 (floor 750_000 → 770k−23.5k < 750k) and buys at 780_000. Pin test `TestDrain_FailsClosedOnTreasuryError` (LiveCredits errors + returns wrong-high value → NO buy).
- [ ] **Step 2:** run → FAIL. **Step 3:** implement `buyqueue.go`. Run → PASS.
- [ ] **Step 4: Failing ordering + reuse + cap tests** — deep system fills before shallow; WANTED SPARE (seed) only after all fills; SPARE in-system reassigned without Purchaser call (call-count 0); cap reached → zero buys, report says `CapHeld`.
- [ ] **Step 5:** implement ordering/reuse. **Step 6: Failing placement tests** — full lifecycle on fakes: QUEUED slot + successful buy → BOUGHT with ship + fleet tag asserted; BOUGHT same-system → one NavigateRoute and IN_TRANSIT; IN_TRANSIT with ship row at waypoint IN_ORBIT → one Dock; second tick DOCKED → PARKED. Adversarial: navigate error leaves state BOUGHT (retry next tick), no double-issue within a tick.
- [ ] **Step 7:** implement `placement.go` + adapter `ship_ports.go` (mediator sends mirror `adapters/expansion/adapters.go:194 BuyProbe` claim/relay/recheck shape, minus its cooldown; ledger row comes free via PurchaseShipCommand's transaction recording — the shared-ledger serialization other buyers read).
- [ ] **Step 8:** package green, `go build ./...`. **Step 9:** `bd create --type=task --priority=2 --title="Real pending-capex intent ledger to replace capex_reserve_credits stand-in (spec §4, sp-k6v8z follow-up)" --description="Task 5 shipped the dynamic probe-buy floor with a constant capex_reserve_credits knob standing in for 'declared pending ship-capex intents' (no intent seam exists — recon flag #1). Build a minimal intent declaration consumed by parkedsensing.BuyPorts so bootstrap/autosizer purchases are reserved exactly, then retire the constant."`
- [ ] **Step 10: Commit** — `rtk git add gobot/internal/application/parkedsensing gobot/internal/adapters/parkedsensing && rtk git commit --no-verify -m "feat(parkedsensing): treasury-residual buy queue (no cooldown, cap+floor only) + placement state machine (sp-k6v8z T5)"`

---

### Task 6: Scan scheduler — pacer, workers, in-flight cap

**Files:**
- Create: `gobot/internal/application/parkedsensing/scanner.go`
- Test: `gobot/internal/application/parkedsensing/scanner_test.go`

**Interfaces:**
- Consumes: T2 schedule/heap + budget, T3 `MarkScanned`, `MarketScanner.ScanAndSaveMarket(ctx, playerID uint, waypointSymbol string) error` (`internal/application/ship/market_scanner.go:41` — does GetMarket + UpsertMarketData + price history in one call), `api.WithSource` + `api.WithPriority` (T1), `LimiterPressure` wait via the existing `domainScouting.PressureReader` the coordinator already holds (`run_probe_sensing_coordinator.go` field `pressure`, fed from `apiClient.LimiterPressure()` at `main.go:1076`).
- Produces:

```go
type ScanPorts struct {
	Scan      MarketScanRunner // Run(ctx, playerID, waypoint) error — adapter over MarketScanner, ctx pre-tagged
	Ledger    SlotLedger
	Pressure  WaitReader       // WaitEWMA() time.Duration
	Rate      RateReader       // PacerRate() float64 — closure over T2 budget recomputed by reconcile
	SpreadOf  SpreadObserver   // RelativeSpread(ctx, playerID, waypoint, whitelist []string) (float64, error) — from market_data just written
}
type Scanner struct{ /* heap, inflight chan struct{} cap N, mu */ }
func NewScanner(p ScanPorts, clock shared.Clock, inflightCap int) *Scanner
func (s *Scanner) SyncMembership(slots []SensingSlotView, rate float64) // reconcile-driven: rebuild weights/heap
func (s *Scanner) RunPacer(ctx context.Context)                        // goroutine: sleep→pop→launch until ctx done
func (s *Scanner) launch(ctx context.Context, slot SensingSlotView)    // acquire inflight token or hold pacer
```

Pacer loop mechanics (tested with `MockClock` by extracting `nextAction(now) (waypoint string, sleepFor time.Duration)` as a pure method): pop when due; ctx per call = `api.WithPriority(api.WithSource(ctx, apibudget.SourceScanning), api.PriorityLow)`; worker goroutine runs Scan → SpreadOf → `UpdateSpreadEWMA` → `MarkScanned` → reschedule slot in heap; in-flight token released in defer. If all `inflightCap` tokens are held, the pacer blocks on token acquisition (the reflex backpressure) — assert via a test where 3 scans hang and the 4th launch does not occur until one completes. YARD slots get fixed weight `1` and a floor interval = `quartermaster_cadence_secs` (whichever is later). Same-market overlap impossible: a slot re-enters the heap only in the worker's completion path (assert: hanging scan → slot absent from heap).

- [ ] **Step 1: Failing pure-pacing tests** — deterministic `nextAction` sequence over 3 slots with weights {4,1,1} at rate 1.0: hot slot appears ~4× more often over a simulated 100 pops; intervals stretch ×2 when `SyncMembership` re-rates at 0.5.
- [ ] **Step 2:** run → FAIL. **Step 3:** implement heap-driven `nextAction` + `SyncMembership`.
- [ ] **Step 4: Failing concurrency tests** — inflight cap blocks 4th launch (channel-token fake with controllable completion); ctx tag assertions (fake Scan captures ctx, asserts `sourceFromContext`==scanning via an exported test hook in T1 — add `api.SourceForTest(ctx)` if needed, test-only exported accessor); completion path calls MarkScanned with updated EWMA.
- [ ] **Step 5:** implement `RunPacer`/`launch` with `supervise.Guard`-style recovery (mirror `container_runner.go:247` idiom: a panicking worker never kills the pacer). Run with `-race`: `go test ./internal/application/parkedsensing/ -run TestScanner -race -v` → PASS.
- [ ] **Step 6: Commit** — `rtk git add gobot/internal/application/parkedsensing && rtk git commit --no-verify -m "feat(parkedsensing): paced-parallel scan scheduler — one pacer, bounded in-flight, LOW+scanning-tagged (sp-k6v8z T6)"`

---

### Task 7: Expansion engine — BFS, seeds, chart tours

**Files:**
- Create: `gobot/internal/application/parkedsensing/expansion.go`
- Modify: `gobot/internal/adapters/parkedsensing/ship_ports.go` (add chart/jump/waypoint-refresh port impls)
- Test: `gobot/internal/application/parkedsensing/expansion_test.go`

**Interfaces:**
- Consumes: gate adjacency via the coordinator's existing `GateAdjacencyReader` (`run_probe_sensing_coordinator.go:196` setter `SetGateGraph`, backed by `gate_edges`), `CreateChart(ctx, shipSymbol, token) error` (`client.go:546`; 400 code 4230 = already-charted, swallow as benign — same as `gategraph/service.go:312` caller), `JumpShipCommand` (`jump_ship.go:20`), `GetWaypoint` → waypoint upsert (extend `GormWaypointRepository` with `UpsertFromDetail(ctx, detail *domainPorts.WaypointDetail) error` if no save method exists — check `waypoint_repository.go` first, follow its era-scoping), T4 `ScreenSystem` re-run after charting, T3 ledger.
- Produces:

```go
type ExpandPorts struct {
	Gates    GateNeighbours   // Neighbours(ctx, system) ([]string, error)
	Ledger   SlotLedger
	Screen   SystemScreener   // ScreenSystem closure from T4
	SeedShip SeedCommander    // JumpTo(ctx, ship, system) / NavigateTo(ctx, ship, waypoint) / Chart(ctx, ship) / RefreshWaypoint(ctx, system, waypoint) / ReadMarketAt(ctx, playerID, ship, waypoint) — every call ctx-tagged charting/LOW
	Ships    ParkedShipReader // DB only
}
type ExpandKnobs struct{ Enabled bool; MinBudgetRate float64 /* skip tick below */; MaxActionsPerTick int /* const 6 */ }
func AdvanceExpansion(ctx context.Context, p ExpandPorts, playerID int, k ExpandKnobs, budgetRate float64) (ExpandReport, error)
```

Encoded rules (each unit-tested on fakes): frontier set = neighbours of IN_SCOPE/NO_WHITELIST systems with no `sensing_systems` row → screen charted ones immediately (remote, no ship); systems with `UnchartedCount > 0` need a seed: reuse reachable SPARE else enqueue a WANTED SPARE-kind slot at the nearest probe-selling yard (the buy queue funds it after fills). Seed lifecycle per tick — one action per seed: DISPATCHED (jump issued) → CHARTING: pick next uncharted waypoint from DB, if ship not there → NavigateTo; if there → Chart + RefreshWaypoint (+ if refreshed traits show MARKETPLACE: ReadMarketAt → whitelist check → UpsertSlot WANTED with measured depth); no uncharted left → DONE + re-`ScreenSystem` → terminal per spec: IN_SCOPE+yard → convert seed's own slot to YARD/PARKED at the yard; IN_SCOPE no yard → nearest MARKET slot; NO_WHITELIST → retarget seed at next frontier neighbour (state DISPATCHED toward it) else park as SPARE. `Enabled=false` or `budgetRate < MinBudgetRate` → return `ExpandReport{Skipped: "disabled"|"budget"}` with zero port calls (assert call counts).

- [ ] **Step 1: Failing rule tests** — (a) charted neighbour screened without any SeedCommander call; (b) uncharted neighbour + no SPARE → exactly one WANTED SPARE slot at the named yard; (c) CHARTING advances one waypoint per tick, market revealed mid-tour enqueues a WANTED slot with depth from the seed reading; (d) NO_WHITELIST verdict retargets the seed, no new buy; (e) disabled/budget-starved ticks are zero-call no-ops.
- [ ] **Step 2:** run → FAIL. **Step 3:** implement `AdvanceExpansion` + seed state machine. **Step 4:** package green incl. `-race`.
- [ ] **Step 5: Commit** — `rtk git add gobot/internal/application/parkedsensing gobot/internal/adapters/parkedsensing && rtk git commit --no-verify -m "feat(parkedsensing): BFS expansion — remote screens, chart-tour seeds, terminal-state reuse (sp-k6v8z T7)"`

---

### Task 8: Coordinator rewire, cutover, knobs, liveconfig, metrics

**Files:**
- Modify: `gobot/internal/application/scouting/commands/run_probe_sensing_coordinator.go` (replace planning/rotation/post-declaration internals; keep Handle/ReconcileOnce/health/heartbeat shells and the command struct, extended)
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_tunables.go`
- Modify: `gobot/internal/adapters/grpc/container_ops_tune.go:120-133` (sensing bounds block)
- Modify: `gobot/internal/adapters/grpc/command_factory_registry.go:736` (builder reads new keys)
- Modify: `gobot/cmd/spacetraders-daemon/main.go:1076-1092` (wire new ports + `SetLiveConfigReader`)
- Create: `gobot/internal/adapters/metrics/parked_sensing_metrics.go`
- Modify: `configs/grafana/dashboards/performance.json` (staleness + sensing-rate panels), `gobot/config.yaml.example` `[sensing]` comment block
- Test: extend `run_probe_sensing_coordinator_test.go` (new-core reconcile), `command_factory_probe_sensing_test.go`, `container_ops_tune_test.go`, cutover test file `run_probe_sensing_cutover_test.go`

**Interfaces:**
- Consumes: everything T1–T7. Existing shells preserved verbatim: `Handle` loop (`:376`), `noteReconcile` health streaks (`:282`), boot-standing launch (`daemon_boot_standing.go:176`), start-reapply spec (`container_ops_coordinator_start_reapply.go:163`), config.yaml whitelist re-inject (`container_ops_probe_sensing.go:72`), `liveconfig.Reader` (`internal/application/liveconfig/liveconfig.go:36`, wiring template `main.go:1129` probeBuyer).
- Produces: the rewired `RunProbeSensingCoordinatorCommand` — **extended, old fields retained but ignored** (recovery-safety: persisted configs from the old core must still build):

```go
// added fields (all int, tune-compatible), with const defaults (RULINGS #5):
ProbeCap             int // defaultParkedProbeCap = 3000        bounds [1, 10000]
ExpansionEnabled     int // defaultExpansionEnabled = 1 (ON)    bounds [1, 2] — tune semantics: value 0 reverts to default, so encode ON=1, OFF=2 (document in TuneBound.Description: "1=on 2=off"; resolver: Enabled = v != 2)
TargetUtilPct        int // defaultTargetUtilPct = 92           bounds [50, 95]
MinScanRateMilli     int // defaultMinScanRateMilli = 100       bounds [10, 2000]
ValueClampR          int // defaultValueClampR = 4              bounds [1, 16]
InflightCap          int // defaultInflightCap = 3              bounds [1, 8]
CapitalMultiplierK   int // defaultCapitalMultiplierK = 2       bounds [0, 10]
CapexReserveCredits  int // defaultCapexReserveCredits = 100000 bounds [0, 5_000_000]
QuartermasterCadence int // defaultQuartermasterCadenceSecs = 3600  bounds [300, 86400]
// retained live: TickSecs, WaitLowMs, WaitHighMs (brake), pressure_half_life passthrough
// retired from tune registry (keys rejected going forward): probe_budget, second_probe_threshold,
// purchase_cooldown_secs, max_spend_per_cycle, spend_window_secs, freshness_target_secs, discovery_declares_per_tick
```

ReconcileOnce becomes: liveconfig snapshot (nil reader → launch config) → budget recompute (`NonSourceRate` via a new `BudgetRateReader` port over the global tracker `metrics.GetGlobalAPIBudgetTracker()`, brake from `pressure`) → screen sweep (bounded: ≤5 PENDING systems/tick) → `DrainBuyQueue` → `AdvancePlacements` → `AdvanceExpansion` → `scanner.SyncMembership` → metrics + one structured summary log (`"action": "parked_sensing_cycle"` with rate, slots by state, staleness max). `Handle` starts the pacer goroutine once (`supervise.Guard("parked-sensing-pacer:"+containerID, ...)`) before the tick loop; ctx cancel stops both.

**Cutover (first reconcile where `sensing_systems` is empty for the player):**
1. Resolve home system = player headquarters (existing player/agent read the coordinator can reach through `FleetReader`'s backing repo — add a narrow `HomeSystemReader` port; implementation reads the players table, NOT the API).
2. Remove every `scout_posts` row for the player EXCEPT rows whose `system_symbol` == home system (preserves bootstrap's home post; the live scout-post coordinator keeps manning it). Use existing `SensingPostRepository.Remove` — already injected.
3. Offline screen every system with `market_data` rows → `sensing_systems` verdicts + WANTED slots (no API calls; assert 0 remote fetches in the cutover test).
4. Adopt orphan probes: scout-type ships (`IsScoutType()`) with `dedicated_fleet == "scout"` NOT assigned to a surviving post (`assigned_hull`/`tour_container_id` on remaining rows) → re-tag `SensingParkedFleetTag`, insert as SPARE slots at their current `location_symbol`.

**Metrics** (`parked_sensing_metrics.go`, copy `scout_metrics.go:17-68` shape): gauges `parked_sensing_rate_req_per_sec{player_id}`, `parked_sensing_staleness_seconds{player_id,tier}` (tier hot/median/cold computed at reconcile from `LastScanAt` percentiles), `parked_sensing_slots{player_id,state}`; register via `SetGlobalParkedSensingCollector` idiom.

- [ ] **Step 1: Failing cutover test** (`run_probe_sensing_cutover_test.go`, pure fakes) — old-world fixture: 3 scout_posts (home + 2 sensing), 2 orphan "scout" probes, market_data for 4 systems (1 all-ore). Assert after one ReconcileOnce: 2 posts removed + home kept; 3 IN_SCOPE + 1 NO_WHITELIST systems; WANTED slots created; orphans re-tagged as SPARE; zero remote market fetches; zero API-client calls total (ports are fakes — count them).
- [ ] **Step 2:** run → FAIL. **Step 3:** implement cutover + new ReconcileOnce composition. Delete the old planning/rotation/post-declaration/buy internals from `run_probe_sensing_coordinator.go` (functions `neediestSensingSystem`, rotation cursor block `:935-941`, sweep-once declaration block `:780-830`, the `probebuy` per-tick call `:623-630`; keep `probeSupply` only if the cap read still wants it — prefer `CountOwnedProbes`). The file should shrink; anything the old core alone used gets deleted with it (the deleted-knobs list in the spec).
- [ ] **Step 4: Failing knob tests** — extend `command_factory_probe_sensing_test.go`: builder passes new keys through; retired keys absent from `tunableKnobsByContainerType()["PROBE_SENSING_COORDINATOR"]` (tune of `probe_budget` now rejected as unknown); `SensingTunableDefaults()` mirrors the new const block (anti-drift). `container_ops_tune_test.go`: `expansion_enabled` bounds [1,2] + description states the 1=on/2=off encoding.
- [ ] **Step 5:** implement tunables/builder/bounds + `SetLiveConfigReader` wiring in `main.go` (mirror `:1129`) + liveconfig consumption in ReconcileOnce (snapshot errors → launch config, per `liveconfig.go:37-39`).
- [ ] **Step 6: Failing recovery test** — persisted old-core config JSON (with `probe_budget`, `freshness_target_secs`) still builds and launches the new core on defaults (recovery path `daemon_server.go:1384` must not fail on stale keys — `configReader.OptionalInt` already tolerates them; pin it).
- [ ] **Step 7:** metrics collector + registration + dashboard panels (staleness by tier, sensing rate vs `spacetraders_daemon_api_rate_limit_wait_seconds` p95 — series exist, recon §4).
- [ ] **Step 8: Full gates** — from `gobot/`: `gofmt -l . | grep -v vendor` empty; `go vet ./...`; `go test ./... -race` (the real sweep — NOT `make test`). Fix until green.
- [ ] **Step 9: Commit** — `rtk git add gobot configs docs && rtk git commit --no-verify -m "feat(sensing): parked-probe core live — coordinator rewire, cutover from touring posts, knobs+liveconfig+metrics (sp-k6v8z T8)"`

---

## Deploy & grading (shipwright, after captain-gate merge of all lanes)

1. Apply migration 045 (`psql -f`) — belt to AutoMigrate's suspenders (boot failure is non-fatal, `main.go:139-142`).
2. `make restart-daemon`; verify boot: `spacetraders container list | grep probe_sensing` RUNNING, first `parked_sensing_cycle` log shows cutover counts.
3. Grade ≥30 min or one full sweep (spec Measurement): requests/completed-transaction, sensing share via source-tagged budget report, `parked_sensing_staleness_seconds` tiers vs envelope, trade p95 `rate_limit_wait` unchanged or better, steady-state sensing nav legs ≈ 0.
4. Watch the first big queue drain live (Risk 1) and the first expansion wave's charting spend.
5. bd: close sp-k6v8z with grading numbers; arming ledger note (knobs live-on-deploy; `expansion_enabled` documented 1=on/2=off).

## Self-review (done at write time)

- Spec coverage: §1 parked assets→T5/T6; §2 ledger→T3; §3 screen/floor-demotion→T4; §4 buy queue/floor→T5; §5 expansion→T7; §6 scheduler→T2/T6; §7 tagging/right-of-way→T1; §8 home→T8 cutover; knobs/cutover/metrics→T8. Divergences table maps every recon correction.
- No placeholders: every step has code or an exact-symbol instruction; the one conditional ("add `UpsertFromDetail` if no save method exists") names the exact fallback signature and file.
- Type consistency: `SensingParkedFleetTag`, `SlotLedger`, `ProbeBuyFloor`, `NonSourceRate`, `WithSource` names identical across tasks; `ExpansionEnabled` 1=on/2=off encoding stated in both T8 knob block and tune-test step.
