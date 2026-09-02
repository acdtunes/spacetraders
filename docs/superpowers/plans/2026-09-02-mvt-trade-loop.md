# MVT Trade Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each intra-system trade hull trades its claimed system until its own yield drops below the best reachable alternative net of travel, then claims the nearest profitable unoccupied system and moves; a derived-size specialist pool works the fat cross-system lanes.

**Architecture:** Pure domain package `internal/domain/trading/mvt` (ranker, departure rule, fleet stats, pool math) with no I/O. Two new tables (`trade_claims`, `mvt_transitions`) behind GORM adapters. The existing tour handler (`RunTourCoordinatorHandler`) gains an MVT branch selected per hull by the fleet tag `trade-mvt` (config key `mvt_loop`), which pins solver scope to one system and replaces the three reposition heuristics with CLAIM/TRAVEL. The trade fleet coordinator gains the specialist pool, which only re-tags idle hulls. Old and new paths coexist until every trade hull carries the new tag.

**Tech Stack:** Go 1.2x, GORM (Postgres live, sqlite in tests via `database.NewTestConnection()`), cobra CLI, existing OR-Tools routing service (untouched).

**Spec:** `docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md`

## Global Constraints

Copied from the spec and the standing books; every task inherits them.

- Keep byte-identical: the OR-Tools solver (`tour_solver.py`), the absorption ledger and its crowding guards (`cf694c5b`, `bce168ad`), the execution money guards (`1b0b60bf`), the look-back loader and toll (`0cfa0a32`), navigation/jump/refuel, the stranded-hold ladder. The ledger gains a reader and no writer. If any of these must change, stop and file a bead.
- No component may assume a minimum count of hulls, systems, or priced markets. Every pool size derives from counts. Unit tests pin 1 hull/1 system and N hulls/1 system.
- Fail toward staying put: ledger unreadable → ranker returns nothing → hull stays in TRADE.
- Money guards own every money decision (RULINGS #4). The loop chooses where; the guards decide whether.
- Knob defaults from the spec: `yield_window_sells=8`, `yield_min_sells=3`, `claim_reach_hops=2`, `specialist_fraction=0.10` (stored as `specialist_fraction_pct=10`), `fat_lane_multiple=2.0` (stored as `fat_lane_multiple_pct=200`), `specialist_cadence=1h` (stored as `specialist_cadence_minutes=60`). Parametrize; never hardcode fleet size (RULINGS #5).
- Fleet tags: `trade` (old path), `trade-mvt` (hull loop), `trade-lane` (specialist; old cross-system path with `max_tour_systems` 2). Rollback is one tag change, no restart.
- Telemetry line per state transition carries exactly: `hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason`.
- Proto untouched. No routing-service change.
- Every code change lands via worktree → `captain-gate --merge` → main (RULINGS #13). Never merge by hand. Never commit `gobot/config.yaml` or `gobot/services/routing-service/run.sh`. Never touch `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.
- Commit trailer on every commit:
  `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01PpeatLrgRdGdnYMZkZw8Ew`.
- Beads: epic `sp-htzl1`. File one child `sp-` bead per migration step (step 1 = Tasks 1–9, step 2 = Tasks 10–12, step 3 = Task 13, step 4 = Task 15) from the repo root before starting the step; `<bead>` in the commit messages below stands for that step's id.
- Ship gate before step 2 goes live: replay shows jumps down **and** margin per hull not down. Before step 3: the five-hull cohort passes on lot-matched figures over two complete hours.

## Decisions this plan makes (refinements of the spec)

| # | Decision | Why |
|---|---|---|
| 1 | Realised yields, fleet draw, lane stats and the replay read `tour_leg_telemetry` (`trading.TourLegTelemetry`, already injected into the tour handler as `h.telemetry`) rather than `transactions` | plain columns, sqlite-testable, already lot-shaped; the tour engine is the only trade engine |
| 2 | The yield EWMA updates after every sell leg; the departure decision is acted on at tour end (`afterProductiveTour`) | the leg executor has no abort channel; adding one touches the solver-execution path the spec keeps byte-identical. Tours are short with scope pinned to one system |
| 3 | Scores are credits per unit of the hull's *next load*: `expected_per_unit = min(depth, cap) × w / cap`, `w = max(0, Σ depth×spread − in_transit × fleet_draw) / Σ depth`; `travel_per_unit = hops × (toll_seconds × rate + gate_fee) / cap` | reconciles the spec's per-unit `yield_here` with its total-credits `expected_yield(S)`; keeps depth in the destination choice |
| 4 | `SystemDepth` returns lane rows; freshness (`RankerAgeCaps.Fresh`) and the per-good spread live in the pure domain | testable without a DB |
| 5 | TRAVEL persists through the existing `persistReposition` / `resumeInFlightReposition` path; `trade_claims` gains no extra column | spec §3 says restart resumes "through the existing jump-resume logic" |
| 6 | `trade_claims` primary key is `(player_id, hull)` and `era_id` is nullable, stamped from the open era like `market_data`/`gate_edges` | every table here is player-scoped; sqlite tests have no era row |
| 7 | Lane `jump_cost` is measured: mean realised transit seconds of the lane's tranches × fleet credits/sec + departure gate fee | needs no gate graph in the fleet coordinator |
| 8 | Specialist promotion picks the idle `trade-mvt` hull whose system equals the lane's source, else its sink, else the lowest symbol | the fleet coordinator has no hop graph; "closest" approximated at system grain |
| 9 | Float knobs are stored as percent ints; the cadence as minutes | codebase convention (`RepositionReachHopDecayPct`, `PlacementParkFloorPct`) |

## File map

New, one responsibility each:

| File | Owns |
|---|---|
| `gobot/internal/domain/trading/mvt/depth.go` | `LaneDepth`, `SystemDepthReader` |
| `gobot/internal/domain/trading/mvt/ranker.go` | `SystemYield`, `Rank`, `BestAlternative` |
| `gobot/internal/domain/trading/mvt/departure.go` | `YieldTracker`, `Decide` |
| `gobot/internal/domain/trading/mvt/fleetstats.go` | `ComputeFleetStats` over telemetry |
| `gobot/internal/domain/trading/mvt/pool.go` | `PoolSize`, `IsFatLane` |
| `gobot/internal/domain/trading/mvt/claim.go` | `Claim`, `ClaimRegistry` |
| `gobot/internal/domain/trading/mvt/transition.go` | `State`, `Transition`, `TransitionRecorder` |
| `gobot/internal/domain/trading/mvt/replay/replay.go` | replay core |
| `gobot/internal/adapters/persistence/models_trade_claim.go`, `trade_claim_registry.go` | `trade_claims` |
| `gobot/internal/adapters/persistence/absorption_ledger_system_depth.go` | `SystemDepths` on the ledger |
| `gobot/internal/adapters/persistence/models_mvt_transition.go`, `mvt_transition_recorder.go` | `mvt_transitions` |
| `gobot/migrations/059_add_trade_claims.{up,down}.sql`, `060_add_mvt_transitions.{up,down}.sql` | schema |
| `gobot/internal/application/trading/commands/run_tour_coordinator_mvt.go` | ports, per-hull state, `mvtRank` |
| `gobot/internal/application/trading/commands/run_tour_coordinator_mvt_shadow.go` | step-1 shadow decisions |
| `gobot/internal/application/trading/commands/run_tour_coordinator_mvt_loop.go` | CLAIM/TRAVEL/recovery/departure |
| `gobot/internal/application/trading/commands/run_trade_fleet_coordinator_specialists.go` | specialist pool |
| `gobot/cmd/mvt-replay/main.go` | replay binary |

Modified: `models.go` (AutoMigrate), `run_tour_coordinator.go`, `run_tour_coordinator_planning.go`, `run_tour_coordinator_trades.go`, `run_tour_coordinator_contract.go`, `run_trade_fleet_coordinator.go`, `_launch.go`, `_fleetview.go`, `internal/infrastructure/config/trade_fleet.go`, `internal/adapters/grpc/container_ops_tour.go`, `container_ops_trade_fleet_coordinator.go`, `command_factory_builders.go`, `cmd/spacetraders-daemon/coordinator_wiring.go`, `cmd/spacetraders-daemon/main.go`.

## Parallelism

| Wave | Tasks | Notes |
|---|---|---|
| A (parallel, disjoint files) | 1, 2, 3, 4, 5, 7, 8 | Tasks 5 and 7 both add one line to `models.go`; merge 5 first, then 7 |
| A′ | 6 (after 1) | needs `mvt.LaneDepth` |
| B | 9 (after 1, 3, 5, 6, 7, 8) | ports + shadow = migration step 1 |
| C (parallel) | 10 → 11 (after 9, 2); 12 (after 1, 2, 3); 13 (after 3, 4, 5, 8) | 10/11 and 13 touch different coordinators |
| D | 14 (after 11 deployed), then 15 (after 14 passes on every hull) | ops gate, then deletion |

Run every Go command from `gobot/`. All Go paths below are relative to `gobot/`.

---

### Task 1: Domain — lane depth types, system yield, and the ranker (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/depth.go`
- Create: `internal/domain/trading/mvt/ranker.go`
- Test: `internal/domain/trading/mvt/ranker_test.go`

**Interfaces:**
- Consumes: `trading.GoodListing` and `trading.RankerAgeCaps.Fresh(l GoodListing, now time.Time) bool` from `internal/domain/trading` (exist).
- Produces:
  - `type LaneDepth struct { Listing trading.GoodListing; BuyPlanned int; BuyResidual float64; SellPlanned int; SellResidual float64 }`
  - `type SystemDepthReader interface { SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]LaneDepth, error) }`
  - `type Hull struct { Symbol, System string; CargoCapacity int; CreditsPerSec float64 }`
  - `type Candidate struct { System string; Hops int; YieldCredits float64; DepthUnits int; InTransit int; EntryWaypoint string }`
  - `type Costs struct { TollSecondsPerHop int; GateFeeFromCurrent int64; FleetDrawPerVisit float64; FleetCreditsPerSec float64 }`
  - `type ScoredSystem struct { System string; Hops int; ExpectedPerUnit, TravelPerUnit, Score float64; EntryWaypoint string }`
  - `func SystemYield(lanes []LaneDepth, caps trading.RankerAgeCaps, now time.Time) (credits float64, units int, entryWaypoint string)`
  - `func Rank(hull Hull, cands []Candidate, costs Costs) []ScoredSystem` — sorted by `Score` desc, then `Hops` asc, then `System` asc; drops candidates with `DepthUnits <= 0`; returns nil when `hull.CargoCapacity <= 0`.
  - `func BestAlternative(ranked []ScoredSystem, current string) (ScoredSystem, bool)` — first entry whose `System != current`.

- [ ] **Step 1: Write the failing tests**

```go
package mvt

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func listing(wp, good, tradeType string, bid, ask, vol int, age time.Duration, now time.Time) trading.GoodListing {
	return trading.GoodListing{Good: good, Waypoint: wp, TradeType: tradeType, Bid: bid, Ask: ask,
		Supply: "MODERATE", Activity: "STRONG", Volume: vol, ObservedAt: now.Add(-age)}
}

func caps() trading.RankerAgeCaps {
	h := time.Hour
	return trading.RankerAgeCaps{Weak: h, Restricted: h, Growing: h, Strong: h}
}

func TestSystemYield_PerGoodSpreadTimesUnoccupiedDepth(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 60, time.Minute, now), BuyPlanned: 10, BuyResidual: 5},
		{Listing: listing("X1-A-2", "IRON", "IMPORT", 150, 160, 40, time.Minute, now), SellPlanned: 0},
		{Listing: listing("X1-A-3", "GOLD", "EXCHANGE", 500, 520, 30, time.Minute, now)},
	}
	credits, units, entry := SystemYield(lanes, caps(), now)
	// IRON: spread 150-100=50; buy depth 60-10-5=45; sell depth 40 → depth 40 → 2000 credits.
	// GOLD: only one waypoint → no lane.
	if credits != 2000 || units != 40 || entry != "X1-A-1" {
		t.Fatalf("got credits=%v units=%d entry=%q, want 2000/40/X1-A-1", credits, units, entry)
	}
}

func TestSystemYield_StaleAndNonPositiveSpreadExcluded(t *testing.T) {
	now := time.Now()
	lanes := []LaneDepth{
		{Listing: listing("X1-A-1", "IRON", "EXPORT", 90, 100, 60, 3*time.Hour, now)}, // stale source
		{Listing: listing("X1-A-2", "IRON", "IMPORT", 150, 160, 40, time.Minute, now)},
		{Listing: listing("X1-A-4", "COPPER", "EXPORT", 90, 100, 60, time.Minute, now)},
		{Listing: listing("X1-A-5", "COPPER", "IMPORT", 100, 110, 40, time.Minute, now)}, // spread 0
	}
	credits, units, _ := SystemYield(lanes, caps(), now)
	if credits != 0 || units != 0 {
		t.Fatalf("got credits=%v units=%d, want 0/0", credits, units)
	}
}

func TestRank_OneHullOneSystem_ReturnsCurrentOrNothing(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	got := Rank(hull, []Candidate{{System: "X1-A", YieldCredits: 5000, DepthUnits: 50}}, Costs{})
	if len(got) != 1 || got[0].System != "X1-A" || got[0].TravelPerUnit != 0 {
		t.Fatalf("got %+v", got)
	}
	if _, ok := BestAlternative(got, "X1-A"); ok {
		t.Fatal("no alternative must exist with a single system")
	}
	if got := Rank(hull, nil, Costs{}); len(got) != 0 {
		t.Fatalf("empty candidates must rank to nothing, got %+v", got)
	}
}

func TestRank_ScoreIsPerUnitOfNextLoadMinusTravel(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100, CreditsPerSec: 2}
	costs := Costs{TollSecondsPerHop: 100, GateFeeFromCurrent: 300}
	cands := []Candidate{
		{System: "X1-A", YieldCredits: 1000, DepthUnits: 100},             // 10/unit, travel 0
		{System: "X1-B", Hops: 2, YieldCredits: 6000, DepthUnits: 50},      // w=120; load=50 → 60/unit; travel (2*100*2 + 2*300)/100 = 10
	}
	got := Rank(hull, cands, costs)
	if got[0].System != "X1-B" || got[0].ExpectedPerUnit != 60 || got[0].TravelPerUnit != 10 || got[0].Score != 50 {
		t.Fatalf("got %+v", got[0])
	}
	if got[1].System != "X1-A" || got[1].Score != 10 {
		t.Fatalf("got %+v", got[1])
	}
}

func TestRank_FleetRateUsedWhenHullHasNone(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100, CreditsPerSec: 0}
	costs := Costs{TollSecondsPerHop: 100, FleetCreditsPerSec: 5}
	got := Rank(hull, []Candidate{{System: "X1-B", Hops: 1, YieldCredits: 10000, DepthUnits: 100}}, costs)
	if got[0].TravelPerUnit != 5 { // 1*100*5/100
		t.Fatalf("travel per unit = %v, want 5", got[0].TravelPerUnit)
	}
}

func TestRank_InTransitClaimIsPenaltyNotExclusion(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	costs := Costs{FleetDrawPerVisit: 1000}
	empty := Candidate{System: "X1-B", Hops: 1, YieldCredits: 5000, DepthUnits: 100}
	occupied := Candidate{System: "X1-C", Hops: 1, YieldCredits: 5000, DepthUnits: 100, InTransit: 1}
	got := Rank(hull, []Candidate{occupied, empty}, costs)
	if got[0].System != "X1-B" || got[1].System != "X1-C" {
		t.Fatalf("empty must beat equally rich occupied by one draw: %+v", got)
	}
	if got[1].ExpectedPerUnit != 40 { // (5000-1000)/100 per unit, load 100
		t.Fatalf("occupied per-unit = %v, want 40", got[1].ExpectedPerUnit)
	}
	heavy := Candidate{System: "X1-D", Hops: 1, YieldCredits: 500, DepthUnits: 100, InTransit: 3}
	got = Rank(hull, []Candidate{heavy}, costs)
	if len(got) != 1 || got[0].ExpectedPerUnit != 0 {
		t.Fatalf("over-penalised system floors at 0, stays listed: %+v", got)
	}
}

func TestRank_NHullsOneSystem_OrderingUnchangedByEqualPenalty(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	for _, n := range []int{1, 2, 10, 400} {
		got := Rank(hull, []Candidate{{System: "X1-A", YieldCredits: 1e6, DepthUnits: 1000, InTransit: n}}, Costs{FleetDrawPerVisit: 100})
		if len(got) != 1 || got[0].System != "X1-A" {
			t.Fatalf("n=%d: %+v", n, got)
		}
	}
}

func TestRank_TiesBreakOnHopsThenSystem(t *testing.T) {
	hull := Hull{Symbol: "H1", System: "X1-A", CargoCapacity: 100}
	got := Rank(hull, []Candidate{
		{System: "X1-C", Hops: 2, YieldCredits: 1000, DepthUnits: 100},
		{System: "X1-B", Hops: 2, YieldCredits: 1000, DepthUnits: 100},
	}, Costs{})
	if got[0].System != "X1-B" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestSystemYield|TestRank' -v`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Write `depth.go`**

```go
// Package mvt holds the pure decision logic of the marginal-value-theorem trade loop:
// system ranking, the departure rule, fleet statistics, and specialist-pool sizing.
// Nothing here performs I/O; adapters feed it rows and the tour handler acts on its answers.
package mvt

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// LaneDepth is one market-good row of a system with the absorption ledger's occupancy on
// both sides. BuyPlanned/BuyResidual sit on the source (a hull buying there);
// SellPlanned/SellResidual on the sink (a hull selling there).
type LaneDepth struct {
	Listing      trading.GoodListing
	BuyPlanned   int
	BuyResidual  float64
	SellPlanned  int
	SellResidual float64
}

// SystemDepthReader returns, for each requested system, every priced market-good row
// joined with the ledger's outstanding occupancy. A system with no rows maps to nil.
type SystemDepthReader interface {
	SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]LaneDepth, error)
}
```

- [ ] **Step 4: Write `ranker.go`**

```go
package mvt

import (
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Hull is the ranking subject. CreditsPerSec is the hull's own recent earning rate;
// zero means "no estimate yet" and Rank substitutes Costs.FleetCreditsPerSec.
type Hull struct {
	Symbol        string
	System        string
	CargoCapacity int
	CreditsPerSec float64
}

// Candidate is a reachable system with its ledger-derived yield. YieldCredits is the
// summed unoccupied depth × spread; DepthUnits the summed unoccupied depth; InTransit the
// number of other hulls that have claimed it and not arrived.
type Candidate struct {
	System        string
	Hops          int
	YieldCredits  float64
	DepthUnits    int
	InTransit     int
	EntryWaypoint string
}

// Costs are the fleet-level terms of the travel penalty and the in-transit penalty.
type Costs struct {
	TollSecondsPerHop  int
	GateFeeFromCurrent int64
	FleetDrawPerVisit  float64
	FleetCreditsPerSec float64
}

// ScoredSystem is a ranked candidate in credits per unit of the hull's next load.
type ScoredSystem struct {
	System          string
	Hops            int
	ExpectedPerUnit float64
	TravelPerUnit   float64
	Score           float64
	EntryWaypoint   string
}

const (
	tradeTypeExport   = "EXPORT"
	tradeTypeImport   = "IMPORT"
	tradeTypeExchange = "EXCHANGE"
)

// SystemYield sums, per good, the best intra-system spread times the unoccupied depth of
// its thinner side. Stale rows (per caps) and non-positive spreads contribute nothing.
// entryWaypoint is the source waypoint of the good with the largest contribution.
func SystemYield(lanes []LaneDepth, caps trading.RankerAgeCaps, now time.Time) (credits float64, units int, entryWaypoint string) {
	type side struct {
		wp    string
		price int
		depth float64
	}
	sources := map[string][]side{}
	sinks := map[string][]side{}
	for _, l := range lanes {
		if !caps.Fresh(l.Listing, now) {
			continue
		}
		g := l.Listing.Good
		if l.Listing.TradeType == tradeTypeExport || l.Listing.TradeType == tradeTypeExchange {
			d := math.Max(0, float64(l.Listing.Volume)-float64(l.BuyPlanned)-l.BuyResidual)
			sources[g] = append(sources[g], side{l.Listing.Waypoint, l.Listing.Ask, d})
		}
		if l.Listing.TradeType == tradeTypeImport || l.Listing.TradeType == tradeTypeExchange {
			d := math.Max(0, float64(l.Listing.Volume)-float64(l.SellPlanned)-l.SellResidual)
			sinks[g] = append(sinks[g], side{l.Listing.Waypoint, l.Listing.Bid, d})
		}
	}
	best := 0.0
	for g, srcs := range sources {
		for _, s := range srcs {
			for _, k := range sinks[g] {
				if k.wp == s.wp {
					continue
				}
				spread := float64(k.price - s.price)
				if spread <= 0 {
					continue
				}
				depth := math.Min(s.depth, k.depth)
				if depth <= 0 {
					continue
				}
				c := depth * spread
				credits += c
				units += int(depth)
				if c > best {
					best, entryWaypoint = c, s.wp
				}
			}
		}
	}
	return credits, units, entryWaypoint
}

// Rank scores every candidate in credits per unit of the hull's next load, net of travel,
// and sorts best-first. A candidate with no depth is dropped, never scored zero.
func Rank(hull Hull, cands []Candidate, costs Costs) []ScoredSystem {
	if hull.CargoCapacity <= 0 {
		return nil
	}
	cap := float64(hull.CargoCapacity)
	rate := hull.CreditsPerSec
	if rate <= 0 {
		rate = costs.FleetCreditsPerSec
	}
	out := make([]ScoredSystem, 0, len(cands))
	for _, c := range cands {
		if c.DepthUnits <= 0 {
			continue
		}
		credits := math.Max(0, c.YieldCredits-float64(c.InTransit)*costs.FleetDrawPerVisit)
		w := credits / float64(c.DepthUnits)
		load := math.Min(float64(c.DepthUnits), cap)
		expected := load * w / cap
		travel := 0.0
		if c.System != hull.System {
			hops := float64(c.Hops)
			travel = (hops*float64(costs.TollSecondsPerHop)*rate + hops*float64(costs.GateFeeFromCurrent)) / cap
		}
		out = append(out, ScoredSystem{System: c.System, Hops: c.Hops, ExpectedPerUnit: expected,
			TravelPerUnit: travel, Score: expected - travel, EntryWaypoint: c.EntryWaypoint})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].System < out[j].System
	})
	return out
}

// BestAlternative is the best-ranked system other than current.
func BestAlternative(ranked []ScoredSystem, current string) (ScoredSystem, bool) {
	for _, s := range ranked {
		if s.System != current {
			return s, true
		}
	}
	return ScoredSystem{}, false
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestSystemYield|TestRank' -v`
Expected: PASS (8 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/domain/trading/mvt/depth.go internal/domain/trading/mvt/ranker.go internal/domain/trading/mvt/ranker_test.go
git commit -m "feat(mvt): pure system ranker with per-good spread yield and in-transit penalty (<bead>)"
```

### Task 2: Domain — yield tracker and the departure rule (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/departure.go`
- Test: `internal/domain/trading/mvt/departure_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (pure numbers in, decision out).
- Produces:
  - `func NewYieldTracker(windowSells, minSells int) *YieldTracker`
  - `func (t *YieldTracker) Observe(marginPerUnit float64, units int, at time.Time)`
  - `func (t *YieldTracker) Estimate() (perUnit float64, ok bool)` — `ok=false` while `Sells() < minSells`
  - `func (t *YieldTracker) Sells() int`
  - `func (t *YieldTracker) CreditsPerSec(now time.Time) float64` — 0 until two observations and positive elapsed time
  - `func (t *YieldTracker) Reset()`
  - `type Decision struct { Leave bool; Reason string; YieldHere, BestAlternative float64 }`
  - `func Decide(t *YieldTracker, bestAltScore float64, hasAlt bool) Decision`
  - Reason constants: `ReasonColdStart = "cold_start"`, `ReasonNoAlternative = "no_alternative"`, `ReasonStay = "stay"`, `ReasonYieldBelow = "yield_below_alternative"`.

- [ ] **Step 1: Write the failing tests**

```go
package mvt

import (
	"testing"
	"time"
)

func TestYieldTracker_EWMAAndColdStart(t *testing.T) {
	tr := NewYieldTracker(8, 3) // alpha = 2/9
	t0 := time.Unix(1_000_000, 0)
	tr.Observe(100, 10, t0)
	if _, ok := tr.Estimate(); ok {
		t.Fatal("one sell must not yield an estimate at minSells=3")
	}
	tr.Observe(100, 10, t0.Add(time.Minute))
	tr.Observe(190, 10, t0.Add(2*time.Minute))
	est, ok := tr.Estimate()
	if !ok {
		t.Fatal("three sells must produce an estimate")
	}
	// ewma: 100 → 100 → 100 + 2/9*(190-100) = 120
	if est < 119.99 || est > 120.01 {
		t.Fatalf("estimate = %v, want 120", est)
	}
	if tr.Sells() != 3 {
		t.Fatalf("sells = %d", tr.Sells())
	}
}

func TestYieldTracker_CreditsPerSec(t *testing.T) {
	tr := NewYieldTracker(8, 1)
	t0 := time.Unix(1_000_000, 0)
	if tr.CreditsPerSec(t0) != 0 {
		t.Fatal("no observations → 0")
	}
	tr.Observe(50, 10, t0) // 500 credits
	if tr.CreditsPerSec(t0.Add(time.Minute)) != 0 {
		t.Fatal("a single observation has no rate")
	}
	tr.Observe(50, 10, t0.Add(100*time.Second)) // 1000 credits over 100 s
	if got := tr.CreditsPerSec(t0.Add(100 * time.Second)); got != 10 {
		t.Fatalf("rate = %v, want 10", got)
	}
	tr.Reset()
	if tr.Sells() != 0 || tr.CreditsPerSec(t0.Add(time.Hour)) != 0 {
		t.Fatal("reset must clear everything")
	}
}

func TestDecide_Table(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	warm := func(perUnit float64) *YieldTracker {
		tr := NewYieldTracker(8, 3)
		for i := 0; i < 3; i++ {
			tr.Observe(perUnit, 10, t0.Add(time.Duration(i)*time.Minute))
		}
		return tr
	}
	cold := func() *YieldTracker {
		tr := NewYieldTracker(8, 3)
		tr.Observe(1, 10, t0)
		return tr
	}
	cases := []struct {
		name   string
		tr     *YieldTracker
		alt    float64
		hasAlt bool
		leave  bool
		reason string
	}{
		{"cold start cannot leave on yield", cold(), 1_000_000, true, false, ReasonColdStart},
		{"no alternative stays", warm(100), 0, false, false, ReasonNoAlternative},
		{"yield at or above alternative stays", warm(100), 100, true, false, ReasonStay},
		{"yield below alternative leaves", warm(100), 100.01, true, true, ReasonYieldBelow},
		{"negative alternative never wins", warm(-5), -1, true, true, ReasonYieldBelow},
	}
	for _, tc := range cases {
		d := Decide(tc.tr, tc.alt, tc.hasAlt)
		if d.Leave != tc.leave || d.Reason != tc.reason {
			t.Fatalf("%s: got %+v", tc.name, d)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestYieldTracker|TestDecide' -v`
Expected: FAIL — undefined `NewYieldTracker`, `Decide`.

- [ ] **Step 3: Write `departure.go`**

```go
package mvt

import "time"

// Departure reasons; each is written verbatim into the transition telemetry line.
const (
	ReasonColdStart     = "cold_start"
	ReasonNoAlternative = "no_alternative"
	ReasonStay          = "stay"
	ReasonYieldBelow    = "yield_below_alternative"
)

// YieldTracker is the hull's own view of the ground it stands on: an exponentially
// weighted moving average of realised margin per unit over its sells in the current
// system, plus the credits-per-second rate the travel cost is priced against.
// It is never persisted; a restart resets it and the cold-start guard applies.
type YieldTracker struct {
	alpha    float64
	minSells int
	sells    int
	ewma     float64
	credits  float64
	firstAt  time.Time
	lastAt   time.Time
}

// NewYieldTracker builds a tracker with alpha = 2/(windowSells+1). windowSells < 1 is
// treated as 1 (alpha = 1: the latest sell is the estimate).
func NewYieldTracker(windowSells, minSells int) *YieldTracker {
	if windowSells < 1 {
		windowSells = 1
	}
	if minSells < 1 {
		minSells = 1
	}
	return &YieldTracker{alpha: 2 / float64(windowSells+1), minSells: minSells}
}

// Observe records one sell leg's realised margin per unit.
func (t *YieldTracker) Observe(marginPerUnit float64, units int, at time.Time) {
	if t.sells == 0 {
		t.ewma = marginPerUnit
		t.firstAt = at
	} else {
		t.ewma = t.alpha*marginPerUnit + (1-t.alpha)*t.ewma
	}
	t.sells++
	t.credits += marginPerUnit * float64(units)
	t.lastAt = at
}

// Estimate is the EWMA; ok is false below minSells (the cold-start guard).
func (t *YieldTracker) Estimate() (float64, bool) {
	if t.sells < t.minSells {
		return 0, false
	}
	return t.ewma, true
}

// Sells is the number of observations since the last Reset.
func (t *YieldTracker) Sells() int { return t.sells }

// CreditsPerSec is realised margin over the span between the first observation and now.
// It needs at least two observations and a positive span; otherwise 0 (caller falls back
// to the fleet rate).
func (t *YieldTracker) CreditsPerSec(now time.Time) float64 {
	if t.sells < 2 {
		return 0
	}
	span := now.Sub(t.firstAt).Seconds()
	if span <= 0 {
		return 0
	}
	return t.credits / span
}

// Reset clears the tracker (arrival in a new system).
func (t *YieldTracker) Reset() { *t = YieldTracker{alpha: t.alpha, minSells: t.minSells} }

// Decision is the departure verdict with the numbers that produced it.
type Decision struct {
	Leave           bool
	Reason          string
	YieldHere       float64
	BestAlternative float64
}

// Decide applies the rule: leave iff yield_here < best alternative score (already net of
// travel). A tracker below minSells cannot leave on yield.
func Decide(t *YieldTracker, bestAltScore float64, hasAlt bool) Decision {
	here, ok := t.Estimate()
	d := Decision{YieldHere: here, BestAlternative: bestAltScore}
	switch {
	case !ok:
		d.Reason = ReasonColdStart
	case !hasAlt:
		d.Reason = ReasonNoAlternative
	case here < bestAltScore:
		d.Leave, d.Reason = true, ReasonYieldBelow
	default:
		d.Reason = ReasonStay
	}
	return d
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestYieldTracker|TestDecide' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/trading/mvt/departure.go internal/domain/trading/mvt/departure_test.go
git commit -m "feat(mvt): yield EWMA tracker and departure rule with cold-start guard (<bead>)"
```

### Task 3: Domain — fleet statistics from tour telemetry (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/fleetstats.go`
- Test: `internal/domain/trading/mvt/fleetstats_test.go`

**Interfaces:**
- Consumes: `trading.TourLegTelemetry{ShipSymbol, Waypoint, Good, IsBuy, RealizedUnits, RealizedUnitPrice, RealizedAt, ...}` (exists) and `shared.ExtractSystemSymbol(waypoint string) string` (`internal/domain/shared/waypoint.go:143`).
- Produces:
  - `type LaneStat struct { Source, Sink, Good string; Tranches int; MarginPerTranche float64; MeanTransitSeconds float64 }` — source and sink are **system** symbols.
  - `type FleetStats struct { Hulls int; MarginTotal float64; MeanMarginPerSystemVisit float64; IntraMarginPerTranche float64; CreditsPerHullSec float64; PerHullMargin map[string]float64; Lanes []LaneStat }`
  - `func ComputeFleetStats(legs []trading.TourLegTelemetry, window time.Duration) FleetStats`

Definitions (the tests pin them):
- Lots are FIFO per (hull, good) from buy legs; a sell consumes lots and its margin is `Σ (sellPrice − lotPrice) × consumed`. A sell with no lot is skipped.
- A tranche is one sell leg. It is intra-system when the majority of its units came from lots bought in the sell's system; otherwise it belongs to lane `(lot system, sell system, good)` with transit = sell time − lot time, unit-weighted.
- A system-visit is a maximal run of a hull's consecutive legs (buys or sells, in `RealizedAt` order) in one system. `MeanMarginPerSystemVisit = Σ visit margins / visits`.
- `CreditsPerHullSec = MarginTotal / (Hulls × window.Seconds())`; 0 when either factor is 0.
- `Lanes` sorted by `MarginPerTranche` desc, then `Source`, `Sink`, `Good`.

- [ ] **Step 1: Write the failing tests**

```go
package mvt

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func leg(ship, wp, good string, isBuy bool, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: ship, Waypoint: wp, Good: good, IsBuy: isBuy,
		RealizedUnits: units, RealizedUnitPrice: price, PlannedUnits: units, PlannedUnitPrice: price,
		PlannedAt: at, RealizedAt: at, TourID: "t", PlayerID: 1}
}

func TestComputeFleetStats_IntraAndLaneTranches(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	legs := []trading.TourLegTelemetry{
		// H1: intra-system visit in X1-A: buy 10 @100, sell 10 @150 → margin 500, one intra tranche
		leg("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		leg("H1", "X1-A-2", "IRON", false, 10, 150, t0.Add(10*time.Minute)),
		// H1: cross-system: buy 20 @200 in X1-A, sell 20 @400 in X1-B after 600 s → margin 4000 on lane A→B
		leg("H1", "X1-A-1", "GOLD", true, 20, 200, t0.Add(20*time.Minute)),
		leg("H1", "X1-B-1", "GOLD", false, 20, 400, t0.Add(30*time.Minute)),
		// H2: sell with no lot → skipped
		leg("H2", "X1-C-1", "IRON", false, 5, 999, t0),
	}
	s := ComputeFleetStats(legs, time.Hour)
	if s.Hulls != 2 || s.MarginTotal != 4500 {
		t.Fatalf("hulls=%d total=%v", s.Hulls, s.MarginTotal)
	}
	// visits: H1@A (margin 500), H1@B (4000), H2@C (0) → mean 1500
	if s.MeanMarginPerSystemVisit != 1500 {
		t.Fatalf("mean per visit = %v, want 1500", s.MeanMarginPerSystemVisit)
	}
	if s.IntraMarginPerTranche != 500 {
		t.Fatalf("intra per tranche = %v, want 500", s.IntraMarginPerTranche)
	}
	if s.CreditsPerHullSec != 4500/(2*3600.0) {
		t.Fatalf("credits/hull/sec = %v", s.CreditsPerHullSec)
	}
	if s.PerHullMargin["H1"] != 4500 || s.PerHullMargin["H2"] != 0 {
		t.Fatalf("per hull = %v", s.PerHullMargin)
	}
	if len(s.Lanes) != 1 || s.Lanes[0] != (LaneStat{Source: "X1-A", Sink: "X1-B", Good: "GOLD", Tranches: 1, MarginPerTranche: 4000, MeanTransitSeconds: 600}) {
		t.Fatalf("lanes = %+v", s.Lanes)
	}
}

func TestComputeFleetStats_EmptyAndZeroWindow(t *testing.T) {
	s := ComputeFleetStats(nil, time.Hour)
	if s.Hulls != 0 || s.CreditsPerHullSec != 0 || s.MeanMarginPerSystemVisit != 0 || len(s.Lanes) != 0 {
		t.Fatalf("empty stats = %+v", s)
	}
	s = ComputeFleetStats([]trading.TourLegTelemetry{leg("H1", "X1-A-1", "IRON", true, 1, 1, time.Unix(0, 0))}, 0)
	if s.CreditsPerHullSec != 0 {
		t.Fatal("zero window must not divide by zero")
	}
}

func TestComputeFleetStats_FIFOAcrossPartialLots(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	legs := []trading.TourLegTelemetry{
		leg("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		leg("H1", "X1-A-1", "IRON", true, 10, 200, t0.Add(time.Minute)),
		leg("H1", "X1-A-2", "IRON", false, 15, 300, t0.Add(2*time.Minute)), // 10@100 + 5@200 → 2000+500
	}
	s := ComputeFleetStats(legs, time.Hour)
	if s.MarginTotal != 2500 {
		t.Fatalf("margin = %v, want 2500", s.MarginTotal)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/trading/mvt/ -run TestComputeFleetStats -v`
Expected: FAIL — undefined `ComputeFleetStats`.

- [ ] **Step 3: Write `fleetstats.go`**

```go
package mvt

import (
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// LaneStat is a realised cross-system (source system, sink system, good) lane.
type LaneStat struct {
	Source, Sink, Good string
	Tranches           int
	MarginPerTranche   float64
	MeanTransitSeconds float64
}

// FleetStats are the fleet-level terms the ranker and the specialist pool read.
type FleetStats struct {
	Hulls                    int
	MarginTotal              float64
	MeanMarginPerSystemVisit float64
	IntraMarginPerTranche    float64
	CreditsPerHullSec        float64
	PerHullMargin            map[string]float64
	Lanes                    []LaneStat
}

type lot struct {
	units  int
	price  int
	system string
	at     time.Time
}

type laneAcc struct {
	tranches int
	margin   float64
	transit  float64 // unit-weighted seconds
	units    int
}

// ComputeFleetStats lot-matches the legs FIFO per (hull, good) and derives visit, tranche,
// lane and rate statistics. window is the span the legs were collected over.
func ComputeFleetStats(legs []trading.TourLegTelemetry, window time.Duration) FleetStats {
	byHull := map[string][]trading.TourLegTelemetry{}
	for _, l := range legs {
		if l.RealizedUnits <= 0 {
			continue
		}
		byHull[l.ShipSymbol] = append(byHull[l.ShipSymbol], l)
	}
	st := FleetStats{PerHullMargin: map[string]float64{}}
	lanes := map[[3]string]*laneAcc{}
	visits, visitMargin := 0, 0.0
	intraTranches, intraMargin := 0, 0.0
	for hull, hl := range byHull {
		sort.Slice(hl, func(i, j int) bool { return hl[i].RealizedAt.Before(hl[j].RealizedAt) })
		lots := map[string][]lot{}
		curSystem, curMargin := "", 0.0
		for _, l := range hl {
			sys := shared.ExtractSystemSymbol(l.Waypoint)
			if sys != curSystem {
				if curSystem != "" {
					visits++
					visitMargin += curMargin
				}
				curSystem, curMargin = sys, 0
			}
			if l.IsBuy {
				lots[l.Good] = append(lots[l.Good], lot{l.RealizedUnits, l.RealizedUnitPrice, sys, l.RealizedAt})
				continue
			}
			q := lots[l.Good]
			if len(q) == 0 {
				continue
			}
			need := l.RealizedUnits
			margin, intraUnits, crossUnits := 0.0, 0, 0
			cross := map[string]*laneAcc{} // source system → accumulator for this sell
			for need > 0 && len(q) > 0 {
				take := q[0].units
				if take > need {
					take = need
				}
				m := float64(l.RealizedUnitPrice-q[0].price) * float64(take)
				margin += m
				if q[0].system == sys {
					intraUnits += take
				} else {
					crossUnits += take
					a := cross[q[0].system]
					if a == nil {
						a = &laneAcc{}
						cross[q[0].system] = a
					}
					a.margin += m
					a.units += take
					a.transit += l.RealizedAt.Sub(q[0].at).Seconds() * float64(take)
				}
				q[0].units -= take
				need -= take
				if q[0].units == 0 {
					q = q[1:]
				}
			}
			lots[l.Good] = q
			st.MarginTotal += margin
			st.PerHullMargin[hull] += margin
			curMargin += margin
			if intraUnits >= crossUnits {
				intraTranches++
				intraMargin += margin
				continue
			}
			for src, a := range cross {
				key := [3]string{src, sys, l.Good}
				acc := lanes[key]
				if acc == nil {
					acc = &laneAcc{}
					lanes[key] = acc
				}
				acc.tranches++
				acc.margin += a.margin
				acc.transit += a.transit
				acc.units += a.units
			}
		}
		if curSystem != "" {
			visits++
			visitMargin += curMargin
		}
		if _, ok := st.PerHullMargin[hull]; !ok {
			st.PerHullMargin[hull] = 0
		}
	}
	st.Hulls = len(byHull)
	if visits > 0 {
		st.MeanMarginPerSystemVisit = visitMargin / float64(visits)
	}
	if intraTranches > 0 {
		st.IntraMarginPerTranche = intraMargin / float64(intraTranches)
	}
	if st.Hulls > 0 && window > 0 {
		st.CreditsPerHullSec = st.MarginTotal / (float64(st.Hulls) * window.Seconds())
	}
	for k, a := range lanes {
		ls := LaneStat{Source: k[0], Sink: k[1], Good: k[2], Tranches: a.tranches, MarginPerTranche: a.margin / float64(a.tranches)}
		if a.units > 0 {
			ls.MeanTransitSeconds = a.transit / float64(a.units)
		}
		st.Lanes = append(st.Lanes, ls)
	}
	sort.Slice(st.Lanes, func(i, j int) bool {
		a, b := st.Lanes[i], st.Lanes[j]
		if a.MarginPerTranche != b.MarginPerTranche {
			return a.MarginPerTranche > b.MarginPerTranche
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Sink != b.Sink {
			return a.Sink < b.Sink
		}
		return a.Good < b.Good
	})
	return st
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/domain/trading/mvt/ -run TestComputeFleetStats -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/trading/mvt/fleetstats.go internal/domain/trading/mvt/fleetstats_test.go
git commit -m "feat(mvt): lot-matched fleet statistics from tour telemetry (<bead>)"
```

### Task 4: Domain — specialist pool math (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/pool.go`
- Test: `internal/domain/trading/mvt/pool_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces:
  - `func PoolSize(fatLanes, hulls, fractionPct int) int` — `min(fatLanes, floor(hulls × fractionPct / 100))`, never negative.
  - `func IsFatLane(marginPerTranche, transitSeconds, fleetCreditsPerSec float64, gateFee int64, intraMarginPerTranche float64, multiplePct int) bool` — `marginPerTranche − (transitSeconds × fleetCreditsPerSec + gateFee) > multiplePct/100 × intraMarginPerTranche`; false when `intraMarginPerTranche <= 0` (no baseline → no specialists).

- [ ] **Step 1: Write the failing tests**

```go
package mvt

import "testing"

func TestPoolSize_ScaleAxis(t *testing.T) {
	cases := []struct{ fat, hulls, pct, want int }{
		{5, 1, 10, 0},    // N=1 → floor(0.1) = 0
		{5, 2, 10, 0},
		{5, 10, 10, 1},
		{5, 40, 10, 4},
		{5, 400, 10, 5},  // fat lanes cap the pool
		{0, 400, 10, 0},  // no fat lanes → no specialists
		{3, 400, 0, 0},   // fraction 0 disables the pool
		{-1, 10, 10, 0},  // never negative
	}
	for _, c := range cases {
		if got := PoolSize(c.fat, c.hulls, c.pct); got != c.want {
			t.Fatalf("PoolSize(%d,%d,%d) = %d, want %d", c.fat, c.hulls, c.pct, got, c.want)
		}
	}
}

func TestIsFatLane(t *testing.T) {
	// intra baseline 1000/tranche, multiple 2.0 → net must exceed 2000
	if !IsFatLane(5000, 600, 2, 500, 1000, 200) { // 5000 - (1200 + 500) = 3300 > 2000
		t.Fatal("lane clearing 2× intra after jump cost is fat")
	}
	if IsFatLane(3600, 600, 2, 500, 1000, 200) { // 3600 - 1700 = 1900 ≤ 2000
		t.Fatal("lane below 2× intra is not fat")
	}
	if IsFatLane(1e9, 0, 0, 0, 0, 200) {
		t.Fatal("no intra baseline → never fat (fail toward no specialists)")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestPoolSize|TestIsFatLane' -v`
Expected: FAIL — undefined `PoolSize`, `IsFatLane`.

- [ ] **Step 3: Write `pool.go`**

```go
package mvt

// PoolSize is min(count of fat lanes, floor(hulls × fractionPct / 100)). At one hull and
// ten percent the floor is zero: no specialists.
func PoolSize(fatLanes, hulls, fractionPct int) int {
	if fatLanes < 0 || hulls < 0 || fractionPct < 0 {
		return 0
	}
	byFraction := hulls * fractionPct / 100
	if fatLanes < byFraction {
		return fatLanes
	}
	return byFraction
}

// IsFatLane reports whether a lane's margin per tranche, net of the measured jump cost,
// clears multiplePct/100 times the fleet's intra-system margin per tranche. With no
// intra-system baseline nothing qualifies.
func IsFatLane(marginPerTranche, transitSeconds, fleetCreditsPerSec float64, gateFee int64, intraMarginPerTranche float64, multiplePct int) bool {
	if intraMarginPerTranche <= 0 {
		return false
	}
	jumpCost := transitSeconds*fleetCreditsPerSec + float64(gateFee)
	return marginPerTranche-jumpCost > float64(multiplePct)/100*intraMarginPerTranche
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/domain/trading/mvt/ -run 'TestPoolSize|TestIsFatLane' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/trading/mvt/pool.go internal/domain/trading/mvt/pool_test.go
git commit -m "feat(mvt): derived specialist pool size and fat-lane qualifier (<bead>)"
```

### Task 5: Claim registry — migration, model, adapter (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/claim.go`
- Create: `migrations/059_add_trade_claims.up.sql`, `migrations/059_add_trade_claims.down.sql`
- Create: `internal/adapters/persistence/models_trade_claim.go`
- Create: `internal/adapters/persistence/trade_claim_registry.go`
- Modify: `internal/adapters/persistence/models.go:45` (AutoMigrate list, after `&UnreadableHullModel{},`)
- Test: `internal/adapters/persistence/trade_claim_registry_test.go` (package `persistence_test`)

**Interfaces:**
- Consumes: `database.NewTestConnection()` (`internal/infrastructure/database/connection.go:132`, sqlite + AutoMigrate), `persistence.PlayerModel{AgentSymbol, Token, CreatedAt}`, `EraModel{EraID}` with `closed_at` (open-era lookup pattern at `internal/adapters/persistence/market_repository.go:271`).
- Produces:
  - `type Claim struct { Hull, System string; ClaimedAt time.Time; ArrivedAt *time.Time }`
  - `type ClaimRegistry interface { Upsert(ctx, playerID int, hull, system string, at time.Time) error; MarkArrived(ctx, playerID int, hull string, at time.Time) error; Release(ctx, playerID int, hull string) error; Get(ctx, playerID int, hull string) (Claim, bool, error); InTransit(ctx, playerID int) (map[string]int, error) }`
  - `persistence.NewTradeClaimRegistry(db *gorm.DB) *TradeClaimRegistryGORM` implementing `mvt.ClaimRegistry`.

- [ ] **Step 1: Write `claim.go` (domain types)**

```go
package mvt

import (
	"context"
	"time"
)

// Claim is a hull's durable statement of which system it is working or travelling to.
// ArrivedAt nil means in transit. One row per hull; a penalty for the ranker, never a lock.
type Claim struct {
	Hull      string
	System    string
	ClaimedAt time.Time
	ArrivedAt *time.Time
}

// ClaimRegistry is the durable claim table. Upsert resets ArrivedAt to nil.
type ClaimRegistry interface {
	Upsert(ctx context.Context, playerID int, hull, system string, at time.Time) error
	MarkArrived(ctx context.Context, playerID int, hull string, at time.Time) error
	Release(ctx context.Context, playerID int, hull string) error
	Get(ctx context.Context, playerID int, hull string) (Claim, bool, error)
	// InTransit counts unarrived claims per system for the open era.
	InTransit(ctx context.Context, playerID int) (map[string]int, error)
}
```

- [ ] **Step 2: Write the failing adapter test**

```go
package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func newClaimTestDB(t *testing.T) (*persistence.TradeClaimRegistryGORM, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "MVT-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewTradeClaimRegistry(db), player.ID
}

func TestTradeClaimRegistry_UpsertArriveReleaseInTransit(t *testing.T) {
	var _ mvt.ClaimRegistry = (*persistence.TradeClaimRegistryGORM)(nil)
	reg, pid := newClaimTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, ok, err := reg.Get(ctx, pid, "H1")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, reg.Upsert(ctx, pid, "H1", "X1-B", now))
	require.NoError(t, reg.Upsert(ctx, pid, "H2", "X1-B", now))
	require.NoError(t, reg.Upsert(ctx, pid, "H3", "X1-C", now))
	inTransit, err := reg.InTransit(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"X1-B": 2, "X1-C": 1}, inTransit)

	require.NoError(t, reg.MarkArrived(ctx, pid, "H1", now.Add(time.Minute)))
	c, ok, err := reg.Get(ctx, pid, "H1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "X1-B", c.System)
	require.NotNil(t, c.ArrivedAt)
	inTransit, err = reg.InTransit(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"X1-B": 1, "X1-C": 1}, inTransit)

	// Re-claiming resets arrival.
	require.NoError(t, reg.Upsert(ctx, pid, "H1", "X1-D", now.Add(2*time.Minute)))
	c, _, _ = reg.Get(ctx, pid, "H1")
	require.Equal(t, "X1-D", c.System)
	require.Nil(t, c.ArrivedAt)

	require.NoError(t, reg.Release(ctx, pid, "H1"))
	_, ok, _ = reg.Get(ctx, pid, "H1")
	require.False(t, ok)
	require.NoError(t, reg.Release(ctx, pid, "H1")) // idempotent

	// Another player's rows are invisible.
	other, err := reg.InTransit(ctx, pid+1)
	require.NoError(t, err)
	require.Empty(t, other)
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/adapters/persistence/ -run TestTradeClaimRegistry -v`
Expected: FAIL — undefined `NewTradeClaimRegistry`.

- [ ] **Step 4: Write the migration**

`migrations/059_add_trade_claims.up.sql`:

```sql
-- MVT trade loop: one durable claim per hull (spec docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md §3).
CREATE TABLE IF NOT EXISTS trade_claims (
    player_id   BIGINT      NOT NULL,
    hull        VARCHAR(50) NOT NULL,
    system      VARCHAR(20) NOT NULL,
    claimed_at  TIMESTAMPTZ NOT NULL,
    arrived_at  TIMESTAMPTZ,
    era_id      BIGINT,
    PRIMARY KEY (player_id, hull)
);
CREATE INDEX IF NOT EXISTS idx_trade_claims_in_transit
    ON trade_claims (player_id, system)
    WHERE arrived_at IS NULL;
COMMENT ON TABLE trade_claims IS 'MVT trade loop: the system each intra-system hull is working (arrived_at set) or travelling to (arrived_at NULL). A ranking penalty, never a lock.';
```

`migrations/059_add_trade_claims.down.sql`:

```sql
DROP INDEX IF EXISTS idx_trade_claims_in_transit;
DROP TABLE IF EXISTS trade_claims;
```

- [ ] **Step 5: Write the model and register it**

`internal/adapters/persistence/models_trade_claim.go`:

```go
package persistence

import "time"

// TradeClaimModel mirrors migrations/059_add_trade_claims.up.sql.
type TradeClaimModel struct {
	PlayerID  int        `gorm:"column:player_id;primaryKey"`
	Hull      string     `gorm:"column:hull;primaryKey;size:50"`
	System    string     `gorm:"column:system;size:20;not null"`
	ClaimedAt time.Time  `gorm:"column:claimed_at;not null"`
	ArrivedAt *time.Time `gorm:"column:arrived_at"`
	EraID     *int       `gorm:"column:era_id"`
}

func (TradeClaimModel) TableName() string { return "trade_claims" }
```

In `internal/adapters/persistence/models.go`, add `&TradeClaimModel{},` immediately after `&UnreadableHullModel{},` (line 45).

- [ ] **Step 6: Write the adapter**

`internal/adapters/persistence/trade_claim_registry.go`:

```go
package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// TradeClaimRegistryGORM is the trade_claims adapter. Rows are stamped with the open era
// and reads are scoped to it, so a dead era's claims never penalise a live ranking.
type TradeClaimRegistryGORM struct {
	db *gorm.DB
}

var _ mvt.ClaimRegistry = (*TradeClaimRegistryGORM)(nil)

func NewTradeClaimRegistry(db *gorm.DB) *TradeClaimRegistryGORM {
	return &TradeClaimRegistryGORM{db: db}
}

func (r *TradeClaimRegistryGORM) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

func (r *TradeClaimRegistryGORM) eraScope(ctx context.Context, q *gorm.DB) *gorm.DB {
	if era := r.openEraID(ctx); era != nil {
		return q.Where("era_id = ?", *era)
	}
	return q.Where("era_id IS NULL")
}

func (r *TradeClaimRegistryGORM) Upsert(ctx context.Context, playerID int, hull, system string, at time.Time) error {
	if r.db == nil {
		return errors.New("no database wired for the trade claim registry")
	}
	row := TradeClaimModel{PlayerID: playerID, Hull: hull, System: system, ClaimedAt: at, ArrivedAt: nil, EraID: r.openEraID(ctx)}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "player_id"}, {Name: "hull"}},
		DoUpdates: clause.AssignmentColumns([]string{"system", "claimed_at", "arrived_at", "era_id"}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert trade claim %s→%s: %w", hull, system, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) MarkArrived(ctx context.Context, playerID int, hull string, at time.Time) error {
	if err := r.db.WithContext(ctx).Model(&TradeClaimModel{}).
		Where("player_id = ? AND hull = ?", playerID, hull).
		Update("arrived_at", at).Error; err != nil {
		return fmt.Errorf("mark trade claim arrived %s: %w", hull, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) Release(ctx context.Context, playerID int, hull string) error {
	if err := r.db.WithContext(ctx).Where("player_id = ? AND hull = ?", playerID, hull).Delete(&TradeClaimModel{}).Error; err != nil {
		return fmt.Errorf("release trade claim %s: %w", hull, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) Get(ctx context.Context, playerID int, hull string) (mvt.Claim, bool, error) {
	var row TradeClaimModel
	err := r.eraScope(ctx, r.db.WithContext(ctx).Where("player_id = ? AND hull = ?", playerID, hull)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return mvt.Claim{}, false, nil
	}
	if err != nil {
		return mvt.Claim{}, false, fmt.Errorf("read trade claim %s: %w", hull, err)
	}
	return mvt.Claim{Hull: row.Hull, System: row.System, ClaimedAt: row.ClaimedAt, ArrivedAt: row.ArrivedAt}, true, nil
}

func (r *TradeClaimRegistryGORM) InTransit(ctx context.Context, playerID int) (map[string]int, error) {
	var rows []struct {
		System string
		N      int
	}
	q := r.eraScope(ctx, r.db.WithContext(ctx).Model(&TradeClaimModel{}).
		Select("system, COUNT(*) AS n").
		Where("player_id = ? AND arrived_at IS NULL", playerID)).
		Group("system")
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count in-transit trade claims: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.System] = row.N
	}
	return out, nil
}
```

- [ ] **Step 7: Run the test**

Run: `go test ./internal/adapters/persistence/ -run TestTradeClaimRegistry -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/domain/trading/mvt/claim.go migrations/059_add_trade_claims.up.sql migrations/059_add_trade_claims.down.sql internal/adapters/persistence/models_trade_claim.go internal/adapters/persistence/trade_claim_registry.go internal/adapters/persistence/trade_claim_registry_test.go internal/adapters/persistence/models.go
git commit -m "feat(mvt): era-scoped trade_claims registry (<bead>)"
```

### Task 6: `SystemDepths` reader on the absorption ledger (Wave A′, after Task 1)

**Files:**
- Create: `internal/adapters/persistence/absorption_ledger_system_depth.go`
- Test: `internal/adapters/persistence/absorption_ledger_system_depth_test.go` (package `persistence`, internal — it needs the unexported state constants)

**Interfaces:**
- Consumes: `mvt.LaneDepth`, `mvt.SystemDepthReader` (Task 1); `(*AbsorptionLedgerGORM).Outstanding(ctx, playerID) (map[absorption.LaneKey]absorption.KeyOccupancy, error)` (`absorption_ledger_repository.go:315`); `absorption.LaneKey{Waypoint, Good, Side}`, `absorption.SideBuy = "buy"`, `absorption.SideSell = "sell"`; `MarketData` GORM model (`models_market.go`, table `market_data`, columns `player_id, waypoint_symbol, good_symbol, supply, activity, purchase_price, sell_price, trade_volume, trade_type, last_updated`); `MarketAbsorptionLedgerModel` rows with `State` in `absorptionStatePlanned` / `absorptionStateExecuted` (read the model file for the exact field list before writing the test seed).
- Produces: `func (r *AbsorptionLedgerGORM) SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]mvt.LaneDepth, error)`.

Mapping `MarketData` → `trading.GoodListing`: `Good=GoodSymbol, Waypoint=WaypointSymbol, TradeType=*TradeType (""), Bid=SellPrice, Ask=PurchasePrice, Supply=*Supply, Activity=*Activity, Volume=TradeVolume, ObservedAt=LastUpdated`. Before writing it, run `grep -rn "trading.GoodListing{" internal/` and reuse the existing conversion helper if one exists in the persistence package; if it lives in another package, copy its field mapping exactly.

- [ ] **Step 1: Write the failing test**

```go
package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestAbsorptionLedger_SystemDepths_JoinsPricesWithOccupancy(t *testing.T) {
	var _ mvt.SystemDepthReader = (*AbsorptionLedgerGORM)(nil)
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := PlayerModel{AgentSymbol: "DEPTH-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	now := time.Now().UTC()
	exp, imp := "EXPORT", "IMPORT"
	rows := []MarketData{
		{PlayerID: player.ID, WaypointSymbol: "X1-A-1", GoodSymbol: "IRON", PurchasePrice: 100, SellPrice: 90, TradeVolume: 60, TradeType: &exp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-A-2", GoodSymbol: "IRON", PurchasePrice: 160, SellPrice: 150, TradeVolume: 40, TradeType: &imp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-B-1", GoodSymbol: "IRON", PurchasePrice: 100, SellPrice: 90, TradeVolume: 10, TradeType: &exp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-ZZ-1", GoodSymbol: "IRON", PurchasePrice: 1, SellPrice: 1, TradeVolume: 1, TradeType: &exp, LastUpdated: now},
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}
	// One PLANNED buy reservation of 10 units on X1-A-1 IRON, unexpired.
	require.NoError(t, db.Create(&MarketAbsorptionLedgerModel{
		PlayerID: player.ID, Waypoint: "X1-A-1", Good: "IRON", Side: "buy", Units: 10,
		State: absorptionStatePlanned, ContainerID: "ctr-1", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}).Error) // add any further NOT NULL fields the model declares

	ledger := NewAbsorptionLedgerGORM(db) // use the package's real constructor name
	got, err := ledger.SystemDepths(context.Background(), player.ID, []string{"X1-A", "X1-B", "X1-NOPE"})
	require.NoError(t, err)
	require.Len(t, got["X1-A"], 2)
	require.Len(t, got["X1-B"], 1)
	require.Nil(t, got["X1-NOPE"])
	require.NotContains(t, got, "X1-ZZ")
	var src mvt.LaneDepth
	for _, l := range got["X1-A"] {
		if l.Listing.Waypoint == "X1-A-1" {
			src = l
		}
	}
	require.Equal(t, "IRON", src.Listing.Good)
	require.Equal(t, 100, src.Listing.Ask)
	require.Equal(t, 90, src.Listing.Bid)
	require.Equal(t, "EXPORT", src.Listing.TradeType)
	require.Equal(t, 60, src.Listing.Volume)
	require.Equal(t, 10, src.BuyPlanned)
	require.Equal(t, 0, src.SellPlanned)
}
```

Adjust the `MarketAbsorptionLedgerModel` literal to the model's actual NOT NULL fields (read `models_*absorption*.go` first) and `NewAbsorptionLedgerGORM` to the real constructor name (`grep -n "^func New.*AbsorptionLedger" internal/adapters/persistence/*.go`).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/adapters/persistence/ -run TestAbsorptionLedger_SystemDepths -v`
Expected: FAIL — `SystemDepths` undefined.

- [ ] **Step 3: Write the reader**

```go
package persistence

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

var _ mvt.SystemDepthReader = (*AbsorptionLedgerGORM)(nil)

// SystemDepths joins every priced market-good in the requested systems with the ledger's
// outstanding occupancy (PLANNED reservations and recovering EXECUTED residuals) on both
// sides. It is the MVT ranker's only view of "not currently being explored". Read-only.
func (r *AbsorptionLedgerGORM) SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]mvt.LaneDepth, error) {
	if len(systems) == 0 {
		return map[string][]mvt.LaneDepth{}, nil
	}
	occupancy, err := r.Outstanding(ctx, playerID)
	if err != nil {
		return nil, err
	}
	prefix := r.db.WithContext(ctx)
	for i, s := range systems {
		if i == 0 {
			prefix = prefix.Where("waypoint_symbol LIKE ?", s+"-%")
		} else {
			prefix = prefix.Or("waypoint_symbol LIKE ?", s+"-%")
		}
	}
	var rows []MarketData
	if err := r.db.WithContext(ctx).Where("player_id = ?", playerID).Where(prefix).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read market depth for %d systems: %w", len(systems), err)
	}
	wanted := make(map[string]bool, len(systems))
	for _, s := range systems {
		wanted[s] = true
	}
	out := make(map[string][]mvt.LaneDepth, len(systems))
	for _, m := range rows {
		sys := shared.ExtractSystemSymbol(m.WaypointSymbol)
		if !wanted[sys] {
			continue
		}
		listing := trading.GoodListing{
			Good: m.GoodSymbol, Waypoint: m.WaypointSymbol, TradeType: deref(m.TradeType),
			Bid: m.SellPrice, Ask: m.PurchasePrice, Supply: deref(m.Supply), Activity: deref(m.Activity),
			Volume: m.TradeVolume, ObservedAt: m.LastUpdated,
		}
		buy := occupancy[absorption.LaneKey{Waypoint: m.WaypointSymbol, Good: m.GoodSymbol, Side: absorption.SideBuy}]
		sell := occupancy[absorption.LaneKey{Waypoint: m.WaypointSymbol, Good: m.GoodSymbol, Side: absorption.SideSell}]
		out[sys] = append(out[sys], mvt.LaneDepth{
			Listing:     listing,
			BuyPlanned:  buy.PlannedUnits,
			BuyResidual: buy.RecoveringResidual,
			SellPlanned: sell.PlannedUnits,
			SellResidual: sell.RecoveringResidual,
		})
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
```

If a `deref`-style helper already exists in the package, reuse it instead of adding one.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/adapters/persistence/ -run TestAbsorptionLedger_SystemDepths -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/persistence/absorption_ledger_system_depth.go internal/adapters/persistence/absorption_ledger_system_depth_test.go
git commit -m "feat(mvt): SystemDepths ledger reader — prices joined with outstanding occupancy (<bead>)"
```

### Task 7: Transition telemetry — migration, model, recorder (Wave A)

**Files:**
- Create: `internal/domain/trading/mvt/transition.go`
- Create: `migrations/060_add_mvt_transitions.up.sql`, `migrations/060_add_mvt_transitions.down.sql`
- Create: `internal/adapters/persistence/models_mvt_transition.go`
- Create: `internal/adapters/persistence/mvt_transition_recorder.go`
- Modify: `internal/adapters/persistence/models.go:45` (append `&MVTTransitionModel{},` after Task 5's line)
- Test: `internal/adapters/persistence/mvt_transition_recorder_test.go` (package `persistence_test`)

**Interfaces:**
- Consumes: `database.NewTestConnection()`, `persistence.PlayerModel`.
- Produces:
  - `type State string` with `StateTrade State = "TRADE"`, `StateClaim State = "CLAIM"`, `StateTravel State = "TRAVEL"`.
  - `type Transition struct { PlayerID int; Hull string; From, To State; System string; YieldHere, BestAlternative, TravelCost float64; Reason string; At time.Time }`
  - `type TransitionRecorder interface { Record(ctx context.Context, t Transition) error }`
  - `persistence.NewMVTTransitionRecorder(db *gorm.DB) *MVTTransitionRecorderGORM` implementing it; `ListSince(ctx, playerID int, since time.Time) ([]mvt.Transition, error)` for tests and the cohort check.

- [ ] **Step 1: Write `transition.go`**

```go
package mvt

import (
	"context"
	"time"
)

// State is a hull-loop state. Transitions between them are the loop's only telemetry.
type State string

const (
	StateTrade  State = "TRADE"
	StateClaim  State = "CLAIM"
	StateTravel State = "TRAVEL"
)

// Transition is one state change with the numbers that caused it — exactly the fields the
// spec names: hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason.
type Transition struct {
	PlayerID        int
	Hull            string
	From, To        State
	System          string
	YieldHere       float64
	BestAlternative float64
	TravelCost      float64
	Reason          string
	At              time.Time
}

// TransitionRecorder persists transitions. Recording must never block a hull: callers log
// and continue on error.
type TransitionRecorder interface {
	Record(ctx context.Context, t Transition) error
}
```

- [ ] **Step 2: Write the failing test**

```go
package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestMVTTransitionRecorder_RoundTrip(t *testing.T) {
	var _ mvt.TransitionRecorder = (*persistence.MVTTransitionRecorderGORM)(nil)
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "MVT-T", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	rec := persistence.NewMVTTransitionRecorder(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	want := mvt.Transition{PlayerID: player.ID, Hull: "H1", From: mvt.StateTrade, To: mvt.StateClaim, System: "X1-A",
		YieldHere: 12.5, BestAlternative: 40, TravelCost: 3.25, Reason: mvt.ReasonYieldBelow, At: now}
	require.NoError(t, rec.Record(ctx, want))
	require.NoError(t, rec.Record(ctx, mvt.Transition{PlayerID: player.ID, Hull: "H1", From: mvt.StateClaim, To: mvt.StateTravel, System: "X1-B", Reason: "claim", At: now.Add(time.Second)}))
	got, err := rec.ListSince(ctx, player.ID, now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, want, got[0])
	none, err := rec.ListSince(ctx, player.ID+1, now.Add(-time.Minute))
	require.NoError(t, err)
	require.Empty(t, none)
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/adapters/persistence/ -run TestMVTTransitionRecorder -v`
Expected: FAIL — undefined `NewMVTTransitionRecorder`.

- [ ] **Step 4: Write migration, model, recorder**

`migrations/060_add_mvt_transitions.up.sql`:

```sql
-- MVT trade loop: one row per hull state transition (spec §5 telemetry line).
CREATE TABLE IF NOT EXISTS mvt_transitions (
    id               BIGSERIAL PRIMARY KEY,
    player_id        BIGINT           NOT NULL,
    hull             VARCHAR(50)      NOT NULL,
    from_state       VARCHAR(8)       NOT NULL,
    to_state         VARCHAR(8)       NOT NULL,
    system           VARCHAR(20)      NOT NULL,
    yield_here       DOUBLE PRECISION NOT NULL DEFAULT 0,
    best_alternative DOUBLE PRECISION NOT NULL DEFAULT 0,
    travel_cost      DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason           VARCHAR(64)      NOT NULL,
    at               TIMESTAMPTZ      NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mvt_transitions_player_at ON mvt_transitions (player_id, at DESC);
COMMENT ON TABLE mvt_transitions IS 'MVT trade loop telemetry: hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason — read by the replay and dashboards';
```

`migrations/060_add_mvt_transitions.down.sql`:

```sql
DROP INDEX IF EXISTS idx_mvt_transitions_player_at;
DROP TABLE IF EXISTS mvt_transitions;
```

`internal/adapters/persistence/models_mvt_transition.go`:

```go
package persistence

import "time"

// MVTTransitionModel mirrors migrations/060_add_mvt_transitions.up.sql.
type MVTTransitionModel struct {
	ID              uint      `gorm:"column:id;primaryKey;autoIncrement"`
	PlayerID        int       `gorm:"column:player_id;not null;index:idx_mvt_transitions_player_at,priority:1"`
	Hull            string    `gorm:"column:hull;size:50;not null"`
	FromState       string    `gorm:"column:from_state;size:8;not null"`
	ToState         string    `gorm:"column:to_state;size:8;not null"`
	System          string    `gorm:"column:system;size:20;not null"`
	YieldHere       float64   `gorm:"column:yield_here;not null;default:0"`
	BestAlternative float64   `gorm:"column:best_alternative;not null;default:0"`
	TravelCost      float64   `gorm:"column:travel_cost;not null;default:0"`
	Reason          string    `gorm:"column:reason;size:64;not null"`
	At              time.Time `gorm:"column:at;not null;index:idx_mvt_transitions_player_at,priority:2"`
}

func (MVTTransitionModel) TableName() string { return "mvt_transitions" }
```

`internal/adapters/persistence/mvt_transition_recorder.go`:

```go
package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// MVTTransitionRecorderGORM appends hull-loop transitions to mvt_transitions.
type MVTTransitionRecorderGORM struct {
	db *gorm.DB
}

var _ mvt.TransitionRecorder = (*MVTTransitionRecorderGORM)(nil)

func NewMVTTransitionRecorder(db *gorm.DB) *MVTTransitionRecorderGORM {
	return &MVTTransitionRecorderGORM{db: db}
}

func (r *MVTTransitionRecorderGORM) Record(ctx context.Context, t mvt.Transition) error {
	row := MVTTransitionModel{PlayerID: t.PlayerID, Hull: t.Hull, FromState: string(t.From), ToState: string(t.To),
		System: t.System, YieldHere: t.YieldHere, BestAlternative: t.BestAlternative, TravelCost: t.TravelCost,
		Reason: t.Reason, At: t.At}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("record mvt transition %s %s→%s: %w", t.Hull, t.From, t.To, err)
	}
	return nil
}

// ListSince returns a player's transitions at or after since, oldest first.
func (r *MVTTransitionRecorderGORM) ListSince(ctx context.Context, playerID int, since time.Time) ([]mvt.Transition, error) {
	var rows []MVTTransitionModel
	if err := r.db.WithContext(ctx).Where("player_id = ? AND at >= ?", playerID, since).Order("at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mvt transitions: %w", err)
	}
	out := make([]mvt.Transition, 0, len(rows))
	for _, row := range rows {
		out = append(out, mvt.Transition{PlayerID: row.PlayerID, Hull: row.Hull, From: mvt.State(row.FromState), To: mvt.State(row.ToState),
			System: row.System, YieldHere: row.YieldHere, BestAlternative: row.BestAlternative, TravelCost: row.TravelCost,
			Reason: row.Reason, At: row.At})
	}
	return out, nil
}
```

Register `&MVTTransitionModel{},` in `models.go` after `&TradeClaimModel{},`.

- [ ] **Step 5: Run the test**

Run: `go test ./internal/adapters/persistence/ -run TestMVTTransitionRecorder -v`
Expected: PASS. If sqlite returns `At` with a different location, compare with `.Equal` on the time fields instead of struct equality.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/trading/mvt/transition.go migrations/060_add_mvt_transitions.up.sql migrations/060_add_mvt_transitions.down.sql internal/adapters/persistence/models_mvt_transition.go internal/adapters/persistence/mvt_transition_recorder.go internal/adapters/persistence/mvt_transition_recorder_test.go internal/adapters/persistence/models.go
git commit -m "feat(mvt): mvt_transitions telemetry table and recorder (<bead>)"
```

### Task 8: Config knobs and fleet-tag plumbing (Wave A)

**Files:**
- Modify: `internal/infrastructure/config/trade_fleet.go` (`TradeFleetConfig` struct)
- Modify: `internal/application/trading/commands/run_tour_coordinator_contract.go` (`RunTourCoordinatorCommand`)
- Modify: `internal/application/trading/commands/run_trade_fleet_coordinator.go:23` (tag constants) and the `RunTradeFleetCoordinatorCommand` struct (same file)
- Modify: `internal/application/trading/commands/run_trade_fleet_coordinator_fleetview.go` (`partitionTradeFleet`)
- Modify: `internal/application/trading/commands/run_trade_fleet_coordinator_launch.go` (`TourLaunchSpec`, `buildTourLaunchSpec`, `launchTourForHull:14`, `relaunchHungTours:69`)
- Modify: `internal/adapters/grpc/container_ops_tour.go` (`TourRunOverrides:25`, `StartTourRun:75` config map)
- Modify: `internal/adapters/grpc/container_ops_trade_fleet_coordinator.go:69` (`LaunchTour`)
- Modify: `internal/adapters/grpc/command_factory_builders.go` (`buildTourCoordinatorCommand` near line 648; the trade fleet coordinator builder — find it with `grep -n "FullHullPausePct" internal/adapters/grpc/*.go`)
- Test: `internal/application/trading/commands/run_trade_fleet_coordinator_fleetview_test.go` (create if absent), `run_trade_fleet_coordinator_launch_test.go` (create if absent)

**Interfaces:**
- Consumes: `ship.DedicatedFleet()`, `ship.IsReservedByCaptain()`, `ship.IsAssigned()`, `ship.IsInTransit()` on `*navigation.Ship`; `configReader.OptionalInt(key, fallback)`, `OptionalBool(key)`.
- Produces (names later tasks use verbatim):
  - Constants in `run_trade_fleet_coordinator.go`: `tradeFleet = "trade"` (exists), `tradeFleetMVT = "trade-mvt"`, `tradeFleetLane = "trade-lane"`; `func isTradeFleetTag(tag string) bool`.
  - `RunTourCoordinatorCommand` fields: `MVTLoop bool`, `YieldWindowSells int`, `YieldMinSells int`, `ClaimReachHops int`, `SpecialistCadenceMinutes int`.
  - Exported defaults in `run_tour_coordinator_contract.go`: `DefaultYieldWindowSells = 8`, `DefaultYieldMinSells = 3`, `DefaultClaimReachHops = 2`, `DefaultSpecialistFractionPct = 10`, `DefaultFatLaneMultiplePct = 200`, `DefaultSpecialistCadenceMinutes = 60`.
  - `RunTradeFleetCoordinatorCommand` fields: `SpecialistFractionPct int`, `FatLaneMultiplePct int`, `SpecialistCadenceMinutes int`.
  - `TourLaunchSpec.Fleet string`; `buildTourLaunchSpec(cmd, shipSymbol, fleet string, reachEscalated bool, reserve int64)`.
  - `TourRunOverrides.MVTLoop bool`; config-map keys `mvt_loop`, `yield_window_sells`, `yield_min_sells`, `claim_reach_hops`, `specialist_cadence_minutes` (tour) and `specialist_fraction_pct`, `fat_lane_multiple_pct`, `specialist_cadence_minutes` (fleet coordinator).

- [ ] **Step 1: Write the failing tests**

`run_trade_fleet_coordinator_fleetview_test.go` (append if the file exists; use the package's existing ship-building test helper if one exists — `grep -n "func newTradeFleetShip\|func tfShip\|navigation.NewShip(" internal/application/trading/commands/run_trade_fleet_coordinator*_test.go` — otherwise build ships with `navigation.NewShip` and `SetDedicatedFleet`):

```go
func TestPartitionTradeFleet_AcceptsAllTradeTags(t *testing.T) {
	ships := []*navigation.Ship{
		tfIdleShip(t, "T-OLD", "trade"),
		tfIdleShip(t, "T-MVT", "trade-mvt"),
		tfIdleShip(t, "T-LANE", "trade-lane"),
		tfIdleShip(t, "T-OTHER", "contract"),
		tfIdleShip(t, "T-NONE", ""),
	}
	idle, running := partitionTradeFleet(ships)
	if len(running) != 0 {
		t.Fatalf("running = %d", len(running))
	}
	got := map[string]bool{}
	for _, s := range idle {
		got[s.ShipSymbol()] = true
	}
	if !got["T-OLD"] || !got["T-MVT"] || !got["T-LANE"] || got["T-OTHER"] || got["T-NONE"] {
		t.Fatalf("idle = %v", got)
	}
}
```

`run_trade_fleet_coordinator_launch_test.go`:

```go
func TestBuildTourLaunchSpec_CarriesFleetTag(t *testing.T) {
	cmd := &RunTradeFleetCoordinatorCommand{MaxHops: 3, MaxSpend: 10, MinMargin: 1, ReplanLimit: 2, AgentSymbol: "A", PlayerID: 7}
	spec := buildTourLaunchSpec(cmd, "T-1", "trade-mvt", true, 500)
	if spec.Fleet != "trade-mvt" || spec.ShipSymbol != "T-1" || !spec.RepositionReachEscalated || spec.WorkingCapitalReserve != 500 {
		t.Fatalf("spec = %+v", spec)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/application/trading/commands/ -run 'TestPartitionTradeFleet_AcceptsAllTradeTags|TestBuildTourLaunchSpec_CarriesFleetTag' -v`
Expected: FAIL to compile (`buildTourLaunchSpec` arity) or FAIL on the `trade-mvt` assertion.

- [ ] **Step 3: Tags and partition**

In `run_trade_fleet_coordinator.go` next to `tradeFleet = "trade"`:

```go
	// MVT trade loop membership (spec §8). All three tags are trade hulls launched by this
	// coordinator; the tag selects the tour path per hull and is the rollback lever.
	tradeFleetMVT  = "trade-mvt"
	tradeFleetLane = "trade-lane"
```

```go
// isTradeFleetTag reports whether a dedicated_fleet tag belongs to this coordinator.
func isTradeFleetTag(tag string) bool {
	return tag == tradeFleet || tag == tradeFleetMVT || tag == tradeFleetLane
}
```

In `partitionTradeFleet` replace `if ship.DedicatedFleet() != tradeFleet {` with `if !isTradeFleetTag(ship.DedicatedFleet()) {`.

- [ ] **Step 4: Launch spec carries the tag**

In `run_trade_fleet_coordinator_launch.go`: add `Fleet string` to `TourLaunchSpec` (after `ShipSymbol`); change `buildTourLaunchSpec(cmd *RunTradeFleetCoordinatorCommand, shipSymbol, fleet string, reachEscalated bool, reserve int64)` and set `Fleet: fleet`; in `launchTourForHull` (line 23) call `buildTourLaunchSpec(cmd, ship.ShipSymbol(), ship.DedicatedFleet(), reachEscalated, reserve)`; in `relaunchHungTours` (line 91) resolve the tag from the `running` slice:

```go
		relaunch: func(ctx context.Context, shipSymbol string) (string, error) {
			fleet := tradeFleet
			for _, s := range running {
				if s.ShipSymbol() == shipSymbol {
					fleet = s.DedicatedFleet()
					break
				}
			}
			return h.launcher.LaunchTour(ctx, buildTourLaunchSpec(cmd, shipSymbol, fleet, false, reserveFor()))
		},
```

- [ ] **Step 5: Command fields and defaults**

`run_tour_coordinator_contract.go` — add to `RunTourCoordinatorCommand` after `CandidateShortlistTopN`:

```go
	// MVT trade loop (spec docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md).
	// MVTLoop is set from the hull's fleet tag (trade-mvt) at launch; the knobs below are
	// resolved by the command builder from trade_fleet.* with the spec defaults.
	MVTLoop                  bool
	YieldWindowSells         int
	YieldMinSells            int
	ClaimReachHops           int
	SpecialistCadenceMinutes int
```

and the exported defaults:

```go
// MVT trade loop defaults (spec §5). Fitted from the replay before ship; none encodes fleet size.
const (
	DefaultYieldWindowSells         = 8
	DefaultYieldMinSells            = 3
	DefaultClaimReachHops           = 2
	DefaultSpecialistFractionPct    = 10  // specialist_fraction 0.10
	DefaultFatLaneMultiplePct       = 200 // fat_lane_multiple 2.0
	DefaultSpecialistCadenceMinutes = 60  // specialist_cadence 1h
)
```

`RunTradeFleetCoordinatorCommand` (in `run_trade_fleet_coordinator.go`) — add after `FullHullPausePct`:

```go
	// MVT specialist pool (spec §4).
	SpecialistFractionPct    int
	FatLaneMultiplePct       int
	SpecialistCadenceMinutes int
```

`internal/infrastructure/config/trade_fleet.go` — add to `TradeFleetConfig`:

```go
	// MVT trade loop knobs (spec §5). Zero means "use the code default".
	YieldWindowSells         int `mapstructure:"yield_window_sells"`
	YieldMinSells            int `mapstructure:"yield_min_sells"`
	ClaimReachHops           int `mapstructure:"claim_reach_hops"`
	SpecialistFractionPct    int `mapstructure:"specialist_fraction_pct"`
	FatLaneMultiplePct       int `mapstructure:"fat_lane_multiple_pct"`
	SpecialistCadenceMinutes int `mapstructure:"specialist_cadence_minutes"`
```

- [ ] **Step 6: Config map and builders**

`internal/adapters/grpc/container_ops_tour.go`: add `MVTLoop bool` to `TourRunOverrides`. In `StartTourRun`, next to `config["placement_disabled"] = ...`:

```go
	config["mvt_loop"] = overrides.MVTLoop
	if v := s.tradeFleetConfig.YieldWindowSells; v > 0 {
		config["yield_window_sells"] = v
	}
	if v := s.tradeFleetConfig.YieldMinSells; v > 0 {
		config["yield_min_sells"] = v
	}
	if v := s.tradeFleetConfig.ClaimReachHops; v > 0 {
		config["claim_reach_hops"] = v
	}
	if v := s.tradeFleetConfig.SpecialistCadenceMinutes; v > 0 {
		config["specialist_cadence_minutes"] = v
	}
```

`internal/adapters/grpc/container_ops_trade_fleet_coordinator.go` `LaunchTour`: build the overrides with `MVTLoop: spec.Fleet == "trade-mvt"` alongside the existing `RepositionReachEnabled` assignment (keep whatever it already sets).

`command_factory_builders.go` `buildTourCoordinatorCommand`, next to `PlacementDisabled: cfg.OptionalBool("placement_disabled"),`:

```go
		MVTLoop:                  cfg.OptionalBool("mvt_loop"),
		YieldWindowSells:         cfg.OptionalInt("yield_window_sells", tradingCmd.DefaultYieldWindowSells),
		YieldMinSells:            cfg.OptionalInt("yield_min_sells", tradingCmd.DefaultYieldMinSells),
		ClaimReachHops:           cfg.OptionalInt("claim_reach_hops", tradingCmd.DefaultClaimReachHops),
		SpecialistCadenceMinutes: cfg.OptionalInt("specialist_cadence_minutes", tradingCmd.DefaultSpecialistCadenceMinutes),
```

(use the alias the file already uses for the commands package). In the trade fleet coordinator builder and its config-map writer (both found via `FullHullPausePct`), mirror the same pattern for `specialist_fraction_pct` → `SpecialistFractionPct` (default `DefaultSpecialistFractionPct`), `fat_lane_multiple_pct` → `FatLaneMultiplePct` (`DefaultFatLaneMultiplePct`), `specialist_cadence_minutes` → `SpecialistCadenceMinutes` (`DefaultSpecialistCadenceMinutes`), writing the map key only when the config value is > 0.

- [ ] **Step 7: Build and run the tests**

Run: `go build ./... && go test ./internal/application/trading/commands/ -run 'TestPartitionTradeFleet|TestBuildTourLaunchSpec' -v && go test ./internal/adapters/grpc/ 2>&1 | tail -3`
Expected: build OK, both tests PASS, grpc package tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/infrastructure/config/trade_fleet.go internal/application/trading/commands/run_tour_coordinator_contract.go internal/application/trading/commands/run_trade_fleet_coordinator.go internal/application/trading/commands/run_trade_fleet_coordinator_fleetview.go internal/application/trading/commands/run_trade_fleet_coordinator_launch.go internal/application/trading/commands/run_trade_fleet_coordinator_fleetview_test.go internal/application/trading/commands/run_trade_fleet_coordinator_launch_test.go internal/adapters/grpc/container_ops_tour.go internal/adapters/grpc/container_ops_trade_fleet_coordinator.go internal/adapters/grpc/command_factory_builders.go
git commit -m "feat(mvt): trade-mvt/trade-lane fleet tags, mvt_loop override, and spec knobs (<bead>)"
```

### Task 9: Tour handler — MVT ports, per-hull state, `mvtRank`, shadow decisions, daemon wiring (Wave B; migration step 1)

**Files:**
- Create: `internal/application/trading/commands/run_tour_coordinator_mvt.go`
- Create: `internal/application/trading/commands/run_tour_coordinator_mvt_shadow.go`
- Modify: `internal/application/trading/commands/run_tour_coordinator.go:978` (`afterProductiveTour`, first line of the body)
- Modify: `cmd/spacetraders-daemon/coordinator_wiring.go:123` (`configureTourCoordinator`)
- Test: `internal/application/trading/commands/run_tour_coordinator_mvt_test.go`

**Interfaces:**
- Consumes: `mvt.Rank/SystemYield/BestAlternative/Hull/Candidate/Costs/ScoredSystem` (Task 1); `mvt.YieldTracker` (Task 2); `mvt.ComputeFleetStats/FleetStats` (Task 3); `mvt.ClaimRegistry` (Task 5); `mvt.SystemDepthReader` (Task 6 adapter); `mvt.Transition/TransitionRecorder/State*` (Task 7); command fields `MVTLoop, YieldWindowSells, YieldMinSells, ClaimReachHops, SpecialistCadenceMinutes` (Task 8). Existing handler fields: `h.legs` (`loadShip`, `repositionNeighborsWithinJumps(ctx, originSystem string, playerID, maxJumps int) ([]repositionNeighborEdge{system, underConstruction, hops}, string)`), `h.telemetry.ListByPlayer(ctx, playerID int, since time.Time)`, `h.gateFees.GateFees(ctx, playerID) map[string]int64`, `h.jumpTolls.PerHopTollSeconds(ctx, playerID) int`, `h.rankerAgeCaps trading.RankerAgeCaps`, `h.clock`.
- Produces (used by Tasks 10, 11):
  - `func (h *RunTourCoordinatorHandler) SetMVTPorts(claims mvt.ClaimRegistry, depth mvt.SystemDepthReader, transitions mvt.TransitionRecorder)`
  - `type mvtHullState struct { mu sync.Mutex; claimed string; yield *mvt.YieldTracker; basis map[string][]mvtLot; travelFailures int; holdSells int }` with `type mvtLot struct { units, price int }`
  - `func (h *RunTourCoordinatorHandler) mvtState(cmd *RunTourCoordinatorCommand) *mvtHullState` — lazily creates per hull with `mvt.NewYieldTracker(cmd.YieldWindowSells, cmd.YieldMinSells)`
  - `func (h *RunTourCoordinatorHandler) mvtFleetStats(ctx, cmd) mvt.FleetStats` — cached; recomputed when older than `SpecialistCadenceMinutes` from `h.telemetry.ListByPlayer(ctx, cmd.PlayerID, now−24h)`
  - `func (h *RunTourCoordinatorHandler) mvtRank(ctx, cmd, ship *navigation.Ship) ([]mvt.ScoredSystem, error)`
  - `func (h *RunTourCoordinatorHandler) mvtRecord(ctx, cmd, from, to mvt.State, system string, yieldHere, bestAlt, travel float64, reason string)` — writes the row and a log line; never returns an error
  - `func (h *RunTourCoordinatorHandler) mvtShadow(ctx, cmd)` — old-path hulls only.
  - Reason constants (this file): `mvtReasonShadow = "shadow"`, `mvtReasonBootstrap = "bootstrap"`, `mvtReasonEmpty = "empty"`, `mvtReasonArrived = "arrived"`, `mvtReasonTravelFailed = "travel_failed"`, `mvtReasonRankerUnreadable = "ranker_unreadable"`, `mvtReasonHold = "hold"`.

`mvtRank` algorithm (exact):
1. `current := ship.CurrentLocation().SystemSymbol`; if the location is nil return `nil, errors.New("hull has no location")`.
2. `edges, _ := h.legs.repositionNeighborsWithinJumps(ctx, current, cmd.PlayerID, cmd.ClaimReachHops)`; candidates = `current` (hops 0) plus each edge with `!underConstruction` (hops = `edge.hops`).
3. `depths, err := h.mvt.depth.SystemDepths(ctx, cmd.PlayerID, systems)`; on error return it (caller stays put).
4. `inTransit, err := h.mvt.claims.InTransit(ctx, cmd.PlayerID)`; on error return it.
5. `own, ok, _ := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol)`; if `ok && own.ArrivedAt == nil` subtract 1 from `inTransit[own.System]` (a hull never penalises its own claim).
6. `stats := h.mvtFleetStats(ctx, cmd)`; `now := h.clock.Now()`.
7. For each system: `credits, units, entry := mvt.SystemYield(depths[sys], h.rankerAgeCaps, now)`; skip when `units == 0`; append `mvt.Candidate{System, Hops, YieldCredits: credits, DepthUnits: units, InTransit: inTransit[sys], EntryWaypoint: entry}`.
8. If `h.jumpTolls == nil || h.gateFees == nil` return `nil, errors.New("mvt ranker needs jump toll and gate fee readers")`.
9. `costs := mvt.Costs{TollSecondsPerHop: h.jumpTolls.PerHopTollSeconds(ctx, cmd.PlayerID), GateFeeFromCurrent: h.gateFees.GateFees(ctx, cmd.PlayerID)[current], FleetDrawPerVisit: stats.MeanMarginPerSystemVisit, FleetCreditsPerSec: stats.CreditsPerHullSec}`.
10. `hull := mvt.Hull{Symbol: cmd.ShipSymbol, System: current, CargoCapacity: ship.CargoCapacity(), CreditsPerSec: state.yield.CreditsPerSec(now)}`; return `mvt.Rank(hull, cands, costs), nil`.

`mvtShadow`: if `h.mvt.transitions == nil` return. Load the ship; `ranked, err := h.mvtRank`; on error record `TRADE→TRADE` with reason `mvtReasonRankerUnreadable` and return. `to := StateTrade`; `alt, hasAlt := mvt.BestAlternative(ranked, current)`; if `hasAlt && len(ranked) > 0 && ranked[0].System != current` then `to = StateClaim`. Record `TRADE→to` with `system=current`, `yieldHere` from the tracker estimate (0 when none), `bestAlt = alt.Score`, `travel = alt.TravelPerUnit`, reason `mvtReasonShadow`. Every error is logged at WARNING and swallowed.

- [ ] **Step 1: Write the failing test**

Reuse the existing fixtures in `run_tour_coordinator_rate_floor_test.go` (`repositionFixture()`, `rateFloorPlanner`, `feasiblePlan`, `rfSeed`, `seededTelemetry`, `tradeCaptureLogger`, `writeTourArtifact`, `isolateLegacyReposition`). Read that file first; the fixture's home system is `X1-S1` with neighbour `X1-S2`.

```go
package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

type mvtFakeClaims struct {
	mu   sync.Mutex
	rows map[string]mvt.Claim
	fail bool
}

func newMVTFakeClaims() *mvtFakeClaims { return &mvtFakeClaims{rows: map[string]mvt.Claim{}} }
func (c *mvtFakeClaims) Upsert(_ context.Context, _ int, hull, system string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[hull] = mvt.Claim{Hull: hull, System: system, ClaimedAt: at}
	return nil
}
func (c *mvtFakeClaims) MarkArrived(_ context.Context, _ int, hull string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rows[hull]
	r.ArrivedAt = &at
	c.rows[hull] = r
	return nil
}
func (c *mvtFakeClaims) Release(_ context.Context, _ int, hull string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, hull)
	return nil
}
func (c *mvtFakeClaims) Get(_ context.Context, _ int, hull string) (mvt.Claim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rows[hull]
	return r, ok, nil
}
func (c *mvtFakeClaims) InTransit(_ context.Context, _ int) (map[string]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, context.DeadlineExceeded
	}
	out := map[string]int{}
	for _, r := range c.rows {
		if r.ArrivedAt == nil {
			out[r.System]++
		}
	}
	return out, nil
}

type mvtFakeDepth struct {
	lanes map[string][]mvt.LaneDepth
	fail  bool
}

func (d *mvtFakeDepth) SystemDepths(_ context.Context, _ int, systems []string) (map[string][]mvt.LaneDepth, error) {
	if d.fail {
		return nil, context.DeadlineExceeded
	}
	out := map[string][]mvt.LaneDepth{}
	for _, s := range systems {
		if l, ok := d.lanes[s]; ok {
			out[s] = l
		}
	}
	return out, nil
}

type mvtFakeTransitions struct {
	mu   sync.Mutex
	rows []mvt.Transition
}

func (r *mvtFakeTransitions) Record(_ context.Context, t mvt.Transition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, t)
	return nil
}
func (r *mvtFakeTransitions) last() mvt.Transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[len(r.rows)-1]
}

type mvtFakeTolls struct{ seconds int }

func (t mvtFakeTolls) PerHopTollSeconds(context.Context, int) int { return t.seconds }

type mvtFakeFees struct{ fees map[string]int64 }

func (f mvtFakeFees) GateFees(context.Context, int) map[string]int64 { return f.fees }

// mvtLane builds a fresh EXPORT/IMPORT pair for good in system with the given depth and spread.
func mvtLane(system, good string, depth, ask, bid int, now time.Time) []mvt.LaneDepth {
	mk := func(wp, tt string, price int) trading.GoodListing {
		return trading.GoodListing{Good: good, Waypoint: wp, TradeType: tt, Bid: price, Ask: price,
			Supply: "MODERATE", Activity: "STRONG", Volume: depth, ObservedAt: now}
	}
	return []mvt.LaneDepth{{Listing: mk(system+"-SRC", "EXPORT", ask)}, {Listing: mk(system+"-SNK", "IMPORT", bid)}}
}

func mvtCaps() trading.RankerAgeCaps {
	h := 24 * time.Hour
	return trading.RankerAgeCaps{Weak: h, Restricted: h, Growing: h, Strong: h}
}

func TestMVTShadow_RecordsWouldBeDecisionOnOldPath(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-SH", 100000)})
	claims, trans := newMVTFakeClaims(), &mvtFakeTransitions{}
	now := time.Now()
	depth := &mvtFakeDepth{lanes: map[string][]mvt.LaneDepth{
		"X1-S1": mvtLane("X1-S1", "IRON", 10, 100, 110, now),    // 100 credits of depth at home
		"X1-S2": mvtLane("X1-S2", "IRON", 500, 100, 1000, now),  // 450k next door
	}}
	h.SetMVTPorts(claims, depth, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 361})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{"X1-S1": 100}})
	h.SetRankerAgeCaps(mvtCaps())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	_, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SH", PlayerID: 1, ContainerID: "ctr-sh", Iterations: -1,
		RepositionMinMargin: isolateLegacyReposition, PlacementDisabled: true,
		ModelArtifactPath: writeTourArtifact(t),
		YieldWindowSells: 8, YieldMinSells: 3, ClaimReachHops: 2, SpecialistCadenceMinutes: 60,
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(trans.rows) == 0 {
		t.Fatal("shadow must record a transition after the productive home tour")
	}
	got := trans.last()
	if got.Reason != mvtReasonShadow || got.From != mvt.StateTrade || got.To != mvt.StateClaim || got.System != "X1-S1" || got.BestAlternative <= 0 {
		t.Fatalf("shadow row = %+v", got)
	}
	if len(claims.rows) != 0 {
		t.Fatal("shadow must never write a claim")
	}
	if len(fx.jumps) != 0 {
		t.Fatal("shadow must never move the hull")
	}
}

func TestMVTShadow_UnreadableLedgerRecordsStay(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), &seededTelemetry{rows: rfSeed("TOUR-SH", 100000)})
	trans := &mvtFakeTransitions{}
	h.SetMVTPorts(newMVTFakeClaims(), &mvtFakeDepth{fail: true}, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 361})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{}})
	h.SetRankerAgeCaps(mvtCaps())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SH", PlayerID: 1, ContainerID: "ctr-sh", Iterations: -1,
		RepositionMinMargin: isolateLegacyReposition, PlacementDisabled: true,
		ModelArtifactPath: writeTourArtifact(t), YieldWindowSells: 8, YieldMinSells: 3, ClaimReachHops: 2,
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := trans.last()
	if got.Reason != mvtReasonRankerUnreadable || got.To != mvt.StateTrade {
		t.Fatalf("unreadable ledger must record a stay: %+v", got)
	}
}
```

If `SetJumpTollReader`/`SetGateFeeReader` setters exist under other names on the tour handler, use those (`grep -n "func (h \*RunTourCoordinatorHandler) Set" internal/application/trading/commands/run_tour_coordinator_wiring.go`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/application/trading/commands/ -run TestMVTShadow -v`
Expected: FAIL — `SetMVTPorts` undefined.

- [ ] **Step 3: Write `run_tour_coordinator_mvt.go`**

```go
package commands

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// MVT loop transition reasons that are not departure verdicts (those live in package mvt).
const (
	mvtReasonShadow           = "shadow"
	mvtReasonBootstrap        = "bootstrap"
	mvtReasonEmpty            = "empty"
	mvtReasonArrived          = "arrived"
	mvtReasonTravelFailed     = "travel_failed"
	mvtReasonRankerUnreadable = "ranker_unreadable"
	mvtReasonHold             = "hold"

	mvtFleetStatsWindow = 24 * time.Hour
	mvtTravelFailureCap = 3
)

type mvtPorts struct {
	claims      mvt.ClaimRegistry
	depth       mvt.SystemDepthReader
	transitions mvt.TransitionRecorder
}

// SetMVTPorts wires the claim registry, the ledger depth reader and the transition
// recorder. Without them the MVT branch stays inert and the shadow logger is silent.
func (h *RunTourCoordinatorHandler) SetMVTPorts(claims mvt.ClaimRegistry, depth mvt.SystemDepthReader, transitions mvt.TransitionRecorder) {
	h.mvt = mvtPorts{claims: claims, depth: depth, transitions: transitions}
}

type mvtLot struct{ units, price int }

// mvtHullState is the loop's in-memory view of one hull. Nothing here is persisted: the
// claim registry holds the durable part, and the yield resets on restart by design.
type mvtHullState struct {
	mu             sync.Mutex
	claimed        string
	yield          *mvt.YieldTracker
	basis          map[string][]mvtLot
	travelFailures int
	holdSells      int
}

type mvtFleetCache struct {
	mu         sync.Mutex
	stats      mvt.FleetStats
	computedAt time.Time
}

func (h *RunTourCoordinatorHandler) mvtState(cmd *RunTourCoordinatorCommand) *mvtHullState {
	h.mvtMu.Lock()
	defer h.mvtMu.Unlock()
	if h.mvtHulls == nil {
		h.mvtHulls = map[string]*mvtHullState{}
	}
	st := h.mvtHulls[cmd.ShipSymbol]
	if st == nil {
		st = &mvtHullState{yield: mvt.NewYieldTracker(cmd.YieldWindowSells, cmd.YieldMinSells), basis: map[string][]mvtLot{}}
		h.mvtHulls[cmd.ShipSymbol] = st
	}
	return st
}

func (h *RunTourCoordinatorHandler) mvtCadence(cmd *RunTourCoordinatorCommand) time.Duration {
	if cmd.SpecialistCadenceMinutes <= 0 {
		return time.Duration(DefaultSpecialistCadenceMinutes) * time.Minute
	}
	return time.Duration(cmd.SpecialistCadenceMinutes) * time.Minute
}

// mvtFleetStats is the fleet-wide draw and rate, recomputed on the specialist cadence.
func (h *RunTourCoordinatorHandler) mvtFleetStats(ctx context.Context, cmd *RunTourCoordinatorCommand) mvt.FleetStats {
	now := h.clock.Now()
	h.mvtFleet.mu.Lock()
	defer h.mvtFleet.mu.Unlock()
	if !h.mvtFleet.computedAt.IsZero() && now.Sub(h.mvtFleet.computedAt) < h.mvtCadence(cmd) {
		return h.mvtFleet.stats
	}
	if h.telemetry == nil {
		return h.mvtFleet.stats
	}
	legs, err := h.telemetry.ListByPlayer(ctx, cmd.PlayerID, now.Add(-mvtFleetStatsWindow))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT fleet stats unreadable; keeping previous", map[string]interface{}{"error": err.Error()})
		return h.mvtFleet.stats
	}
	h.mvtFleet.stats = mvt.ComputeFleetStats(legs, mvtFleetStatsWindow)
	h.mvtFleet.computedAt = now
	return h.mvtFleet.stats
}

// mvtRank ranks the hull's current system and every priced system within ClaimReachHops.
// Any unreadable input returns an error and the caller stays put.
func (h *RunTourCoordinatorHandler) mvtRank(ctx context.Context, cmd *RunTourCoordinatorCommand, ship *navigation.Ship) ([]mvt.ScoredSystem, error) {
	if h.mvt.depth == nil || h.mvt.claims == nil {
		return nil, errors.New("mvt ports not wired")
	}
	if ship.CurrentLocation() == nil {
		return nil, errors.New("hull has no location")
	}
	if h.jumpTolls == nil || h.gateFees == nil {
		return nil, errors.New("mvt ranker needs jump toll and gate fee readers")
	}
	current := ship.CurrentLocation().SystemSymbol
	edges, _ := h.legs.repositionNeighborsWithinJumps(ctx, current, cmd.PlayerID, cmd.ClaimReachHops)
	systems := []string{current}
	hops := map[string]int{current: 0}
	for _, e := range edges {
		if e.underConstruction || e.system == current {
			continue
		}
		systems = append(systems, e.system)
		hops[e.system] = e.hops
	}
	depths, err := h.mvt.depth.SystemDepths(ctx, cmd.PlayerID, systems)
	if err != nil {
		return nil, fmt.Errorf("system depths: %w", err)
	}
	inTransit, err := h.mvt.claims.InTransit(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("in-transit claims: %w", err)
	}
	if own, ok, _ := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol); ok && own.ArrivedAt == nil && inTransit[own.System] > 0 {
		inTransit[own.System]--
	}
	stats := h.mvtFleetStats(ctx, cmd)
	now := h.clock.Now()
	cands := make([]mvt.Candidate, 0, len(systems))
	for _, sys := range systems {
		credits, units, entry := mvt.SystemYield(depths[sys], h.rankerAgeCaps, now)
		if units == 0 {
			continue
		}
		cands = append(cands, mvt.Candidate{System: sys, Hops: hops[sys], YieldCredits: credits, DepthUnits: units,
			InTransit: inTransit[sys], EntryWaypoint: entry})
	}
	costs := mvt.Costs{
		TollSecondsPerHop:  h.jumpTolls.PerHopTollSeconds(ctx, cmd.PlayerID),
		GateFeeFromCurrent: h.gateFees.GateFees(ctx, cmd.PlayerID)[current],
		FleetDrawPerVisit:  stats.MeanMarginPerSystemVisit,
		FleetCreditsPerSec: stats.CreditsPerHullSec,
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	rate := st.yield.CreditsPerSec(now)
	st.mu.Unlock()
	hull := mvt.Hull{Symbol: cmd.ShipSymbol, System: current, CargoCapacity: ship.CargoCapacity(), CreditsPerSec: rate}
	return mvt.Rank(hull, cands, costs), nil
}

// mvtRecord writes one telemetry line. Recording never fails a hull.
func (h *RunTourCoordinatorHandler) mvtRecord(ctx context.Context, cmd *RunTourCoordinatorCommand, from, to mvt.State, system string, yieldHere, bestAlt, travel float64, reason string) {
	fields := map[string]interface{}{
		"hull": cmd.ShipSymbol, "from_state": string(from), "to_state": string(to), "system": system,
		"yield_here": yieldHere, "best_alternative": bestAlt, "travel_cost": travel, "reason": reason,
	}
	common.LoggerFromContext(ctx).Log("INFO", "MVT transition", fields)
	if h.mvt.transitions == nil {
		return
	}
	if err := h.mvt.transitions.Record(ctx, mvt.Transition{PlayerID: cmd.PlayerID, Hull: cmd.ShipSymbol, From: from, To: to,
		System: system, YieldHere: yieldHere, BestAlternative: bestAlt, TravelCost: travel, Reason: reason, At: h.clock.Now()}); err != nil {
		fields["error"] = err.Error()
		common.LoggerFromContext(ctx).Log("WARNING", "MVT transition not recorded", fields)
	}
}
```

Add to the `RunTourCoordinatorHandler` struct (in `run_tour_coordinator_wiring.go`, next to `offerPersister`):

```go
	mvt      mvtPorts
	mvtMu    sync.Mutex
	mvtHulls map[string]*mvtHullState
	mvtFleet mvtFleetCache
```

- [ ] **Step 4: Write `run_tour_coordinator_mvt_shadow.go`**

```go
package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtShadow logs where the MVT ranker would send an old-path hull after a productive
// tour. Migration step 1: readers only, no behaviour change, ships ON for every trade hull.
func (h *RunTourCoordinatorHandler) mvtShadow(ctx context.Context, cmd *RunTourCoordinatorCommand) {
	if h.mvt.transitions == nil {
		return
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return
	}
	current := ship.CurrentLocation().SystemSymbol
	st := h.mvtState(cmd)
	st.mu.Lock()
	yieldHere, _ := st.yield.Estimate()
	st.mu.Unlock()
	ranked, err := h.mvtRank(ctx, cmd, ship)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT shadow: ranker unreadable", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, yieldHere, 0, 0, mvtReasonRankerUnreadable)
		return
	}
	to := mvt.StateTrade
	alt, hasAlt := mvt.BestAlternative(ranked, current)
	if hasAlt && len(ranked) > 0 && ranked[0].System != current {
		to = mvt.StateClaim
	}
	h.mvtRecord(ctx, cmd, mvt.StateTrade, to, current, yieldHere, alt.Score, alt.TravelPerUnit, mvtReasonShadow)
}
```

In `run_tour_coordinator.go` `afterProductiveTour` (line 978), make the first statement of the body:

```go
	if !cmd.MVTLoop {
		h.mvtShadow(ctx, cmd)
	}
```

- [ ] **Step 5: Wire the daemon**

In `cmd/spacetraders-daemon/coordinator_wiring.go` `configureTourCoordinator`, after `h.SetRelocationOfferPersister(tourRepositionPersister)`:

```go
	// MVT trade loop readers (spec §5). w.absorption implements mvt.SystemDepthReader.
	h.SetMVTPorts(persistence.NewTradeClaimRegistry(w.db), w.absorption, persistence.NewMVTTransitionRecorder(w.db))
```

If `w.absorption` is not the concrete `*persistence.AbsorptionLedgerGORM` (check with `grep -n "absorption" cmd/spacetraders-daemon/*.go | grep -v "//" | head`), pass the concrete instance that `SetAbsorptionLedger` receives.

- [ ] **Step 6: Build, run the tests, run the package**

Run: `go build ./... && go test ./internal/application/trading/commands/ -run 'TestMVTShadow' -v && go test ./internal/application/trading/commands/ 2>&1 | tail -3`
Expected: both new tests PASS; the whole package PASS (old-path behaviour byte-identical: the shadow only writes telemetry).

- [ ] **Step 7: Commit**

```bash
git add internal/application/trading/commands/run_tour_coordinator_mvt.go internal/application/trading/commands/run_tour_coordinator_mvt_shadow.go internal/application/trading/commands/run_tour_coordinator_mvt_test.go internal/application/trading/commands/run_tour_coordinator.go internal/application/trading/commands/run_tour_coordinator_wiring.go cmd/spacetraders-daemon/coordinator_wiring.go
git commit -m "feat(mvt): ranker wired into the tour handler; shadow decisions logged for every trade hull (<bead>)"
```

**Step 1 ships here.** Deploy (`make restart-daemon`), then confirm rows arrive: `SELECT hull, to_state, system, best_alternative, travel_cost, reason, at FROM mvt_transitions ORDER BY at DESC LIMIT 20;`. Distribution of `to_state` over a full hour is the first look at how often the loop would move hulls.

### Task 10: Hull loop — CLAIM, TRAVEL, scope pin, recovery, old-path bypass (Wave C; migration step 2a)

**Files:**
- Create: `internal/application/trading/commands/run_tour_coordinator_mvt_loop.go`
- Modify: `internal/application/trading/commands/run_tour_coordinator_planning.go:399` (`tourSystemsFrom`)
- Modify: `internal/application/trading/commands/run_tour_coordinator.go` — `execute` after the `resumeInFlightReposition` block (lines 616–628), the `maybeDisperseFromCrowdedGround` call (line 868), `afterProductiveTour` (978), `rescueStarvedGround` (1016)
- Test: `internal/application/trading/commands/run_tour_coordinator_mvt_loop_test.go`

**Interfaces:**
- Consumes: Task 9 (`mvtState`, `mvtRank`, `mvtRecord`, reason constants, `h.mvt`); `h.legs.RepositionToWaypointWithinJumps(ctx, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error`; `h.persistReposition(ctx, cmd, RepositionEpisode{InProgress, TargetSystem, TargetWaypoint})`; `repositionEpisode{repositioned, fromSystem, toSystem, dispersalTried}` (the in-memory episode returned by `resumeInFlightReposition`).
- Produces (used by Task 11):
  - `func (h *RunTourCoordinatorHandler) mvtClaimedSystem(cmd) string`
  - `func (h *RunTourCoordinatorHandler) mvtClaimAndTravel(ctx, cmd, response, reason string) (moved bool, err error)` — ranks, then `mvtTravelTo`
  - `func (h *RunTourCoordinatorHandler) mvtTravelTo(ctx, cmd, response, ranked []mvt.ScoredSystem, reason string) (moved bool, err error)`
  - `func (h *RunTourCoordinatorHandler) mvtRecover(ctx, cmd, response, resumed repositionEpisode) error`
  - `func (h *RunTourCoordinatorHandler) mvtAfterTour(ctx, cmd, response) error` — in this task a stub that only re-stamps presence; Task 11 fills it in.

Behaviour (exact):
- **Scope pin.** `tourSystemsFrom`: when `cmd.MVTLoop`, return `[]string{claimed}` if `mvtClaimedSystem(cmd) != ""`, else `[]string{home}`. The old path is untouched.
- **`mvtTravelTo`**: load ship; `current`. If `len(ranked) == 0 || ranked[0].System == current`: `Upsert(current)` then `MarkArrived(now)`; `state.claimed = current`; record `CLAIM→TRADE` with reason `mvt.ReasonNoAlternative` (empty ranking) or `mvt.ReasonStay`; return `false, nil`. Otherwise `target := ranked[0]`: record `TRADE→CLAIM` (reason = the caller's reason) and `CLAIM→TRAVEL` (reason `"claim"`, system = target); `Upsert(target.System)`; `persistReposition(InProgress: true, TargetSystem, TargetWaypoint: target.EntryWaypoint)`; `jumps := max(1, target.Hops)`; `err := RepositionToWaypointWithinJumps(ctx, hull, target.EntryWaypoint, playerID, jumps)`.
  - failure: `Release`; `persistReposition(InProgress: false)`; `state.travelFailures++`; if `>= mvtTravelFailureCap` set `state.holdSells = cmd.YieldWindowSells` and `travelFailures = 0`; record `TRAVEL→TRADE` reason `mvtReasonTravelFailed` with system = current; return `false, nil` (the hull keeps trading where it stands; the error is logged, not returned).
  - success: `MarkArrived`; `persistReposition(InProgress: false)`; `state.claimed = target.System`; `state.yield.Reset()`; `state.basis = map[string][]mvtLot{}`; `travelFailures = 0`; record `TRAVEL→TRADE` reason `mvtReasonArrived` with system = target; `response.Repositions++` if that counter exists (check `RunTourCoordinatorResponse`; if not, skip); return `true, nil`.
- **`mvtClaimAndTravel`**: `ranked, err := mvtRank`; on error record `CLAIM→TRADE` reason `mvtReasonRankerUnreadable`, set `state.claimed = current` and return `false, nil`; else `mvtTravelTo(ctx, cmd, response, ranked, reason)`.
- **`mvtRecover`** (called in `execute` inside `if continuous {` right after `launchedLaden = h.launchedStuckLaden(ctx, cmd)`, only when `cmd.MVTLoop`): `claim, ok, err := Get`; on err log and treat as `ok=false`.
  - `ok && claim.ArrivedAt != nil` → `state.claimed = claim.System`; return.
  - `ok && claim.ArrivedAt == nil && resumed.repositioned` → the jump-resume just completed: `MarkArrived`, `state.claimed = resumed.toSystem`; record `TRAVEL→TRADE` reason `mvtReasonArrived`; return.
  - `ok && claim.ArrivedAt == nil` (no in-flight reposition to resume) → `Release`; fall through to bootstrap.
  - bootstrap (`!ok` or released): `_, err := mvtClaimAndTravel(ctx, cmd, response, mvtReasonBootstrap)`; return err.
- **Old-path bypass**: in `afterProductiveTour`, after the shadow guard, add `if cmd.MVTLoop { return time.Time{}, h.mvtAfterTour(ctx, cmd, response) }`. In `rescueStarvedGround`, immediately before `repositioned, rerr := h.maybeReposition(...)`, add `if cmd.MVTLoop { return h.mvtClaimAndTravel(ctx, cmd, response, mvtReasonEmpty) }`. Wrap the `maybeDisperseFromCrowdedGround` call at line 868 in `if !cmd.MVTLoop { ... }` (keep `dispersed` false otherwise). Confirm with the code that `rescueStarvedGround` runs on the infeasible/no-progress branch (line 888) — that is the empty-system exit.
- **`mvtAfterTour` (stub in this task)**: `Upsert(current)+MarkArrived` when `state.claimed == ""`; return nil.

- [ ] **Step 1: Write the failing tests**

```go
package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtHandler wires a tour handler with MVT fakes over the shared reposition fixture
// (home X1-S1, neighbour X1-S2). depthS1/depthS2 are the credits of fresh depth in each.
func mvtHandler(t *testing.T, fx *tourFixture, planner *tourFakeRoutingClient, depthS1, depthS2 int) (*RunTourCoordinatorHandler, *mvtFakeClaims, *mvtFakeTransitions) {
	t.Helper()
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: rfSeed("TOUR-MVT", 100000)})
	claims, trans := newMVTFakeClaims(), &mvtFakeTransitions{}
	now := time.Now()
	lanes := map[string][]mvt.LaneDepth{}
	if depthS1 > 0 {
		lanes["X1-S1"] = mvtLane("X1-S1", "IRON", depthS1, 100, 200, now) // 100/unit spread
	}
	if depthS2 > 0 {
		lanes["X1-S2"] = mvtLane("X1-S2", "IRON", depthS2, 100, 200, now)
	}
	h.SetMVTPorts(claims, &mvtFakeDepth{lanes: lanes}, trans)
	h.SetJumpTollReader(mvtFakeTolls{seconds: 1})
	h.SetGateFeeReader(mvtFakeFees{fees: map[string]int64{}})
	h.SetRankerAgeCaps(mvtCaps())
	return h, claims, trans
}

func mvtCmd(t *testing.T) *RunTourCoordinatorCommand {
	t.Helper()
	return &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-MVT", PlayerID: 1, ContainerID: "ctr-mvt", Iterations: -1, MVTLoop: true,
		RepositionMinMargin: isolateLegacyReposition, PlacementDisabled: true,
		ModelArtifactPath: writeTourArtifact(t),
		YieldWindowSells: 8, YieldMinSells: 3, ClaimReachHops: 2, SpecialistCadenceMinutes: 60,
	}
}

func TestTourSystemsFrom_PinnedToClaimUnderMVT(t *testing.T) {
	fx := repositionFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 10)
	cmd := mvtCmd(t)
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S1" {
		t.Fatalf("no claim → home only, got %v", got)
	}
	st := h.mvtState(cmd)
	st.claimed = "X1-S2"
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd); len(got) != 1 || got[0] != "X1-S2" {
		t.Fatalf("claimed → claimed only, got %v", got)
	}
	old := mvtCmd(t)
	old.MVTLoop = false
	if got := h.tourSystemsFrom(context.Background(), "X1-S1", old); len(got) < 2 {
		t.Fatalf("old path must keep home + one hop, got %v", got)
	}
}

func TestMVTBootstrap_StaysWhenHomeIsBest(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 10)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S1" || c.ArrivedAt == nil {
		t.Fatalf("bootstrap must claim home with arrival stamped: %+v ok=%v", c, ok)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("hull must not move: jumps=%v", fx.jumps)
	}
	if got := trans.rows[0]; got.Reason != mvt.ReasonStay || got.To != mvt.StateTrade {
		t.Fatalf("first transition = %+v", got)
	}
}

func TestMVTBootstrap_ClaimsAndTravelsToRicherNeighbour(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" || c.ArrivedAt == nil {
		t.Fatalf("claim = %+v ok=%v", c, ok)
	}
	if len(fx.jumps) == 0 {
		t.Fatal("hull must jump to X1-S2")
	}
	var seen []string
	for _, r := range trans.rows {
		seen = append(seen, string(r.From)+">"+string(r.To)+":"+r.Reason)
	}
	want := []string{"TRADE>CLAIM:bootstrap", "CLAIM>TRAVEL:claim", "TRAVEL>TRADE:arrived"}
	for i, w := range want {
		if i >= len(seen) || seen[i] != w {
			t.Fatalf("transitions = %v, want prefix %v", seen, want)
		}
	}
	if st := h.mvtState(mvtCmd(t)); st.claimed != "X1-S2" {
		t.Fatalf("claimed = %q", st.claimed)
	}
}

func TestMVTTravelFailure_ReleasesClaimAndStays(t *testing.T) {
	fx := repositionFixture()
	fx.navFail = map[string]bool{"X1-S2-SRC": true}
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, ok, _ := claims.Get(ctx, 1, "TOUR-MVT"); ok {
		t.Fatal("failed travel must release the claim")
	}
	found := false
	for _, r := range trans.rows {
		if r.Reason == mvtReasonTravelFailed && r.From == mvt.StateTravel && r.To == mvt.StateTrade && r.System == "X1-S1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no travel_failed transition in %+v", trans.rows)
	}
}

func TestMVTRecover_ArrivedClaimResumesTradeWithoutReclaim(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	arrived := time.Now()
	claims.rows["TOUR-MVT"] = mvt.Claim{Hull: "TOUR-MVT", System: "X1-S1", ClaimedAt: arrived, ArrivedAt: &arrived}
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	for _, r := range trans.rows {
		if r.Reason == mvtReasonBootstrap {
			t.Fatal("an arrived claim must not bootstrap")
		}
	}
	if st := h.mvtState(mvtCmd(t)); st.claimed != "X1-S1" {
		t.Fatalf("claimed = %q, want the recovered X1-S1", st.claimed)
	}
}
```

`fx.navFail` is keyed by destination waypoint (see `tourFixture.navFail map[string]bool`); if the fake navigates by a different key, adapt the key, not the assertion. If the fixture's neighbour list is not `X1-S1 → X1-S2`, set `fx.neighbors["X1-S1"] = []string{"X1-S2"}` in `mvtHandler`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/application/trading/commands/ -run 'TestTourSystemsFrom_Pinned|TestMVTBootstrap|TestMVTTravelFailure|TestMVTRecover' -v`
Expected: FAIL — `mvtClaimAndTravel`/`mvtRecover` undefined; scope-pin assertion fails.

- [ ] **Step 3: Write `run_tour_coordinator_mvt_loop.go`**

```go
package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

const mvtReasonClaim = "claim"

// mvtClaimedSystem is the system the hull's solver scope is pinned to ("" before the
// first CLAIM).
func (h *RunTourCoordinatorHandler) mvtClaimedSystem(cmd *RunTourCoordinatorCommand) string {
	st := h.mvtState(cmd)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.claimed
}

// mvtClaimAndTravel runs CLAIM from wherever the hull stands. Every failure resolves to
// "stay where you are".
func (h *RunTourCoordinatorHandler) mvtClaimAndTravel(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, reason string) (bool, error) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return false, err
	}
	current := ship.CurrentLocation().SystemSymbol
	ranked, rerr := h.mvtRank(ctx, cmd, ship)
	if rerr != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT CLAIM: ranker unreadable, staying", map[string]interface{}{"hull": cmd.ShipSymbol, "error": rerr.Error()})
		st := h.mvtState(cmd)
		st.mu.Lock()
		st.claimed = current
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTrade, current, 0, 0, 0, mvtReasonRankerUnreadable)
		return false, nil
	}
	return h.mvtTravelTo(ctx, cmd, response, ranked, reason)
}

// mvtTravelTo claims ranked[0] and moves there; ranked[0] == current (or nothing ranked)
// is a stay that re-stamps presence in the registry.
func (h *RunTourCoordinatorHandler) mvtTravelTo(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, ranked []mvt.ScoredSystem, reason string) (bool, error) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return false, err
	}
	current := ship.CurrentLocation().SystemSymbol
	now := h.clock.Now()
	st := h.mvtState(cmd)
	logger := common.LoggerFromContext(ctx)

	if len(ranked) == 0 || ranked[0].System == current {
		stayReason := mvt.ReasonStay
		if len(ranked) == 0 {
			stayReason = mvt.ReasonNoAlternative
		}
		h.mvtStampPresence(ctx, cmd, current)
		st.mu.Lock()
		st.claimed = current
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTrade, current, 0, 0, 0, stayReason)
		return false, nil
	}

	target := ranked[0]
	h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateClaim, current, 0, target.Score, target.TravelPerUnit, reason)
	h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTravel, target.System, 0, target.Score, target.TravelPerUnit, mvtReasonClaim)
	if err := h.mvt.claims.Upsert(ctx, cmd.PlayerID, cmd.ShipSymbol, target.System, now); err != nil {
		logger.Log("WARNING", "MVT CLAIM: registry write failed, staying", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		return false, nil
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: target.System, TargetWaypoint: target.EntryWaypoint})
	jumps := target.Hops
	if jumps < 1 {
		jumps = 1
	}
	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, target.EntryWaypoint, cmd.PlayerID, jumps); terr != nil {
		logger.Log("WARNING", "MVT TRAVEL failed; claim released", map[string]interface{}{"hull": cmd.ShipSymbol, "target": target.System, "error": terr.Error()})
		_ = h.mvt.claims.Release(ctx, cmd.PlayerID, cmd.ShipSymbol)
		h.persistReposition(ctx, cmd, RepositionEpisode{})
		st.mu.Lock()
		st.travelFailures++
		if st.travelFailures >= mvtTravelFailureCap {
			st.travelFailures = 0
			st.holdSells = cmd.YieldWindowSells
		}
		st.claimed = current
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, current, 0, target.Score, target.TravelPerUnit, mvtReasonTravelFailed)
		return false, nil
	}
	_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, h.clock.Now())
	h.persistReposition(ctx, cmd, RepositionEpisode{})
	st.mu.Lock()
	st.claimed = target.System
	st.yield.Reset()
	st.basis = map[string][]mvtLot{}
	st.travelFailures = 0
	st.mu.Unlock()
	h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, target.System, 0, target.Score, target.TravelPerUnit, mvtReasonArrived)
	return true, nil
}

// mvtStampPresence records "I am here" so other hulls' rankers see the system occupied
// through the ledger's shadows, and recovery finds an arrived row.
func (h *RunTourCoordinatorHandler) mvtStampPresence(ctx context.Context, cmd *RunTourCoordinatorCommand, system string) {
	now := h.clock.Now()
	if err := h.mvt.claims.Upsert(ctx, cmd.PlayerID, cmd.ShipSymbol, system, now); err != nil {
		return
	}
	_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, now)
}

// mvtRecover restores the loop after a container restart from the claim row and the
// in-flight reposition resume. A hull with no usable claim bootstraps from where it stands.
func (h *RunTourCoordinatorHandler) mvtRecover(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, resumed repositionEpisode) error {
	if h.mvt.claims == nil {
		return nil
	}
	claim, ok, err := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT recover: claim unreadable, bootstrapping", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		ok = false
	}
	st := h.mvtState(cmd)
	switch {
	case ok && claim.ArrivedAt != nil:
		st.mu.Lock()
		st.claimed = claim.System
		st.mu.Unlock()
		return nil
	case ok && resumed.repositioned:
		_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, h.clock.Now())
		st.mu.Lock()
		st.claimed = resumed.toSystem
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, resumed.toSystem, 0, 0, 0, mvtReasonArrived)
		return nil
	case ok:
		_ = h.mvt.claims.Release(ctx, cmd.PlayerID, cmd.ShipSymbol)
	}
	_, err = h.mvtClaimAndTravel(ctx, cmd, response, mvtReasonBootstrap)
	return err
}

// mvtAfterTour runs after each productive tour. Task 11 adds the departure rule; here it
// only guarantees the registry knows where the hull is.
func (h *RunTourCoordinatorHandler) mvtAfterTour(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse) error {
	if h.mvtClaimedSystem(cmd) != "" {
		return nil
	}
	if ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID); err == nil && ship != nil && ship.CurrentLocation() != nil {
		h.mvtStampPresence(ctx, cmd, ship.CurrentLocation().SystemSymbol)
		st := h.mvtState(cmd)
		st.mu.Lock()
		st.claimed = ship.CurrentLocation().SystemSymbol
		st.mu.Unlock()
	}
	return nil
}
```

- [ ] **Step 4: Hook the loop into the existing paths**

1. `run_tour_coordinator_planning.go:399` — first lines of `tourSystemsFrom`:

```go
	if cmd.MVTLoop {
		if claimed := h.mvtClaimedSystem(cmd); claimed != "" {
			return []string{claimed}
		}
		return []string{home}
	}
```

2. `run_tour_coordinator.go` `execute`, inside `if continuous {` after `launchedLaden = h.launchedStuckLaden(ctx, cmd)`:

```go
		if cmd.MVTLoop {
			if err := h.mvtRecover(ctx, cmd, response, episode); err != nil {
				return err
			}
		}
```

3. Line 868: `if !cmd.MVTLoop { dispersed, derr := h.maybeDisperseFromCrowdedGround(...) ... }` — keep the existing body inside the guard and leave the old-path variables' zero values when skipped.

4. `afterProductiveTour`, after the shadow guard from Task 9:

```go
	if cmd.MVTLoop {
		return time.Time{}, h.mvtAfterTour(ctx, cmd, response)
	}
```

5. `rescueStarvedGround`, immediately before `repositioned, rerr := h.maybeReposition(...)`:

```go
	if cmd.MVTLoop {
		return h.mvtClaimAndTravel(ctx, cmd, response, mvtReasonEmpty)
	}
```

- [ ] **Step 5: Build and run the tests**

Run: `go build ./... && go test ./internal/application/trading/commands/ -run 'TestTourSystemsFrom_Pinned|TestMVTBootstrap|TestMVTTravelFailure|TestMVTRecover|TestMVTShadow' -v && go test ./internal/application/trading/commands/ 2>&1 | tail -3`
Expected: all PASS; the rest of the package unchanged (every hook is behind `cmd.MVTLoop`).

- [ ] **Step 6: Commit**

```bash
git add internal/application/trading/commands/run_tour_coordinator_mvt_loop.go internal/application/trading/commands/run_tour_coordinator_mvt_loop_test.go internal/application/trading/commands/run_tour_coordinator_planning.go internal/application/trading/commands/run_tour_coordinator.go
git commit -m "feat(mvt): hull loop CLAIM/TRAVEL with pinned solver scope and restart recovery behind trade-mvt (<bead>)"
```

### Task 11: Hull loop — per-sell yield, departure at tour end, hold after failures (Wave C; migration step 2b, after Task 10)

**Files:**
- Modify: `internal/application/trading/commands/run_tour_coordinator_mvt_loop.go` (`mvtAfterTour`, new `mvtObserveLeg`)
- Modify: `internal/application/trading/commands/run_tour_coordinator_trades.go:629` (after the `h.telemetry.RecordLeg(...)` call)
- Test: `internal/application/trading/commands/run_tour_coordinator_mvt_loop_test.go` (append)

**Interfaces:**
- Consumes: Task 10 (`mvtTravelTo`, `mvtState`, `mvtRank`, `mvtRecord`); `mvt.Decide`, `mvt.BestAlternative`; `trading.TourLegTelemetry` as built at the `RecordLeg` site.
- Produces: `func (h *RunTourCoordinatorHandler) mvtObserveLeg(cmd *RunTourCoordinatorCommand, leg trading.TourLegTelemetry)`.

Behaviour (exact):
- **`mvtObserveLeg`**: return unless `cmd.MVTLoop && leg.RealizedUnits > 0`. Buy → append `mvtLot{units, RealizedUnitPrice}` to `state.basis[leg.Good]`. Sell → consume FIFO from `state.basis[leg.Good]`; if no lot exists, return (no basis, no observation); `marginPerUnit = Σ(sellPrice − lotPrice) × consumed / consumed`; `state.yield.Observe(marginPerUnit, consumed, leg.RealizedAt)`; if `state.holdSells > 0` decrement it.
- **`mvtAfterTour`** (replaces the Task 10 stub): load ship, `current`; if `state.claimed == ""` stamp presence and set it (Task 10 behaviour); `ranked, err := mvtRank` → on error record `TRADE→TRADE` reason `mvtReasonRankerUnreadable` and return nil; `alt, hasAlt := mvt.BestAlternative(ranked, current)`; `d := mvt.Decide(state.yield, alt.Score, hasAlt)`; if `state.holdSells > 0` record `TRADE→TRADE` with reason `mvtReasonHold` and return nil; if `!d.Leave` record `TRADE→TRADE` with `d.Reason`, `d.YieldHere`, `alt.Score`, `alt.TravelPerUnit` and return nil; else `_, err := h.mvtTravelTo(ctx, cmd, response, ranked, d.Reason)` and return err. The `TRADE→CLAIM` row written inside `mvtTravelTo` carries `yieldHere = 0`; pass `d.YieldHere` through by adding a `yieldHere float64` parameter to `mvtTravelTo` (update Task 10's three callers: bootstrap and empty pass 0).

- [ ] **Step 1: Write the failing tests** (append to `run_tour_coordinator_mvt_loop_test.go`)

```go
func mvtSellLeg(good string, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: "TOUR-MVT", Waypoint: "X1-S1-SNK", Good: good, IsBuy: false,
		RealizedUnits: units, RealizedUnitPrice: price, RealizedAt: at, PlayerID: 1}
}

func mvtBuyLeg(good string, units, price int, at time.Time) trading.TourLegTelemetry {
	l := mvtSellLeg(good, units, price, at)
	l.IsBuy, l.Waypoint = true, "X1-S1-SRC"
	return l
}

func TestMVTObserveLeg_FIFOMarginFeedsTracker(t *testing.T) {
	fx := repositionFixture()
	h, _, _ := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 10)
	cmd := mvtCmd(t)
	cmd.YieldMinSells = 1
	t0 := time.Now()
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 10, 100, t0))
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 10, 200, t0.Add(time.Second)))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 15, 300, t0.Add(2*time.Second))) // (10×200 + 5×100)/15 = 166.67/unit
	h.mvtObserveLeg(cmd, mvtSellLeg("GOLD", 5, 999, t0.Add(3*time.Second)))  // no basis → ignored
	st := h.mvtState(cmd)
	est, ok := st.yield.Estimate()
	if !ok || est < 166.6 || est > 166.7 || st.yield.Sells() != 1 {
		t.Fatalf("estimate=%v ok=%v sells=%d", est, ok, st.yield.Sells())
	}
	old := mvtCmd(t)
	old.MVTLoop = false
	h.mvtObserveLeg(old, mvtBuyLeg("IRON", 1, 1, t0))
	if len(h.mvtState(old).basis["IRON"]) != 1 { // old cmd shares the hull symbol; only the flag differs
		t.Fatal("old path must not record lots")
	}
}

func TestMVTAfterTour_LeavesWhenYieldBelowAlternative(t *testing.T) {
	fx := repositionFixture()
	h, claims, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 500, 500)
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	t0 := time.Now()
	for i := 0; i < 3; i++ { // warm: 1 credit/unit here, far below S2's 100/unit
		st.yield.Observe(1, 10, t0.Add(time.Duration(i)*time.Second))
	}
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" {
		t.Fatalf("claim = %+v ok=%v", c, ok)
	}
	if got := trans.rows[0]; got.Reason != mvt.ReasonYieldBelow || got.To != mvt.StateClaim || got.YieldHere != 1 {
		t.Fatalf("first transition = %+v", got)
	}
}

func TestMVTAfterTour_ColdStartStays(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	st.yield.Observe(1, 10, time.Now()) // 1 sell < yield_min_sells 3
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if err := h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{}); err != nil {
		t.Fatalf("after tour: %v", err)
	}
	if got := trans.last(); got.Reason != mvt.ReasonColdStart || got.To != mvt.StateTrade || len(fx.jumps) != 0 {
		t.Fatalf("cold start must stay: %+v jumps=%v", got, fx.jumps)
	}
}

func TestMVTAfterTour_HoldAfterRepeatedTravelFailures(t *testing.T) {
	fx := repositionFixture()
	h, _, trans := mvtHandler(t, fx, rateFloorPlanner(feasiblePlan(600000, 600000)), 10, 500)
	cmd := mvtCmd(t)
	st := h.mvtState(cmd)
	st.claimed = "X1-S1"
	st.holdSells = 2
	for i := 0; i < 3; i++ {
		st.yield.Observe(1, 10, time.Now())
	}
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	_ = h.mvtAfterTour(ctx, cmd, &RunTourCoordinatorResponse{})
	if got := trans.last(); got.Reason != mvtReasonHold || len(fx.jumps) != 0 {
		t.Fatalf("hold must block departure: %+v", got)
	}
	t0 := time.Now()
	h.mvtObserveLeg(cmd, mvtBuyLeg("IRON", 2, 1, t0))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(time.Second)))
	h.mvtObserveLeg(cmd, mvtSellLeg("IRON", 1, 2, t0.Add(2*time.Second)))
	if st.holdSells != 0 {
		t.Fatalf("holdSells = %d after two sells", st.holdSells)
	}
}

func TestMVTEmptyPlan_ClaimsImmediatelyIgnoringColdStart(t *testing.T) {
	fx := repositionFixture()
	// Home is dead from the first plan; S2 is rich. No sells ever happen at home.
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			return infeasibleTour()
		}
		return feasiblePlan(600000, 600000)
	}}
	h, claims, trans := mvtHandler(t, fx, planner, 10, 500)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	if _, err := h.Handle(ctx, mvtCmd(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	c, ok, _ := claims.Get(ctx, 1, "TOUR-MVT")
	if !ok || c.System != "X1-S2" {
		t.Fatalf("empty home must claim S2: %+v ok=%v", c, ok)
	}
	seenEmpty := false
	for _, r := range trans.rows {
		if r.Reason == mvtReasonEmpty || r.Reason == mvtReasonBootstrap {
			seenEmpty = true
		}
	}
	if !seenEmpty {
		t.Fatalf("no empty/bootstrap CLAIM in %+v", trans.rows)
	}
}
```

Add the `routing` import for `routing.TourShipState`/`routing.TourPlan` (the package path used by `run_tour_coordinator_test.go`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/application/trading/commands/ -run 'TestMVTObserveLeg|TestMVTAfterTour|TestMVTEmptyPlan' -v`
Expected: FAIL — `mvtObserveLeg` undefined; `mvtAfterTour` records no departure.

- [ ] **Step 3: Implement**

Append to `run_tour_coordinator_mvt_loop.go`:

```go
// mvtObserveLeg feeds one realised leg into the hull's yield tracker. Buys open FIFO
// lots; sells consume them and observe margin per unit. Sells with no lot are not evidence.
func (h *RunTourCoordinatorHandler) mvtObserveLeg(cmd *RunTourCoordinatorCommand, leg trading.TourLegTelemetry) {
	if !cmd.MVTLoop || leg.RealizedUnits <= 0 {
		return
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	defer st.mu.Unlock()
	if leg.IsBuy {
		st.basis[leg.Good] = append(st.basis[leg.Good], mvtLot{units: leg.RealizedUnits, price: leg.RealizedUnitPrice})
		return
	}
	lots := st.basis[leg.Good]
	if len(lots) == 0 {
		return
	}
	need, consumed, margin := leg.RealizedUnits, 0, 0.0
	for need > 0 && len(lots) > 0 {
		take := lots[0].units
		if take > need {
			take = need
		}
		margin += float64(leg.RealizedUnitPrice-lots[0].price) * float64(take)
		consumed += take
		need -= take
		lots[0].units -= take
		if lots[0].units == 0 {
			lots = lots[1:]
		}
	}
	st.basis[leg.Good] = lots
	if consumed == 0 {
		return
	}
	st.yield.Observe(margin/float64(consumed), consumed, leg.RealizedAt)
	if st.holdSells > 0 {
		st.holdSells--
	}
}
```

Replace `mvtAfterTour` with:

```go
// mvtAfterTour applies the departure rule after a productive tour. Deviation from spec
// §1 recorded in the plan: the EWMA updates per sell, the decision is taken at tour end.
func (h *RunTourCoordinatorHandler) mvtAfterTour(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse) error {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return nil
	}
	current := ship.CurrentLocation().SystemSymbol
	st := h.mvtState(cmd)
	st.mu.Lock()
	if st.claimed == "" {
		st.claimed = current
		st.mu.Unlock()
		h.mvtStampPresence(ctx, cmd, current)
		st.mu.Lock()
	}
	hold := st.holdSells
	st.mu.Unlock()

	ranked, rerr := h.mvtRank(ctx, cmd, ship)
	if rerr != nil {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, 0, 0, 0, mvtReasonRankerUnreadable)
		return nil
	}
	alt, hasAlt := mvt.BestAlternative(ranked, current)
	st.mu.Lock()
	d := mvt.Decide(st.yield, alt.Score, hasAlt)
	st.mu.Unlock()
	if hold > 0 {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, d.YieldHere, alt.Score, alt.TravelPerUnit, mvtReasonHold)
		return nil
	}
	if !d.Leave {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, d.YieldHere, alt.Score, alt.TravelPerUnit, d.Reason)
		return nil
	}
	_, err = h.mvtTravelTo(ctx, cmd, response, ranked, d.Reason, d.YieldHere)
	return err
}
```

Change `mvtTravelTo`'s signature to `(ctx, cmd, response, ranked []mvt.ScoredSystem, reason string, yieldHere float64)` and use `yieldHere` in its `TRADE→CLAIM` record; update `mvtClaimAndTravel` to pass `0`.

At `run_tour_coordinator_trades.go:629`, bind the telemetry literal to a local before recording and add the hook:

```go
	leg := trading.TourLegTelemetry{ /* the existing literal, unchanged */ }
	if err := h.telemetry.RecordLeg(ctx, leg); err != nil { /* existing handling */ }
	h.mvtObserveLeg(cmd, leg)
```

Keep the existing error handling of `RecordLeg` exactly as it is; only the literal moves into `leg`.

- [ ] **Step 4: Build and run the tests**

Run: `go build ./... && go test ./internal/application/trading/commands/ -run 'TestMVT|TestTourSystemsFrom_Pinned' -v && go test ./internal/application/trading/commands/ 2>&1 | tail -3`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/application/trading/commands/run_tour_coordinator_mvt_loop.go internal/application/trading/commands/run_tour_coordinator_mvt_loop_test.go internal/application/trading/commands/run_tour_coordinator_trades.go
git commit -m "feat(mvt): per-sell yield EWMA and tour-end departure rule; empty-system exit; hold after 3 travel failures (<bead>)"
```

**Step 2 ships here** only after Task 12's replay passes the gate. Then Task 14 arms five hulls.

### Task 12: Replay harness over the last 24 h of legs (Wave C; the step-2 ship gate)

**Files:**
- Create: `internal/domain/trading/mvt/replay/replay.go`
- Create: `cmd/mvt-replay/main.go`
- Test: `internal/domain/trading/mvt/replay/replay_test.go`

**Interfaces:**
- Consumes: `mvt.Rank/Candidate/Costs/Hull/BestAlternative` (Task 1), `mvt.NewYieldTracker/Decide` (Task 2), `mvt.ComputeFleetStats` (Task 3), `trading.TourLegTelemetry`, `shared.ExtractSystemSymbol`; for the binary: `config.MustLoadConfig("")`, `database.NewConnection(&cfg.Database)`, `persistence.NewTourTelemetryRepository(db).ListByPlayer(ctx, cfg.Captain.PlayerID, since)`, `persistence.GateEdgeModel{SystemSymbol, ConnectedSystem, EraID, UnderConstruction}` (table `gate_edges`), `persistence.EraModel{EraID}` (`closed_at IS NULL`).
- Produces:
  - `type Config struct { Window time.Duration; Horizon time.Duration; BoundaryGap time.Duration; YieldWindowSells, YieldMinSells, ClaimReachHops, TollSecondsPerHop int; GateFee int64 }`
  - `type Decision struct { Hull string; At time.Time; From, ActualNext, LoopNext, Reason string; YieldHere, BestAlternative, TravelCost float64; Stranded bool }`
  - `type Report struct { Hulls, Boundaries, ActualJumps, LoopJumps int; ActualMarginPerHull, LoopMarginPerHull float64; Stranded []Decision; Decisions []Decision }` with `func (r Report) Gate() (pass bool, why string)` — pass iff `LoopJumps < ActualJumps && LoopMarginPerHull >= ActualMarginPerHull`.
  - `func Run(legs []trading.TourLegTelemetry, neighbours map[string][]string, cfg Config) Report`

Algorithm (exact):
1. Keep legs with `RealizedUnits > 0`; group by hull; sort by `RealizedAt`; `stats := mvt.ComputeFleetStats(legs, cfg.Window)`.
2. FIFO basis per (hull, good) as in `ComputeFleetStats`; each sell gets `marginPerUnit` and `margin`.
3. Per system, a time-sorted list of sells `(at, units, margin)` by any hull. `yield(S, t0, t1) = (Σ margin, Σ units)` over sells in `[t0, t1)`.
4. `hops(from, to)` = BFS over `neighbours` capped at `cfg.ClaimReachHops`; unreachable = excluded.
5. A boundary for hull `h` is the last leg of a visit: the next leg is in a different system or is more than `cfg.BoundaryGap` later, or there is no next leg (only when the visit ended at least `cfg.Horizon` before the last leg in the data). `From` = visit system `X`, `ActualNext` = the next leg's system (`X` when the hull stayed).
6. At each boundary time `t`: rebuild the tracker from `h`'s sells in this visit (`Observe` each); candidates = `X` plus every system within reach, each with `YieldCredits, DepthUnits = yield(S, t−Horizon, t)`, `InTransit` = simulated claims on `S` whose simulated arrival `> t` (arrival = decision time + hops × TollSecondsPerHop), dropping `DepthUnits == 0`; `hull := mvt.Hull{Symbol: h, System: X, CargoCapacity: max units of any single leg by h (fallback 1), CreditsPerSec: tracker.CreditsPerSec(t)}`; `costs := mvt.Costs{TollSecondsPerHop, GateFeeFromCurrent: cfg.GateFee, FleetDrawPerVisit: stats.MeanMarginPerSystemVisit, FleetCreditsPerSec: stats.CreditsPerHullSec}`; `ranked := mvt.Rank(...)`; `alt, hasAlt := mvt.BestAlternative(ranked, X)`; `d := mvt.Decide(tracker, alt.Score, hasAlt)`.
7. `LoopNext = X`; if `d.Leave`, or the visit had zero sells and `len(ranked) > 0 && ranked[0].System != X` (exhaustion exit), then `LoopNext = ranked[0].System` and a simulated claim is added.
8. `ActualJumps += (ActualNext != X)`, `LoopJumps += (LoopNext != X)`. `ActualMargin += yield-by-h in [t, t+Horizon)`; `LoopMargin += unitsSoldBy h in [t, t+Horizon) × rate`, where `rate` is `margin/units of yield(LoopNext, t, t+Horizon)` when that window has sells, else `margin/units of yield(LoopNext, t−Horizon, t)` — the trailing rate the ranker scored the destination on — else 0. `Stranded = yield(LoopNext, t, t+Horizon).units == 0`, for stays and jumps alike. *(Amended in the step-2 review, sp-pfayi: the original valued an unobservable stay at 0 without flagging it while flagging the identical jump, which zeroed 30% of the fleet's margin on the live run and decided the gate; the listing in step 3 predates this amendment. Round 2 of the same review: the binary now prints the gate under four valuations of the unobservable decisions — trailing-rate (this headline), observable-only, neutral (the hull's own actual), zero-credit — plus a ROBUST line that passes only when all four do; on the live 24 h read the headline alone passes, so which valuation is the ship-gate instrument is the plan owner's ruling, owed together with the sign-off on this amendment before Task 14. The measured tables are recorded in the step-2 bead, sp-pfayi.)*
9. `*MarginPerHull = margin / Hulls`.

- [ ] **Step 1: Write the failing test**

```go
package replay

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func rl(ship, wp, good string, isBuy bool, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{ShipSymbol: ship, Waypoint: wp, Good: good, IsBuy: isBuy,
		RealizedUnits: units, RealizedUnitPrice: price, RealizedAt: at, PlayerID: 1}
}

func cfg() Config {
	return Config{Window: 24 * time.Hour, Horizon: time.Hour, BoundaryGap: 10 * time.Minute,
		YieldWindowSells: 8, YieldMinSells: 1, ClaimReachHops: 2, TollSecondsPerHop: 1}
}

func TestRun_HullThatJumpedToAPoorerSystemWouldHaveStayed(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		// H1 in X1-A: two sells at 100/unit, then it jumped to X1-B (actual), where it earned 10/unit.
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(5*time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(10*time.Minute)),
		rl("H1", "X1-A-2", "IRON", false, 10, 200, t0.Add(15*time.Minute)),
		rl("H1", "X1-B-1", "IRON", true, 10, 100, t0.Add(30*time.Minute)),
		rl("H1", "X1-B-2", "IRON", false, 10, 110, t0.Add(35*time.Minute)),
		// H2 keeps X1-A rich during the next hour (the loop's evidence that staying pays).
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(20*time.Minute)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(25*time.Minute)),
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(50*time.Minute)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(55*time.Minute)),
		rl("H2", "X1-A-1", "IRON", true, 10, 100, t0.Add(3*time.Hour)),
		rl("H2", "X1-A-2", "IRON", false, 10, 200, t0.Add(3*time.Hour+5*time.Minute)),
	}
	r := Run(legs, map[string][]string{"X1-A": {"X1-B"}, "X1-B": {"X1-A"}}, cfg())
	if r.Hulls != 2 || r.Boundaries == 0 {
		t.Fatalf("hulls=%d boundaries=%d", r.Hulls, r.Boundaries)
	}
	var first *Decision
	for i := range r.Decisions {
		if r.Decisions[i].Hull == "H1" && r.Decisions[i].From == "X1-A" {
			first = &r.Decisions[i]
			break
		}
	}
	if first == nil || first.ActualNext != "X1-B" || first.LoopNext != "X1-A" {
		t.Fatalf("H1's X1-A boundary = %+v", first)
	}
	if r.LoopJumps >= r.ActualJumps {
		t.Fatalf("loop jumps %d must be below actual %d", r.LoopJumps, r.ActualJumps)
	}
	if pass, why := r.Gate(); !pass {
		t.Fatalf("gate should pass: %s", why)
	}
}

func TestRun_EmptyInputAndUnreachableSystems(t *testing.T) {
	r := Run(nil, nil, cfg())
	if r.Hulls != 0 || r.Boundaries != 0 {
		t.Fatalf("empty = %+v", r)
	}
	if pass, _ := r.Gate(); pass {
		t.Fatal("no data can never pass the gate")
	}
	t0 := time.Unix(1_700_000_000, 0)
	legs := []trading.TourLegTelemetry{
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0),
		rl("H1", "X1-A-2", "IRON", false, 10, 101, t0.Add(time.Minute)),
		rl("H1", "X1-A-1", "IRON", true, 10, 100, t0.Add(3*time.Hour)),
		rl("H9", "X1-Z-1", "IRON", true, 10, 1, t0),
		rl("H9", "X1-Z-2", "IRON", false, 10, 5000, t0.Add(time.Minute)),
	}
	r = Run(legs, map[string][]string{}, cfg()) // X1-Z unreachable from X1-A
	for _, d := range r.Decisions {
		if d.Hull == "H1" && d.LoopNext == "X1-Z" {
			t.Fatal("unreachable system must never be chosen")
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/domain/trading/mvt/replay/ -v`
Expected: FAIL — package missing.

- [ ] **Step 3: Write `replay.go`**

```go
// Package replay runs the MVT ranker and departure rule over recorded tour legs and
// reports what the loop would have done against what the fleet did. Its primary metric is
// jumps; the ship gate is "jumps down and margin per hull not down".
package replay

import (
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

type Config struct {
	Window            time.Duration
	Horizon           time.Duration
	BoundaryGap       time.Duration
	YieldWindowSells  int
	YieldMinSells     int
	ClaimReachHops    int
	TollSecondsPerHop int
	GateFee           int64
}

type Decision struct {
	Hull            string
	At              time.Time
	From            string
	ActualNext      string
	LoopNext        string
	Reason          string
	YieldHere       float64
	BestAlternative float64
	TravelCost      float64
	Stranded        bool
}

type Report struct {
	Hulls, Boundaries   int
	ActualJumps         int
	LoopJumps           int
	ActualMarginPerHull float64
	LoopMarginPerHull   float64
	Stranded            []Decision
	Decisions           []Decision
}

// Gate is the spec §7 ship gate.
func (r Report) Gate() (bool, string) {
	if r.Boundaries == 0 {
		return false, "no boundaries in the data"
	}
	if r.LoopJumps >= r.ActualJumps {
		return false, fmt.Sprintf("jumps not down: loop %d vs actual %d", r.LoopJumps, r.ActualJumps)
	}
	if r.LoopMarginPerHull < r.ActualMarginPerHull {
		return false, fmt.Sprintf("margin per hull down: loop %.0f vs actual %.0f", r.LoopMarginPerHull, r.ActualMarginPerHull)
	}
	return true, fmt.Sprintf("jumps %d→%d, margin/hull %.0f→%.0f", r.ActualJumps, r.LoopJumps, r.ActualMarginPerHull, r.LoopMarginPerHull)
}

type sell struct {
	hull   string
	at     time.Time
	units  int
	margin float64
}

type lot struct{ units, price int }

type claim struct {
	system  string
	arrival time.Time
}

func hopsFrom(neighbours map[string][]string, origin string, maxHops int) map[string]int {
	dist := map[string]int{origin: 0}
	frontier := []string{origin}
	for len(frontier) > 0 && maxHops > 0 {
		var next []string
		for _, s := range frontier {
			for _, n := range neighbours[s] {
				if _, seen := dist[n]; seen {
					continue
				}
				dist[n] = dist[s] + 1
				if dist[n] < maxHops {
					next = append(next, n)
				}
			}
		}
		frontier = next
	}
	return dist
}

func Run(legs []trading.TourLegTelemetry, neighbours map[string][]string, cfg Config) Report {
	byHull := map[string][]trading.TourLegTelemetry{}
	var last time.Time
	for _, l := range legs {
		if l.RealizedUnits <= 0 {
			continue
		}
		byHull[l.ShipSymbol] = append(byHull[l.ShipSymbol], l)
		if l.RealizedAt.After(last) {
			last = l.RealizedAt
		}
	}
	r := Report{Hulls: len(byHull)}
	if r.Hulls == 0 {
		return r
	}
	stats := mvt.ComputeFleetStats(legs, cfg.Window)

	// Pass 1: lot-match every sell so system yields are known for any window.
	sellsBySystem := map[string][]sell{}
	sellsByHull := map[string][]sell{}
	capacity := map[string]int{}
	type visit struct {
		system string
		start  int
		end    int // inclusive leg index
	}
	visits := map[string][]visit{}
	marginPerUnitOf := map[string]map[int]float64{} // hull → leg index → margin/unit (sells only)
	for hull, hl := range byHull {
		sort.Slice(hl, func(i, j int) bool { return hl[i].RealizedAt.Before(hl[j].RealizedAt) })
		lots := map[string][]lot{}
		marginPerUnitOf[hull] = map[int]float64{}
		cur := visit{system: "", start: 0}
		for i, l := range hl {
			if l.RealizedUnits > capacity[hull] {
				capacity[hull] = l.RealizedUnits
			}
			sys := shared.ExtractSystemSymbol(l.Waypoint)
			gap := i > 0 && l.RealizedAt.Sub(hl[i-1].RealizedAt) > cfg.BoundaryGap
			if sys != cur.system || gap {
				if cur.system != "" {
					cur.end = i - 1
					visits[hull] = append(visits[hull], cur)
				}
				cur = visit{system: sys, start: i}
			}
			if l.IsBuy {
				lots[l.Good] = append(lots[l.Good], lot{l.RealizedUnits, l.RealizedUnitPrice})
				continue
			}
			q := lots[l.Good]
			need, consumed, margin := l.RealizedUnits, 0, 0.0
			for need > 0 && len(q) > 0 {
				take := q[0].units
				if take > need {
					take = need
				}
				margin += float64(l.RealizedUnitPrice-q[0].price) * float64(take)
				consumed += take
				need -= take
				q[0].units -= take
				if q[0].units == 0 {
					q = q[1:]
				}
			}
			lots[l.Good] = q
			if consumed == 0 {
				continue
			}
			s := sell{hull: hull, at: l.RealizedAt, units: consumed, margin: margin}
			sellsBySystem[sys] = append(sellsBySystem[sys], s)
			sellsByHull[hull] = append(sellsByHull[hull], s)
			marginPerUnitOf[hull][i] = margin / float64(consumed)
		}
		if cur.system != "" {
			cur.end = len(hl) - 1
			visits[hull] = append(visits[hull], cur)
		}
	}
	yield := func(system string, t0, t1 time.Time) (float64, int) {
		m, u := 0.0, 0
		for _, s := range sellsBySystem[system] {
			if !s.at.Before(t0) && s.at.Before(t1) {
				m += s.margin
				u += s.units
			}
		}
		return m, u
	}
	hullYield := func(hull string, t0, t1 time.Time) (float64, int) {
		m, u := 0.0, 0
		for _, s := range sellsByHull[hull] {
			if !s.at.Before(t0) && s.at.Before(t1) {
				m += s.margin
				u += s.units
			}
		}
		return m, u
	}

	// Pass 2: decisions at every visit boundary, in time order across hulls so simulated
	// claims penalise later decisions.
	type boundary struct {
		hull string
		v    visit
		next string
		at   time.Time
	}
	var bounds []boundary
	for hull, vs := range visits {
		hl := byHull[hull]
		for i, v := range vs {
			endAt := hl[v.end].RealizedAt
			next := v.system
			if i+1 < len(vs) {
				next = vs[i+1].system
			} else if last.Sub(endAt) < cfg.Horizon {
				continue // the visit had not ended when the data ends
			}
			bounds = append(bounds, boundary{hull: hull, v: v, next: next, at: endAt})
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i].at.Before(bounds[j].at) })

	var claims []claim
	actualMargin, loopMargin := 0.0, 0.0
	for _, b := range bounds {
		hl := byHull[b.hull]
		tracker := mvt.NewYieldTracker(cfg.YieldWindowSells, cfg.YieldMinSells)
		for i := b.v.start; i <= b.v.end; i++ {
			if mpu, ok := marginPerUnitOf[b.hull][i]; ok {
				tracker.Observe(mpu, hl[i].RealizedUnits, hl[i].RealizedAt)
			}
		}
		reach := hopsFrom(neighbours, b.v.system, cfg.ClaimReachHops)
		var cands []mvt.Candidate
		for sys, hops := range reach {
			credits, units := yield(sys, b.at.Add(-cfg.Horizon), b.at)
			if units == 0 {
				continue
			}
			inTransit := 0
			for _, c := range claims {
				if c.system == sys && c.arrival.After(b.at) {
					inTransit++
				}
			}
			cands = append(cands, mvt.Candidate{System: sys, Hops: hops, YieldCredits: credits, DepthUnits: units, InTransit: inTransit})
		}
		capHull := capacity[b.hull]
		if capHull < 1 {
			capHull = 1
		}
		hull := mvt.Hull{Symbol: b.hull, System: b.v.system, CargoCapacity: capHull, CreditsPerSec: tracker.CreditsPerSec(b.at)}
		costs := mvt.Costs{TollSecondsPerHop: cfg.TollSecondsPerHop, GateFeeFromCurrent: cfg.GateFee,
			FleetDrawPerVisit: stats.MeanMarginPerSystemVisit, FleetCreditsPerSec: stats.CreditsPerHullSec}
		ranked := mvt.Rank(hull, cands, costs)
		alt, hasAlt := mvt.BestAlternative(ranked, b.v.system)
		d := mvt.Decide(tracker, alt.Score, hasAlt)
		dec := Decision{Hull: b.hull, At: b.at, From: b.v.system, ActualNext: b.next, LoopNext: b.v.system,
			Reason: d.Reason, YieldHere: d.YieldHere, BestAlternative: alt.Score, TravelCost: alt.TravelPerUnit}
		exhausted := tracker.Sells() == 0 && len(ranked) > 0 && ranked[0].System != b.v.system
		if d.Leave || exhausted {
			dec.LoopNext = ranked[0].System
			if exhausted && !d.Leave {
				dec.Reason = "empty"
			}
			claims = append(claims, claim{system: dec.LoopNext, arrival: b.at.Add(time.Duration(ranked[0].Hops*cfg.TollSecondsPerHop) * time.Second)})
		}
		r.Boundaries++
		if dec.ActualNext != dec.From {
			r.ActualJumps++
		}
		am, _ := hullYield(b.hull, b.at, b.at.Add(cfg.Horizon))
		actualMargin += am
		if dec.LoopNext != dec.From {
			r.LoopJumps++
			lm, lu := yield(dec.LoopNext, b.at, b.at.Add(cfg.Horizon))
			_, hu := hullYield(b.hull, b.at, b.at.Add(cfg.Horizon))
			if lu > 0 {
				loopMargin += float64(hu) * lm / float64(lu)
			} else {
				dec.Stranded = true
			}
		} else {
			sm, su := yield(dec.From, b.at, b.at.Add(cfg.Horizon))
			_, hu := hullYield(b.hull, b.at, b.at.Add(cfg.Horizon))
			if su > 0 {
				loopMargin += float64(hu) * sm / float64(su)
			}
		}
		if dec.Stranded {
			r.Stranded = append(r.Stranded, dec)
		}
		r.Decisions = append(r.Decisions, dec)
	}
	r.ActualMarginPerHull = actualMargin / float64(r.Hulls)
	r.LoopMarginPerHull = loopMargin / float64(r.Hulls)
	return r
}
```

- [ ] **Step 4: Write `cmd/mvt-replay/main.go`**

```go
// mvt-replay runs the MVT ranker and departure rule over the last N hours of recorded tour
// legs and prints the spec §7 gate: jumps down AND margin per hull not down.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt/replay"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func main() {
	hours := flag.Int("hours", 24, "window of legs to replay")
	horizon := flag.Duration("horizon", time.Hour, "look-ahead used to value a decision")
	gap := flag.Duration("boundary-gap", 10*time.Minute, "idle gap that ends a visit")
	windowSells := flag.Int("yield-window-sells", 8, "EWMA window")
	minSells := flag.Int("yield-min-sells", 3, "cold-start guard")
	reach := flag.Int("claim-reach-hops", 2, "candidate radius")
	toll := flag.Int("toll-seconds", 361, "seconds per gate hop (jump cooldown)")
	fee := flag.Int64("gate-fee", 0, "credits per hop from the departure system")
	asJSON := flag.Bool("json", false, "print the full report as JSON")
	flag.Parse()

	cfg := config.MustLoadConfig("")
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(2)
	}
	ctx := context.Background()
	since := time.Now().Add(-time.Duration(*hours) * time.Hour)
	legs, err := persistence.NewTourTelemetryRepository(db).ListByPlayer(ctx, cfg.Captain.PlayerID, since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "legs:", err)
		os.Exit(2)
	}
	var era persistence.EraModel
	q := db.WithContext(ctx).Model(&persistence.GateEdgeModel{}).Where("under_construction = ?", false)
	if err := db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err == nil {
		q = q.Where("era_id = ?", era.EraID)
	}
	var edges []persistence.GateEdgeModel
	if err := q.Find(&edges).Error; err != nil {
		fmt.Fprintln(os.Stderr, "gate edges:", err)
		os.Exit(2)
	}
	neighbours := map[string][]string{}
	for _, e := range edges {
		neighbours[e.SystemSymbol] = append(neighbours[e.SystemSymbol], e.ConnectedSystem)
	}
	rep := replay.Run(legs, neighbours, replay.Config{
		Window: time.Duration(*hours) * time.Hour, Horizon: *horizon, BoundaryGap: *gap,
		YieldWindowSells: *windowSells, YieldMinSells: *minSells, ClaimReachHops: *reach,
		TollSecondsPerHop: *toll, GateFee: *fee,
	})
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		return
	}
	pass, why := rep.Gate()
	fmt.Printf("legs=%d hulls=%d boundaries=%d\n", len(legs), rep.Hulls, rep.Boundaries)
	fmt.Printf("jumps: actual=%d loop=%d\n", rep.ActualJumps, rep.LoopJumps)
	fmt.Printf("margin/hull over %s after each boundary: actual=%.0f loop=%.0f\n", *horizon, rep.ActualMarginPerHull, rep.LoopMarginPerHull)
	fmt.Printf("stranded (>%s with no sells at destination): %d\n", *horizon, len(rep.Stranded))
	for _, d := range rep.Stranded {
		fmt.Printf("  %s %s %s→%s\n", d.At.Format(time.RFC3339), d.Hull, d.From, d.LoopNext)
	}
	fmt.Printf("GATE: %v — %s\n", pass, why)
	if !pass {
		os.Exit(1)
	}
}
```

Check the `gate_edges` column name for `UnderConstruction` in `models_navigation.go` and use it verbatim in the `Where`.

- [ ] **Step 5: Run the tests, build the binary, run it against live data**

Run: `go test ./internal/domain/trading/mvt/replay/ -v && go build ./cmd/mvt-replay && ./mvt-replay -hours 24`
Expected: tests PASS; the binary prints the four lines and `GATE: true|false — …`. Record the printed numbers in the step-2 bead. Fit the knob defaults here: rerun with `-yield-min-sells 2/4`, `-claim-reach-hops 1/3`, `-yield-window-sells 4/12` and keep the spec defaults unless a variant beats them on both gate terms.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/trading/mvt/replay/replay.go internal/domain/trading/mvt/replay/replay_test.go cmd/mvt-replay/main.go
git commit -m "feat(mvt): replay harness over tour legs with the jumps-down/margin-not-down gate (<bead>)"
```

### Task 13: Specialist pool in the trade fleet coordinator (Wave C; migration step 3, gated on the cohort)

**Files:**
- Create: `internal/application/trading/commands/run_trade_fleet_coordinator_specialists.go`
- Modify: `internal/application/trading/commands/run_trade_fleet_coordinator.go` — struct fields (near `treasury TreasuryReader`), `reconcileOnce` (line 342; after `idle, running := partitionTradeFleet(ships)` at 364)
- Modify: `cmd/spacetraders-daemon/main.go` (near line 971, next to `tradeFleetCoordinatorHandler.SetTourLauncher(daemonServer)`)
- Test: `internal/application/trading/commands/run_trade_fleet_coordinator_specialists_test.go`

**Interfaces:**
- Consumes: `mvt.ComputeFleetStats/FleetStats/LaneStat` (Task 3), `mvt.PoolSize/IsFatLane` (Task 4), `mvt.ClaimRegistry` (Task 5), tags `tradeFleetMVT`/`tradeFleetLane`/`isTradeFleetTag` and command fields `SpecialistFractionPct, FatLaneMultiplePct, SpecialistCadenceMinutes` (Task 8); `trading.TourTelemetryRepository.ListByPlayer`; `GateFeeReader` (same package, `gate_fees.go:44`); `ship.SetDedicatedFleet(fleet string)`, `h.shipRepo.Save(ctx, ship)`, `ship.CurrentLocation().SystemSymbol`; `common.ContainerLogger` as `reconcileOnce` already holds it.
- Produces:
  - `func (h *RunTradeFleetCoordinatorHandler) SetSpecialistPorts(claims mvt.ClaimRegistry, telemetry trading.TourTelemetryRepository, fees GateFeeReader)`
  - `func (h *RunTradeFleetCoordinatorHandler) reconcileSpecialists(ctx, cmd, all, idle []*navigation.Ship, now time.Time, logger common.ContainerLogger) (promoted, demoted int)`
  - pure helper `func planSpecialists(all, idle []*navigation.Ship, fat []mvt.LaneStat, pool int, perHullMargin map[string]float64) (promote, demote []*navigation.Ship)`.

Behaviour (exact):
- Runs only when `h.specialistPorts` is wired and `now − h.specialistsAt ≥ cadence` (`cmd.SpecialistCadenceMinutes`, default `DefaultSpecialistCadenceMinutes`); stamps `h.specialistsAt = now` afterwards even when nothing changes.
- `legs := telemetry.ListByPlayer(ctx, int(cmd.PlayerID), now−24h)` → `stats := mvt.ComputeFleetStats(legs, 24h)`; on error log WARNING and return (0, 0).
- `fees := fees.GateFees(ctx, int(cmd.PlayerID))`; `fat` = lanes where `mvt.IsFatLane(l.MarginPerTranche, l.MeanTransitSeconds, stats.CreditsPerHullSec, fees[l.Source], stats.IntraMarginPerTranche, pct)` with `pct = cmd.FatLaneMultiplePct` (default `DefaultFatLaneMultiplePct` when 0).
- `N` = count of `all` with `isTradeFleetTag`; `pool := mvt.PoolSize(len(fat), N, fractionPct)` (`DefaultSpecialistFractionPct` when 0).
- `planSpecialists`: `current` = hulls tagged `trade-lane` in `all`; `idleLane`, `idleMVT` = idle hulls by tag.
  - Self-demotion first: every `idleLane` hull whose current system is neither `Source` nor `Sink` of any fat lane → demote.
  - If `len(current) − len(selfDemoted) > pool`: demote the excess from the remaining `idleLane`, lowest `perHullMargin` first (ties: symbol asc).
  - If `len(current) − demoted < pool`: promote `pool − (len(current) − demoted)` hulls from `idleMVT`: walk `fat` in order; for each lane pick the unpromoted idle hull whose system == `Source`, else == `Sink`, else the lowest symbol; stop when filled.
  - Running (non-idle) hulls are never touched.
- Apply: promote → `ship.SetDedicatedFleet(tradeFleetLane)`, `shipRepo.Save`, `claims.Release(hull)`; demote → `SetDedicatedFleet(tradeFleetMVT)`, `Save`. Log one INFO line per change with `hull, from_tag, to_tag, pool, fat_lanes`. A `Save` error logs WARNING and skips that hull.
- `reconcileOnce` calls `h.reconcileSpecialists(ctx, cmd, ships, idle, now, logger)` right after `partitionTradeFleet` and before the idle launch loop, so a re-tagged idle hull launches with the right override in the same tick.

- [ ] **Step 1: Write the failing tests**

Use the fleet coordinator's existing test ship builder if there is one (`grep -n "navigation.NewShip\|func .*Ship(" internal/application/trading/commands/run_trade_fleet_coordinator*_test.go`); otherwise add `tfIdleShipAt(t, symbol, tag, waypoint string) *navigation.Ship` next to Task 8's `tfIdleShip` that also sets the location.

```go
package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

func symbols(ships []*navigation.Ship) []string {
	out := []string{}
	for _, s := range ships {
		out = append(out, s.ShipSymbol())
	}
	return out
}

func TestPlanSpecialists_PromotesClosestIdleMVTHull(t *testing.T) {
	a := tfIdleShipAt(t, "M-A", "trade-mvt", "X1-A-1")
	b := tfIdleShipAt(t, "M-B", "trade-mvt", "X1-B-1")
	c := tfIdleShipAt(t, "M-C", "trade-mvt", "X1-C-1")
	running := tfIdleShipAt(t, "M-RUN", "trade-mvt", "X1-A-2") // in all, not idle
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD", MarginPerTranche: 9000}}
	promote, demote := planSpecialists([]*navigation.Ship{a, b, c, running}, []*navigation.Ship{a, b, c}, fat, 1, nil)
	if got := symbols(promote); len(got) != 1 || got[0] != "M-B" {
		t.Fatalf("promote = %v, want the hull in the lane's source", got)
	}
	if len(demote) != 0 {
		t.Fatalf("demote = %v", symbols(demote))
	}
	// No hull at source: sink wins; none at either: lowest symbol.
	promote, _ = planSpecialists([]*navigation.Ship{a, c}, []*navigation.Ship{a, c}, fat, 1, nil)
	if got := symbols(promote); got[0] != "M-A" {
		t.Fatalf("promote = %v, want the sink hull", got)
	}
	promote, _ = planSpecialists([]*navigation.Ship{c}, []*navigation.Ship{c}, fat, 1, nil)
	if got := symbols(promote); got[0] != "M-C" {
		t.Fatalf("promote = %v", got)
	}
}

func TestPlanSpecialists_ShrinkDemotesLowestMarginAndSelfDemotesOrphans(t *testing.T) {
	l1 := tfIdleShipAt(t, "L-1", "trade-lane", "X1-A-1")
	l2 := tfIdleShipAt(t, "L-2", "trade-lane", "X1-B-1")
	l3 := tfIdleShipAt(t, "L-3", "trade-lane", "X1-Z-1") // no fat lane touches X1-Z
	lRun := tfIdleShipAt(t, "L-RUN", "trade-lane", "X1-Z-2")
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	margins := map[string]float64{"L-1": 100, "L-2": 5000}
	promote, demote := planSpecialists([]*navigation.Ship{l1, l2, l3, lRun}, []*navigation.Ship{l1, l2, l3}, fat, 1, margins)
	if len(promote) != 0 {
		t.Fatalf("promote = %v", symbols(promote))
	}
	got := symbols(demote)
	// L-3 self-demotes (orphan); pool 1 with 4 specialists → 2 more must go but only idle
	// ones can: L-1 (lowest margin) goes; L-2 stays because L-RUN cannot be touched yet.
	want := map[string]bool{"L-3": true, "L-1": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("demote = %v, want L-3 and L-1", got)
	}
}

func TestPlanSpecialists_PoolZeroTouchesNothing(t *testing.T) {
	m := tfIdleShipAt(t, "M-A", "trade-mvt", "X1-A-1")
	promote, demote := planSpecialists([]*navigation.Ship{m}, []*navigation.Ship{m}, nil, 0, nil)
	if len(promote)+len(demote) != 0 {
		t.Fatal("N=1 → pool 0 → no changes")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/application/trading/commands/ -run TestPlanSpecialists -v`
Expected: FAIL — `planSpecialists` undefined.

- [ ] **Step 3: Write `run_trade_fleet_coordinator_specialists.go`**

```go
package commands

import (
	"context"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

const specialistStatsWindow = 24 * time.Hour

type specialistPorts struct {
	claims    mvt.ClaimRegistry
	telemetry trading.TourTelemetryRepository
	fees      GateFeeReader
}

// SetSpecialistPorts wires the MVT specialist pool (spec §4). Unwired, the pool is inert.
func (h *RunTradeFleetCoordinatorHandler) SetSpecialistPorts(claims mvt.ClaimRegistry, telemetry trading.TourTelemetryRepository, fees GateFeeReader) {
	h.specialists = &specialistPorts{claims: claims, telemetry: telemetry, fees: fees}
}

func shipSystem(s *navigation.Ship) string {
	if s.CurrentLocation() == nil {
		return ""
	}
	return s.CurrentLocation().SystemSymbol
}

// planSpecialists decides tag changes over IDLE hulls only: orphaned specialists self-demote,
// excess specialists demote lowest-margin first, and open seats promote the idle trade-mvt
// hull standing at a fat lane's source (else sink, else lowest symbol).
func planSpecialists(all, idle []*navigation.Ship, fat []mvt.LaneStat, pool int, perHullMargin map[string]float64) (promote, demote []*navigation.Ship) {
	touches := map[string]bool{}
	for _, l := range fat {
		touches[l.Source], touches[l.Sink] = true, true
	}
	current := 0
	for _, s := range all {
		if s.DedicatedFleet() == tradeFleetLane {
			current++
		}
	}
	var idleLane, idleMVT []*navigation.Ship
	for _, s := range idle {
		switch s.DedicatedFleet() {
		case tradeFleetLane:
			idleLane = append(idleLane, s)
		case tradeFleetMVT:
			idleMVT = append(idleMVT, s)
		}
	}
	sort.Slice(idleLane, func(i, j int) bool {
		mi, mj := perHullMargin[idleLane[i].ShipSymbol()], perHullMargin[idleLane[j].ShipSymbol()]
		if mi != mj {
			return mi < mj
		}
		return idleLane[i].ShipSymbol() < idleLane[j].ShipSymbol()
	})
	sort.Slice(idleMVT, func(i, j int) bool { return idleMVT[i].ShipSymbol() < idleMVT[j].ShipSymbol() })

	demoted := map[string]bool{}
	for _, s := range idleLane {
		if !touches[shipSystem(s)] {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	for _, s := range idleLane {
		if current-len(demote) <= pool {
			break
		}
		if !demoted[s.ShipSymbol()] {
			demote = append(demote, s)
			demoted[s.ShipSymbol()] = true
		}
	}
	seats := pool - (current - len(demote))
	if seats <= 0 || len(idleMVT) == 0 {
		return promote, demote
	}
	taken := map[string]bool{}
	pick := func(pred func(*navigation.Ship) bool) *navigation.Ship {
		for _, s := range idleMVT {
			if !taken[s.ShipSymbol()] && pred(s) {
				return s
			}
		}
		return nil
	}
	for _, l := range fat {
		if seats == 0 {
			break
		}
		lane := l
		s := pick(func(s *navigation.Ship) bool { return shipSystem(s) == lane.Source })
		if s == nil {
			s = pick(func(s *navigation.Ship) bool { return shipSystem(s) == lane.Sink })
		}
		if s == nil {
			s = pick(func(*navigation.Ship) bool { return true })
		}
		if s == nil {
			break
		}
		taken[s.ShipSymbol()] = true
		promote = append(promote, s)
		seats--
	}
	return promote, demote
}

// reconcileSpecialists derives the pool on the specialist cadence and applies tag changes
// to idle hulls. Every failure leaves the fleet as it was.
func (h *RunTradeFleetCoordinatorHandler) reconcileSpecialists(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, all, idle []*navigation.Ship, now time.Time, logger common.ContainerLogger) (promoted, demoted int) {
	if h.specialists == nil {
		return 0, 0
	}
	cadenceMin := cmd.SpecialistCadenceMinutes
	if cadenceMin <= 0 {
		cadenceMin = DefaultSpecialistCadenceMinutes
	}
	if !h.specialistsAt.IsZero() && now.Sub(h.specialistsAt) < time.Duration(cadenceMin)*time.Minute {
		return 0, 0
	}
	h.specialistsAt = now
	playerID := int(cmd.PlayerID)
	legs, err := h.specialists.telemetry.ListByPlayer(ctx, playerID, now.Add(-specialistStatsWindow))
	if err != nil {
		logger.Log("WARNING", "Specialist pool: telemetry unreadable; pool unchanged", map[string]interface{}{"error": err.Error()})
		return 0, 0
	}
	stats := mvt.ComputeFleetStats(legs, specialistStatsWindow)
	fees := h.specialists.fees.GateFees(ctx, playerID)
	multiple := cmd.FatLaneMultiplePct
	if multiple <= 0 {
		multiple = DefaultFatLaneMultiplePct
	}
	fraction := cmd.SpecialistFractionPct
	if fraction <= 0 {
		fraction = DefaultSpecialistFractionPct
	}
	var fat []mvt.LaneStat
	for _, l := range stats.Lanes {
		if mvt.IsFatLane(l.MarginPerTranche, l.MeanTransitSeconds, stats.CreditsPerHullSec, fees[l.Source], stats.IntraMarginPerTranche, multiple) {
			fat = append(fat, l)
		}
	}
	n := 0
	for _, s := range all {
		if isTradeFleetTag(s.DedicatedFleet()) {
			n++
		}
	}
	pool := mvt.PoolSize(len(fat), n, fraction)
	promote, demote := planSpecialists(all, idle, fat, pool, stats.PerHullMargin)
	apply := func(s *navigation.Ship, to string) bool {
		from := s.DedicatedFleet()
		s.SetDedicatedFleet(to)
		if err := h.shipRepo.Save(ctx, s); err != nil {
			s.SetDedicatedFleet(from)
			logger.Log("WARNING", "Specialist pool: re-tag failed", map[string]interface{}{"hull": s.ShipSymbol(), "to": to, "error": err.Error()})
			return false
		}
		logger.Log("INFO", "Specialist pool: hull re-tagged", map[string]interface{}{"hull": s.ShipSymbol(), "from_tag": from, "to_tag": to, "pool": pool, "fat_lanes": len(fat)})
		return true
	}
	for _, s := range promote {
		if apply(s, tradeFleetLane) {
			_ = h.specialists.claims.Release(ctx, playerID, s.ShipSymbol())
			promoted++
		}
	}
	for _, s := range demote {
		if apply(s, tradeFleetMVT) {
			demoted++
		}
	}
	return promoted, demoted
}
```

Add to `RunTradeFleetCoordinatorHandler`: `specialists *specialistPorts` and `specialistsAt time.Time`. In `reconcileOnce`, after `idle, running := partitionTradeFleet(ships)`:

```go
	if p, d := h.reconcileSpecialists(ctx, cmd, ships, idle, now, logger); p+d > 0 {
		logger.Log("INFO", "Specialist pool reconciled", map[string]interface{}{"promoted": p, "demoted": d})
	}
```

(`now` and `logger` are the names `reconcileOnce` already uses; match them.)

- [ ] **Step 4: Wire the daemon**

In `cmd/spacetraders-daemon/main.go` next to `tradeFleetCoordinatorHandler.SetTourLauncher(daemonServer)` (line 971):

```go
	tradeFleetCoordinatorHandler.SetSpecialistPorts(
		persistence.NewTradeClaimRegistry(db),
		persistence.NewTourTelemetryRepository(db),
		tradeRouteCmd.NewLedgerGateFeeReader(transactionRepo, nil),
	)
```

Use the variable names main.go already holds for the GORM handle and the transaction repository (`grep -n "NewLedgerGateFeeReader\|NewTourTelemetryRepository" cmd/spacetraders-daemon/*.go`), and the alias main.go uses for the commands package.

- [ ] **Step 5: Build and test**

Run: `go build ./... && go test ./internal/application/trading/commands/ -run 'TestPlanSpecialists' -v && go test ./internal/application/trading/commands/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/application/trading/commands/run_trade_fleet_coordinator_specialists.go internal/application/trading/commands/run_trade_fleet_coordinator_specialists_test.go internal/application/trading/commands/run_trade_fleet_coordinator.go cmd/spacetraders-daemon/main.go
git commit -m "feat(mvt): derived specialist pool — fat-lane qualifier, idle-only promotion/demotion (<bead>)"
```

**Step 3 ships here.** With `trade-lane` hulls launched on the old path (`max_tour_systems` 2), confirm in `SELECT ship_symbol, dedicated_fleet FROM ships WHERE player_id = <pid>` that the pool never exceeds `floor(N × 0.10)` and that promotions match the daemon's `Specialist pool: hull re-tagged` lines.

### Task 14: Live cohort — arm five hulls, measure two complete hours (Wave D; no code)

**Files:** none. Runs against the deployed daemon after Task 11 is merged, deployed, and the Task 12 replay gate printed `GATE: true`.

- [ ] **Step 1: Pick the cohort**

Choose five bulk freighters currently tagged `trade`, spread over at least three systems, none mid-jump:

```sql
SELECT ship_symbol, dedicated_fleet, current_waypoint FROM ships
WHERE player_id = <pid> AND dedicated_fleet = 'trade' AND is_in_transit = false
ORDER BY ship_symbol LIMIT 12;
```

(Use the real column names from `models_navigation.go` if they differ.) Record the five symbols and the wall-clock start in the step-2 bead.

- [ ] **Step 2: Arm the tag**

For each of the five: `spacetraders fleet assign --ship <SYMBOL> --fleet trade-mvt`. The fleet coordinator relaunches the hull's next tour with `mvt_loop=true`; no restart. Confirm within ten minutes:

```sql
SELECT hull, system, claimed_at, arrived_at FROM trade_claims WHERE player_id = <pid> ORDER BY hull;
SELECT hull, from_state, to_state, system, yield_here, best_alternative, travel_cost, reason, at
FROM mvt_transitions WHERE player_id = <pid> ORDER BY at DESC LIMIT 25;
```

Five claim rows and a `bootstrap` transition per hull is success.

- [ ] **Step 3: Wait two complete clock hours, then measure**

Only complete hours; never a partial hour, never the naive series, never treasury delta.

Margin per hull, lot-matched: run `spacetraders ledger positions` for the window (read `CLI-PRIMER.md` for the window flags) and split the per-hull totals into cohort (the five) and control (every other `trade` hull). Divide each by its hull count.

Jumps per hull, from system changes between consecutive realised legs:

```sql
WITH s AS (
  SELECT ship_symbol, realized_at,
         split_part(waypoint, '-', 1) || '-' || split_part(waypoint, '-', 2) AS sys
  FROM tour_leg_telemetry
  WHERE player_id = <pid> AND realized_at >= '<START>' AND realized_at < '<START + 2h>'
), t AS (
  SELECT ship_symbol, sys, lag(sys) OVER (PARTITION BY ship_symbol ORDER BY realized_at) AS prev
  FROM s
)
SELECT ship_symbol, count(*) FILTER (WHERE prev IS NOT NULL AND sys <> prev) AS system_changes
FROM t GROUP BY 1 ORDER BY 1;
```

- [ ] **Step 4: Apply the gate**

Pass iff cohort margin per hull ≥ control margin per hull **and** cohort system changes per hull < control. Either alone fails.

- Pass → record both tables in the bead; proceed to Task 13 (specialists) and, after that ships, roll `trade-mvt` to the remaining hulls five at a time with the same two-hour check each time.
- Fail → `spacetraders fleet assign --ship <SYMBOL> --fleet trade` for the five (rollback is the tag, no restart); record the numbers; open a bead with the `mvt_transitions` rows of the worst hull attached. Do not tune knobs live; tune them in the replay (Task 12 Step 5) and re-run the cohort.

### Task 15: Retire the old reposition path (Wave D; migration step 4 — only after every trade hull carries `trade-mvt` and its two-hour check passed)

**Files:**
- Delete: `internal/application/trading/commands/run_tour_coordinator_reposition.go`, `run_tour_coordinator_rate_floor.go`, `run_tour_coordinator_dispersal.go`, `run_tour_coordinator_relocation_offer.go`, and their `_test.go` files, plus any file whose only callers were these (find with `go build ./...` after deletion and `grep -rn "maybeReposition\|maybeRepositionRateFloor\|maybeDisperseFromCrowdedGround\|maybeOfferForRelocation" internal/`).
- Modify: `run_tour_coordinator.go` — remove the three `if !cmd.MVTLoop`/old-path branches so the MVT branch is unconditional; remove the `resumeInFlightReposition` old-path fields only if nothing else uses them.
- Modify: `run_tour_coordinator_contract.go` — delete `RepositionDisabled, RepositionMinMargin, RepositionMaxCandidates, RepositionJumpBound, RepositionReachEnabled, RepositionReachHopDecayPct, RepositionReachMaxHullsPerSystem, OwnTradePenaltyPct, OwnTradeColdMinutes, OwnTradePenaltyDisabled, RepositionRateFloorEnabled, RepositionRateFloorPct, RepositionRateFloorImprovementPct, RepositionRateFloorDwellMinutes, PlacementDisabled, PlacementBetaWindowMinutes, PlacementParkFloorPct, PlacementShortlistTopN, PlacementHorizonMinutes, RelocationOfferWindowSeconds, MVTLoop` (the loop is now the only path; keep `RepositionInProgress/RepositionTargetSystem/RepositionTargetWaypoint` — TRAVEL resume uses them).
- Modify: `internal/infrastructure/config/trade_fleet.go`, `container_ops_tour.go` (`TourRunOverrides` → drop `RepositionReachEnabled` and `MVTLoop`; config keys `placement_*`, `reposition_*`), `command_factory_builders.go`, `container_ops_tune.go` (any tune entries for the deleted knobs), `run_trade_fleet_coordinator_launch.go` (`TourLaunchSpec.RepositionReachEscalated`), `run_trade_fleet_coordinator.go` (`tradeFleet` and `tradeFleetMVT` both mean the loop; keep both tags accepted so no re-tag is needed).
- Docs: `CLI-PRIMER.md` and `MECHANICS.md` sections that describe the three heuristics and their knobs — replace with the loop and its six knobs.

- [ ] **Step 1: Confirm the precondition**

```sql
SELECT dedicated_fleet, count(*) FROM ships WHERE player_id = <pid> AND dedicated_fleet IN ('trade','trade-mvt','trade-lane') GROUP BY 1;
```

`trade` must be 0. If not, stop.

- [ ] **Step 2: Delete, then make it build**

Delete the files listed above; run `go build ./... 2>&1 | head -40`; remove every dangling reference the compiler names; repeat until clean. Then `go vet ./...`.

- [ ] **Step 3: Prove the knobs are gone**

Run: `grep -rn "reposition_rate_floor\|placement_disabled\|reposition_reach\|own_trade_penalty\|relocation_offer" internal/ cmd/ configs/ --include='*.go' --include='*.yaml' --include='*.json' | grep -v _test`
Expected: no output.

- [ ] **Step 4: Run the full suite**

Run: `go test ./... 2>&1 | tail -5` and `go test -race ./internal/application/trading/... 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A internal/ cmd/ ../CLI-PRIMER.md ../MECHANICS.md
git commit -m "refactor(mvt): retire margin-death reposition, rate floor, reach anti-herd and their knobs — the hull loop is the only path (<bead>)"
```

Deploy with `make restart-daemon` and watch `mvt_transitions` for one complete hour: every trade hull must show at least one row.

---

## Self-review

**Spec coverage** (spec section → task):

| Spec | Task |
|---|---|
| Scope: kept byte-identical | Global Constraints; no task edits the solver, ledger writers, guards, loader |
| Scale invariance | Task 1 (`TestRank_OneHullOneSystem`, `TestRank_NHullsOneSystem`), Task 4 (`TestPoolSize_ScaleAxis`) |
| §1 TRADE, departure rule, cold-start guard, empty-system exit, CLAIM, TRAVEL | Tasks 2, 10, 11 |
| §2 ranker, `expected_yield`, `travel_cost`, in-transit penalty, candidates, bootstrap | Tasks 1, 9, 10 |
| §3 claim registry, restart | Tasks 5, 10 (`mvtRecover`) |
| §4 specialist pool | Tasks 3, 4, 13 |
| §5 interfaces: `SystemDepth`, `Gates.Hops` (via `repositionNeighborsWithinJumps`), `Claims.InTransitTo` (`InTransit`), telemetry line, knobs | Tasks 6, 9, 5, 7, 8 |
| §6 failure modes | Task 1 (unreadable → error → stay), Task 10 (travel failure, hold after 3), Task 11 (hold), Task 13 (telemetry unreadable → pool unchanged) |
| §7 unit tests, replay, ship gate, live cohort | Tasks 1–4 tables, Task 12, Task 14 |
| §8 migration steps 1–4 by tag | Tasks 9 → 10/11(+12) → 13 → 15; tags in Task 8 |

Gaps: none found. Two spec statements are implemented as refinements, listed in "Decisions this plan makes" (#2 tour-end departure, #4 lane rows instead of `(units, credits)`).

**Placeholder scan:** no "TBD"/"TODO"/"implement later". Pointers to existing helpers (`rfSeed`, `repositionFixture`, `tfIdleShip`, the `RecordLeg` literal) name the file and symbol; the plan gives the code that uses them.

**Type consistency:** `mvt.Rank(hull Hull, cands []Candidate, costs Costs) []ScoredSystem` (Task 1) is what Tasks 9 and 12 call; `Decide(t *YieldTracker, bestAltScore float64, hasAlt bool) Decision` (Task 2) is what Tasks 11 and 12 call; `mvtTravelTo(ctx, cmd, response, ranked, reason, yieldHere)` — Task 11 adds the sixth parameter and updates Task 10's callers; `ClaimRegistry` methods (Task 5) match the fake in Task 9; `SystemDepths(ctx, playerID, systems)` (Task 1 interface, Task 6 adapter, Task 9 fake); `TourLaunchSpec.Fleet` and `buildTourLaunchSpec(cmd, symbol, fleet, reachEscalated, reserve)` (Task 8) are the only launch-side changes Task 13 relies on.

