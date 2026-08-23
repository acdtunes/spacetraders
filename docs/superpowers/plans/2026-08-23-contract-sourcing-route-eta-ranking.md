# Contract Sourcing Route-ETA Ranking — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rank contract sourcing candidates on real fuel-aware route ETAs from the existing OR-Tools planner instead of straight-line cruise estimates, and admit unclaimed IN_TRANSIT hulls with arrival-adjusted ETAs.

**Architecture:** The domain ranking (`SelectHullForCargo`) stays pure and gains an optional supplied-ETA map; a new application-layer `RouteETAEstimator` prices all candidates in parallel through the existing `routing.RoutingClient` port (2s budget, fail-open); the coordinator wires the estimator in via the codebase's optional-port convention (nil ⇒ byte-identical current behavior).

**Tech Stack:** Go (daemon), existing Python OR-Tools routing service via gRPC (NO changes to it), existing `internal/domain/routing` port.

**Spec:** `docs/superpowers/specs/2026-08-23-contract-sourcing-route-eta-ranking-design.md` — read it first; it is the authority on behavior.

## Global Constraints

- **Fail OPEN everywhere** (RULINGS #1): this ranking spends no credits; no failure of it may ever block a contract dispatch. Fallback = today's straight-line ranking, all-or-nothing (never mix real ETAs with straight-line numbers in one comparison).
- **Merges via captain-gate ONLY** (RULINGS #13): `gobot/bin/captain-gate --repo /Users/andres/cities/spacetraders --worktree <wt> --branch <br> --message "<msg>" --provision --merge`. Never merge to main by hand. Verify the merged SHA's diffstat afterward (RULINGS #12).
- **Work in an isolated worktree** (repo convention: `/Users/andres/cities/captain-worktrees/<bead-id>`, branch named `<bead-id>`). Commit with `--no-verify`; never stage `.beads/issues.jsonl`.
- **Protected paths — never touch:** `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.
- **Comment discipline** (ENGINEERING.md §6, enforced by `make comment-audit-check` inside the gate): short, present-tense WHY comments; no bead-ids, no incident narration, no measured numbers in source comments.
- **File a bd bead first** (`bd create` from the repo root, `sp-` prefix), claim it, close it with the merged SHA. All work is beads (RULINGS #11).
- **Base:** rebase onto latest `main` AFTER sp-66zwd has merged (the displaced-hull tiebreak). Its landed signatures are the base for Task 1 — read them before editing; do not assume the exact parameter shapes written here survived its merge verbatim.
- Full verification before gating: `go build ./...`, `go vet ./...`, `go test ./... -race`.
- Do NOT restart the prod daemon; landing on `main` is this plan's full scope. Live-arming is a separate operator decision (RULINGS #19 — record it).

## File Structure

| File | Role |
|---|---|
| `gobot/internal/domain/contract/hull_for_cargo.go` | Modify: `hullFit.travelTime` becomes suppliable via an ETA map; straight-line stays as the nil-map path |
| `gobot/internal/domain/contract/ship_selector.go` | Modify: in-transit skip becomes claim-aware; threads the ETA map |
| `gobot/internal/domain/contract/hull_for_cargo_test.go` | Extend: ETA-ranking tests (mirror its existing fixture helpers) |
| `gobot/internal/application/contract/route_eta.go` | Create: `RouteETAEstimator` — parallel PlanRoute calls, budget, drop/fallback classification |
| `gobot/internal/application/contract/route_eta_test.go` | Create: estimator tests with a fake `RoutingClient` |
| `gobot/internal/application/contract/ship_selector.go` | Modify: `SelectClosestShip` runs the estimator, passes ETAs down, logs `ranking_mode` + per-candidate ETAs |
| `gobot/internal/application/contract/ship_pool_manager.go` | Modify: `FindIdleShipsByFleet` admits unclaimed IN_TRANSIT hulls |
| `gobot/internal/application/contract/commands/run_fleet_coordinator.go` | Modify: handler field + `NewRunFleetCoordinatorHandler` wiring; estimator handed to `SelectClosestShip` |
| `gobot/internal/adapters/grpc/daemon_server.go` (construction site of the contract handler) | Modify: construct the estimator from the daemon's existing `routingClient` and inject it |

---

### Task 0: Bead + worktree

**Files:** none (process).

- [ ] **Step 1:** From `/Users/andres/cities/spacetraders`: `bd create "Contract sourcing: rank candidates on route ETAs (spec 2026-08-23)" -l "feature,contract" --description "Implements docs/superpowers/specs/2026-08-23-contract-sourcing-route-eta-ranking-design.md. Rank sourcing candidates on PlanRoute total_time_seconds instead of straight-line cruise estimates; admit unclaimed IN_TRANSIT hulls with arrival-adjusted ETAs; fail open to straight-line ranking (all-or-nothing) on any planner failure or 2s budget overrun."` — note the bead id; `bd update <id> --claim`.
- [ ] **Step 2:** `git -C /Users/andres/cities/spacetraders worktree add /Users/andres/cities/captain-worktrees/<bead-id> -b <bead-id> main`. Confirm sp-66zwd's merge is in the base: `git log --oneline -5` must show the sp-66zwd merge; if not, STOP and wait for it (this plan builds on its signatures).
- [ ] **Step 3:** Read the landed post-sp-66zwd signatures of `SelectHullForCargo`, `SelectOptimalShip` (domain) and `SelectClosestShip` (application). Wherever this plan's code blocks show those signatures, reconcile mechanically against what actually landed (extra `deliveryFleet`/`standbySlots`-style parameters stay; this plan only ADDS the ETA map alongside them).

---

### Task 1: Domain — ETA-supplied ranking

**Files:**
- Modify: `gobot/internal/domain/contract/hull_for_cargo.go` (pre-66zwd anchors: `SelectHullForCargo` ~:89, `newHullFit` ~:138, `byNearestThenSmallest` ~:168 — lines will have shifted)
- Modify: `gobot/internal/domain/contract/ship_selector.go` (`SelectOptimalShip` ~:41, `shouldSkipShipInTransit` ~:89)
- Test: `gobot/internal/domain/contract/hull_for_cargo_test.go`

**Interfaces:**
- Consumes: sp-66zwd's landed `SelectHullForCargo(candidates, target, cargoUnits, <66zwd params>...)`.
- Produces: `SelectHullForCargo(candidates []*navigation.Ship, target *shared.Waypoint, cargoUnits int, etas map[string]float64, <66zwd params>...)` — `etas` is TOTAL seconds to the source per ship symbol; `nil` ⇒ straight-line ranking (today's behavior, byte-identical); non-nil is only ever passed when EVERY candidate has an entry (app layer guarantees; see Task 2). Same added param on `SelectOptimalShip`. `shouldSkipShipInTransit` semantics: in-transit hulls are no longer skipped when idle/unclaimed.

- [ ] **Step 1: Write the failing tests** (mirror the existing fixture/builder helpers already used in `hull_for_cargo_test.go` — do not invent a new Ship construction path; check `git status` before/after editing so you extend, never overwrite):

```go
// Core behavior: with supplied ETAs, a hull whose TOTAL ETA is smaller wins even
// if its straight-line distance is larger (the straight-line order would invert this).
func TestSelectHullForCargo_SuppliedETAOutranksStraightLine(t *testing.T) {
	// far by distance, near by ETA (clean single hop)
	fastArrival := newTestHull("TORWIND-B", /*at*/ farWaypoint, /*hold*/ 80)
	// near by distance, slow by ETA (multi-hop + refuels)
	slowNear := newTestHull("TORWIND-C", /*at*/ nearWaypoint, /*hold*/ 80)
	etas := map[string]float64{"TORWIND-B": 120, "TORWIND-C": 600}

	res, err := SelectHullForCargo([]*navigation.Ship{slowNear, fastArrival}, target, 40, etas /*, 66zwd params zero-values*/)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// Nil map preserves today's ranking exactly (fallback path).
func TestSelectHullForCargo_NilETAMapKeepsStraightLineRanking(t *testing.T) {
	near := newTestHull("TORWIND-B", nearWaypoint, 80)
	far := newTestHull("TORWIND-C", farWaypoint, 80)

	res, err := SelectHullForCargo([]*navigation.Ship{far, near}, target, 40, nil /*, 66zwd params*/)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// Tie ladder preserved under supplied ETAs: equal ETA -> smaller hold; then the
// sp-66zwd displaced-hull tiebreak (reuse its landed test fixtures for the slot context).
func TestSelectHullForCargo_EqualETATieFallsToCapacityThenDisplacement(t *testing.T) {
	small := newTestHull("TORWIND-B", nearWaypoint, 40)
	big := newTestHull("TORWIND-C", nearWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 300, "TORWIND-C": 300}

	res, err := SelectHullForCargo([]*navigation.Ship{big, small}, target, 30, etas /*, 66zwd params*/)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// RULINGS #1 invariant stated as a test: candidates in, a hull always comes out.
func TestSelectHullForCargo_SuppliedETAsNeverProduceNoSelection(t *testing.T) {
	only := newTestHull("TORWIND-B", farWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 999999}

	res, err := SelectHullForCargo([]*navigation.Ship{only}, target, 40, etas /*, 66zwd params*/)

	require.NoError(t, err)
	require.NotNil(t, res)
}

// In-transit unclaimed hulls are RANKED, not skipped, when ETAs are supplied.
func TestSelectOptimalShip_UnclaimedInTransitHullIsEligibleWithETA(t *testing.T) {
	inTransit := newTestHullInTransit("TORWIND-B", /*destination*/ nearWaypoint, 80) // unclaimed
	idleFar := newTestHull("TORWIND-C", farWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 90, "TORWIND-C": 400}

	res, err := NewShipSelector().SelectOptimalShip([]*navigation.Ship{inTransit, idleFar}, target, "", 40, etas /*, 66zwd params*/)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}
```

- [ ] **Step 2: Run to verify they fail** — `cd gobot && go test ./internal/domain/contract/ -run 'ETA|InTransitHull' -v`. Expected: compile failure (signature has no `etas` param) — that IS the valid RED for a signature change; record it.

- [ ] **Step 3: Implement.** In `hull_for_cargo.go`:

```go
// newHullFit computes the ranking figures for one candidate hull. A supplied
// ETA (seconds to the source, refuel- and hop-aware, arrival-adjusted for an
// in-transit hull) replaces the straight-line cruise estimate; absent one, the
// straight-line figure keeps the ranking priced the old way.
func newHullFit(ship *navigation.Ship, target *shared.Waypoint, units int, etas map[string]float64) hullFit {
	capacity := ship.CargoCapacity()
	if capacity < 1 {
		capacity = 1
	}
	distance := ship.CurrentLocation().DistanceTo(target)
	travelTime := float64(shared.FlightModeCruise.TravelTime(distance, ship.EngineSpeed()))
	if etas != nil {
		if eta, ok := etas[ship.ShipSymbol()]; ok {
			travelTime = eta
		}
	}
	return hullFit{
		ship:       ship,
		distance:   distance,
		travelTime: travelTime,
		capacity:   capacity,
		trips:      int(math.Ceil(float64(units) / float64(capacity))),
	}
}
```

Thread `etas` through `SelectHullForCargo` (all four tiers build fits through `newHullFit`) and through `SelectOptimalShip`. In `ship_selector.go` replace the in-transit skip:

```go
// shouldSkipShipInTransit drops a mid-flight hull only when another controller
// owns it: an unclaimed in-transit hull is a legitimate candidate whose ETA
// already counts its remaining flight, and interrupting an ownerless
// repositioning for paying work is the point of ranking it.
func (s *ShipSelector) shouldSkipShipInTransit(ship *navigation.Ship, shipWithCargo *navigation.Ship) bool {
	if ship.NavStatus() != navigation.NavStatusInTransit || shipWithCargo == ship {
		return false
	}
	return !ship.IsIdle()
}
```

(Read `IsIdle()` in `internal/domain/navigation/ship_assignment_ops.go:22` first; if it structurally excludes in-transit hulls, use `!ship.IsAssigned()` here instead and say so in the bead notes.)

- [ ] **Step 4: Run to verify green** — `go test ./internal/domain/contract/ -v`. Expected: PASS including all pre-existing (straight-line and sp-66zwd) tests.
- [ ] **Step 5: Commit** — `git add internal/domain/contract/ && git commit --no-verify -m "feat(contract): rank sourcing candidates on supplied route ETAs; admit unclaimed in-transit hulls"`.

---

### Task 2: Application — RouteETAEstimator

**Files:**
- Create: `gobot/internal/application/contract/route_eta.go`
- Test: `gobot/internal/application/contract/route_eta_test.go`

**Interfaces:**
- Consumes: `routing.RoutingClient` (`internal/domain/routing/ports.go:11`: `PlanRoute(ctx, *RouteRequest) (*RouteResponse, error)`; `RouteRequest{SystemSymbol, StartWaypoint, GoalWaypoint, CurrentFuel, FuelCapacity, EngineSpeed, Waypoints []*system.WaypointData, PreferCruise}`; `RouteResponse.TotalTimeSeconds int`). `system.WaypointData` lives in `internal/domain/system/ports.go`. `Ship.ArrivalTime() *time.Time` (`ship_state_sync.go:21`), `Ship.IsInTransit()` (`ship_state.go:142`). NOTE: the estimator calls the `RoutingClient` PORT directly, not `application/ship.RoutePlanner` — the planner's `*navigation.Route` conversion drops `TotalTimeSeconds`, which is the one number this needs. Same port, same adapter, no new client (spec's no-new-surface intent holds).
- Produces:

```go
type ETAResult struct {
	ETAs    map[string]float64 // symbol → total seconds (remaining transit + planned route)
	Dropped []string           // genuinely unroutable candidates — excluded from selection
	OK      bool               // false ⇒ caller must fall back to straight-line for ALL candidates
}

func NewRouteETAEstimator(client routing.RoutingClient, clock shared.Clock) *RouteETAEstimator // budget fixed at 2s

func (e *RouteETAEstimator) EstimateAll(ctx context.Context, ships []*navigation.Ship, systemSymbol, goalWaypoint string, waypoints map[string]*shared.Waypoint) ETAResult
```

- [ ] **Step 1: Write the failing tests.** Fake client records calls and concurrency:

```go
type fakeRoutingClient struct {
	mu          sync.Mutex
	inFlight    int32 // atomic peak-concurrency tracker
	maxInFlight int32
	perShip     map[string]fakeAnswer // keyed by StartWaypoint
	delay       time.Duration
}

type fakeAnswer struct {
	seconds int
	err     error
}

func (f *fakeRoutingClient) PlanRoute(ctx context.Context, req *routing.RouteRequest) (*routing.RouteResponse, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		prev := atomic.LoadInt32(&f.maxInFlight)
		if cur <= prev || atomic.CompareAndSwapInt32(&f.maxInFlight, prev, cur) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a := f.perShip[req.StartWaypoint]
	if a.err != nil {
		return nil, a.err
	}
	return &routing.RouteResponse{TotalTimeSeconds: a.seconds}, nil
}
```

Tests (all against `EstimateAll`):
1. `HappyPath_AllPriced_OKTrue` — two idle hulls, distinct answers; asserts `OK`, both ETAs present, values equal the fake's seconds.
2. `InTransitHull_AddsRemainingTransit` — hull with `IsInTransit()` true and `ArrivalTime()` 90s in the future (fixture clock), fake route 60s ⇒ ETA 150s. In-transit hull with `ArrivalTime() == nil` ⇒ lands in `Dropped` (unpriceable, conservative).
3. `OneUnroutable_DroppedOthersKept` — one hull's answer is a plain error ⇒ that symbol in `Dropped`, `OK` still true, others priced.
4. `AllUnroutable_OKFalse` — every candidate errors ⇒ `OK == false`.
5. `BudgetOverrun_OKFalse` — `delay` beyond the 2s budget (inject a short test budget via an internal field or test constructor) ⇒ `OK == false`, and `EstimateAll` returns within the budget wall-clock (assert elapsed < budget + slack).
6. `TransportClassError_OKFalse` — answer error wraps `context.DeadlineExceeded` ⇒ global `OK == false`, not a per-candidate drop.
7. `CallsRunInParallel` — 4 hulls, per-call delay 50ms ⇒ assert `maxInFlight >= 2` and total elapsed well under 4×50ms.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/application/contract/ -run RouteETA -v`. Expected: compile failure (`RouteETAEstimator` undefined).
- [ ] **Step 3: Implement** `route_eta.go`:

```go
// RouteETAEstimator prices sourcing candidates on the fuel-aware planner the
// daemon already flies routes with, so selection ranks on the same arrival
// times execution will realize. Fail-open by contract: every failure path
// resolves to OK=false (caller keeps the straight-line ranking) or a dropped
// candidate — never a blocked dispatch.
type RouteETAEstimator struct {
	client routing.RoutingClient
	clock  shared.Clock
	budget time.Duration
}

const routeETABudget = 2 * time.Second

func NewRouteETAEstimator(client routing.RoutingClient, clock shared.Clock) *RouteETAEstimator {
	return &RouteETAEstimator{client: client, clock: clock, budget: routeETABudget}
}

func (e *RouteETAEstimator) EstimateAll(ctx context.Context, ships []*navigation.Ship, systemSymbol, goalWaypoint string, waypoints map[string]*shared.Waypoint) ETAResult {
	if e == nil || e.client == nil || len(ships) == 0 {
		return ETAResult{OK: false}
	}
	ctx, cancel := context.WithTimeout(ctx, e.budget)
	defer cancel()

	waypointData := make([]*system.WaypointData, 0, len(waypoints))
	for _, wp := range waypoints {
		waypointData = append(waypointData, &system.WaypointData{Symbol: wp.Symbol, X: wp.X, Y: wp.Y, HasFuel: wp.HasFuel})
	}

	type answer struct {
		symbol  string
		eta     float64
		drop    bool
		global  bool
	}
	answers := make(chan answer, len(ships))
	now := e.clock.Now()

	for _, ship := range ships {
		go func(ship *navigation.Ship) {
			transitRemainder := 0.0
			if ship.IsInTransit() {
				at := ship.ArrivalTime()
				if at == nil {
					answers <- answer{symbol: ship.ShipSymbol(), drop: true}
					return
				}
				if rem := at.Sub(now).Seconds(); rem > 0 {
					transitRemainder = rem
				}
			}
			// Fuel is deducted at departure in this game, so an in-transit
			// hull's current fuel already IS its arrival fuel — no adjustment.
			resp, err := e.client.PlanRoute(ctx, &routing.RouteRequest{
				SystemSymbol:  systemSymbol,
				StartWaypoint: ship.CurrentLocation().Symbol, // invariant: destination while in transit
				GoalWaypoint:  goalWaypoint,
				CurrentFuel:   ship.Fuel().Current,
				FuelCapacity:  ship.FuelCapacity(),
				EngineSpeed:   ship.EngineSpeed(),
				Waypoints:     waypointData,
				PreferCruise:  false,
			})
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					answers <- answer{symbol: ship.ShipSymbol(), global: true}
					return
				}
				answers <- answer{symbol: ship.ShipSymbol(), drop: true} // unroutable: this hull only
				return
			}
			answers <- answer{symbol: ship.ShipSymbol(), eta: transitRemainder + float64(resp.TotalTimeSeconds)}
		}(ship)
	}

	result := ETAResult{ETAs: make(map[string]float64, len(ships)), OK: true}
	for range ships {
		a := <-answers
		switch {
		case a.global:
			result.OK = false
		case a.drop:
			result.Dropped = append(result.Dropped, a.symbol)
		default:
			result.ETAs[a.symbol] = a.eta
		}
	}
	if len(result.ETAs) == 0 {
		result.OK = false
	}
	return result
}
```

- [ ] **Step 4: Run to verify green** — `go test ./internal/application/contract/ -run RouteETA -race -v`. Expected: PASS.
- [ ] **Step 5: Commit** — `git add internal/application/contract/route_eta*.go && git commit --no-verify -m "feat(contract): parallel fail-open route-ETA estimator over the existing routing port"`.

---

### Task 3: Selection integration — pool, plumbing, logging

**Files:**
- Modify: `gobot/internal/application/contract/ship_pool_manager.go` (`FindIdleShipsByFleet`, in-transit skip ~:274)
- Modify: `gobot/internal/application/contract/ship_selector.go` (`SelectClosestShip` ~:24; log emission ~:89; `summarizeCandidates` ~:105)
- Test: extend the existing tests beside each file (locate with `ls internal/application/contract/*_test.go`; extend, never overwrite — check `git status` after edits)

**Interfaces:**
- Consumes: Task 1's `etas` parameter; Task 2's `EstimateAll`/`ETAResult`.
- Produces: `SelectClosestShip(ctx, shipSymbols, shipRepo, graphProvider, converter, targetWaypointSymbol, requiredCargoSymbol, unitsNeeded, playerID, estimator *RouteETAEstimator, <66zwd params>...)` — `estimator` nil ⇒ straight-line path, byte-identical logs plus `ranking_mode: "fallback_straight_line"`.

- [ ] **Step 1: Failing tests.**
  - Pool: in `FindIdleShipsByFleet`, a fleet-tagged hull with `NavStatus() == NavStatusInTransit` and `IsIdle()` true IS returned; an in-transit ASSIGNED hull is NOT. (Mirror the existing pool-test fixtures.)
  - Selector: with a fake estimator returning `OK: true` and ETAs inverting the straight-line order, the ETA winner is selected and the completion log carries `ranking_mode: "route_eta"`; with the estimator erroring globally (`OK: false`), the straight-line winner is selected and `ranking_mode: "fallback_straight_line"` is logged with a WARN naming the cause; a candidate in `Dropped` never wins even when nearest by straight-line.
- [ ] **Step 2: Verify RED** — `go test ./internal/application/contract/ -run 'FindIdle|SelectClosest' -v`; compile failure on the new parameter is the expected RED.
- [ ] **Step 3: Implement.**
  - `ship_pool_manager.go`: replace the unconditional in-transit `continue` with claim-aware admission:

```go
		// A mid-flight hull stays dispatchable while nothing owns it: its
		// remaining transit is priced into the route-ETA ranking, and the only
		// movement it can be interrupting is an ownerless repositioning.
		if ship.NavStatus() == navigation.NavStatusInTransit && !ship.IsIdle() {
			continue
		}
		if ship.IsIdle() {
			idleShips = append(idleShips, ship)
			idleSymbols = append(idleSymbols, ship.ShipSymbol())
		}
```

  (Same `IsIdle` caveat as Task 1 Step 3 — verify its definition once, apply the same predicate in both places.)
  - `ship_selector.go`: after the existing graph fetch (`graphProvider.GetGraph`, ~:70), run the estimator, honor drops, choose mode:

```go
	etaResult := ETAResult{OK: false}
	if estimator != nil {
		etaResult = estimator.EstimateAll(ctx, ships, systemSymbol, targetWaypointSymbol, graphResult.Graph.Waypoints)
	}
	rankingMode := "fallback_straight_line"
	var etas map[string]float64
	if etaResult.OK {
		rankingMode = "route_eta"
		etas = etaResult.ETAs
		if len(etaResult.Dropped) > 0 {
			dropped := make(map[string]bool, len(etaResult.Dropped))
			for _, s := range etaResult.Dropped {
				dropped[s] = true
			}
			kept := ships[:0]
			for _, ship := range ships {
				if !dropped[ship.ShipSymbol()] {
					kept = append(kept, ship)
				}
			}
			if len(kept) > 0 {
				ships = kept
			} else {
				// Every candidate unroutable — price them all the old way
				// rather than refuse the contract.
				rankingMode = "fallback_straight_line"
				etas = nil
			}
		}
	} else if estimator != nil {
		logger.Log("WARNING", "Route-ETA ranking unavailable - falling back to straight-line selection", map[string]interface{}{
			"action": "route_eta_fallback",
		})
	}
```

  Pass `etas` into `SelectOptimalShip`; add `"ranking_mode": rankingMode` to the `Ship selection completed` fields; extend `summarizeCandidates` to render `SYM@<distance>/<etaSeconds>s` when an ETA exists for the symbol.
- [ ] **Step 4: Verify GREEN** — `go test ./internal/application/contract/... -race`. Expected: PASS including all pre-existing selection/pool tests.
- [ ] **Step 5: Commit** — `git add internal/application/contract/ && git commit --no-verify -m "feat(contract): ETA-mode selection with fail-open fallback; admit unclaimed in-transit hulls to the pool"`.

---

### Task 4: Coordinator + daemon wiring, gate, close-out

**Files:**
- Modify: `gobot/internal/application/contract/commands/run_fleet_coordinator.go` (handler struct ~:74, `NewRunFleetCoordinatorHandler` ~:149, `SelectClosestShip` call ~:673)
- Modify: the handler's construction site in `internal/adapters/grpc/` (find it: `grep -rn "NewRunFleetCoordinatorHandler" internal/adapters/grpc/` — the daemon already holds `routingClient routing.RoutingClient`, `daemon_server.go:53`)
- Test: the coordinator's existing test file beside `run_fleet_coordinator.go` (nil-estimator path only — full estimator behavior is Task 2/3's coverage)

**Interfaces:**
- Consumes: Task 3's `SelectClosestShip` signature; `NewRouteETAEstimator` from Task 2.
- Produces: `RunFleetCoordinatorHandler.routeETAEstimator *appContract.RouteETAEstimator` field, wired via a `SetRouteETAEstimator(e *appContract.RouteETAEstimator)` optional setter (mirroring `SetDedicatedFleetSeedMarker`'s optional-port convention: nil ⇒ fallback mode, never an error).

- [ ] **Step 1: Failing test** — coordinator-level: with no estimator set, the selection path still completes and logs `ranking_mode: "fallback_straight_line"` (asserts the optional-port default is fail-open, not a nil-pointer panic).
- [ ] **Step 2: Verify RED** — compile failure on the call-site parameter.
- [ ] **Step 3: Implement.** Add the field + setter; pass `h.routeETAEstimator` at the `:673` call site. At the daemon construction site: `handler.SetRouteETAEstimator(appContract.NewRouteETAEstimator(s.routingClient, s.clock))` (use the daemon's existing clock; if none is threaded there, `shared.NewSystemClock()` matches the codebase default — check how sibling setters obtain it).
- [ ] **Step 4: Full verification** — from the worktree's `gobot/`: `go build ./... && go vet ./... && go test ./... -race && make comment-audit-check`. Expected: all clean.
- [ ] **Step 5: Commit** — `git add -A ':!*.jsonl' && git commit --no-verify -m "feat(contract): wire route-ETA estimator into the fleet coordinator (optional port, nil = fallback)"`.
- [ ] **Step 6: Gate** — `gobot/bin/captain-gate --repo /Users/andres/cities/spacetraders --worktree /Users/andres/cities/captain-worktrees/<bead-id> --branch <bead-id> --message "feat(contract): rank sourcing candidates on route ETAs (spec 2026-08-23, <bead-id>)" --provision --merge`. Expected: `GatePassed:true, Merged:true, EmptyMerge:false`.
- [ ] **Step 7: Verify the merge** (RULINGS #12) — `git -C /Users/andres/cities/spacetraders show --stat HEAD` lists exactly the files above; record the SHA.
- [ ] **Step 8: Push + close** — `git push`, `bd dolt push`, `bd close <bead-id> --reason "merged <SHA>: route-ETA sourcing ranking, fail-open, in-transit admission"`.
- [ ] **Step 9: Live acceptance note** (do NOT restart the daemon yourself): append to the bead — after the next operator restart, verify at the effect point: a `Ship selection completed` line with `ranking_mode: "route_eta"` and non-zero ETAs, and at least one dispatch ranking an IN_TRANSIT hull (PLAYBOOK §10: merged is not proven).

---

## Self-review record

- Spec coverage: ETA definition table → Task 2 (`transitRemainder` + route seconds, nil-arrival drop); eligibility change → Tasks 1+3 (both call sites); ranking/tiebreak preservation → Task 1 tests; fail-open table → Task 2 classification + Task 3 fallback/drop handling; all-or-nothing fallback → Task 3 (`etas = nil` on empty-kept, global `OK:false` path); observability → Task 3 (`ranking_mode`, per-candidate ETAs, WARN); non-goals (no routing-service/proto changes) → no task touches them; supersession → Task 0 Step 2 base check.
- Deliberate deviation from spec letter, kept from the design conversation: the estimator uses the `RoutingClient` port directly instead of `application/ship.RoutePlanner`, because the planner's `Route` conversion drops `TotalTimeSeconds` (verified `internal/domain/navigation/route.go:70` has no time field) — same adapter, no new surface, spec intent intact. The spec's conservative-fuel note resolves to "no adjustment" because fuel is deducted at departure (in-transit current fuel IS arrival fuel); comment in Task 2 records this.
- Signatures cross-checked against tonight's source reads: `RouteRequest`/`RouteResponse` (`ports.go:28/40`), `ArrivalTime()` (`ship_state_sync.go:21`), `IsInTransit()` (`ship_state.go:142`), `IsIdle()/IsAssigned()` (`ship_assignment_ops.go:22/26`), pool skip (`ship_pool_manager.go:~274`), call site (`run_fleet_coordinator.go:673`), daemon routing client (`daemon_server.go:53`). The one unpinned semantic — whether `IsIdle()` internally excludes in-transit — is explicitly flagged at both use sites with the fallback predicate.
