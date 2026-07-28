# HEAVY_TRADE Implementation Plan (sp-fwk8z)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Buy heavy haulers and put them on trade routes when a heavy-selling shipyard is discovered, capped by a live-tunable dial, without stalling frontier expansion — per spec `docs/superpowers/specs/2026-07-27-heavy-trade-design.md`.

**Architecture:** A derived capability gate (no phase change, no persisted state). The fleet autosizer gains a heavy-purchase loop. Treasury is partitioned by one shared predicate — `HeavyReserve` — that both the autosizer and the sensing buy-floor consume, held in lockstep by a compile-time guard.

**Tech Stack:** Go 1.24, module `github.com/andrescamacho/spacetraders-go`, root `gobot/`. GORM (postgres live / in-memory sqlite in tests via `database.NewTestConnection()`), mediator command pattern, Prometheus metrics.

## Global Constraints

- **RULINGS #4:** every money guard fails closed. `common.ImmutableReserveFloor` (50000) is never weakened, never made configurable.
- **RULINGS #5:** every knob has a const default in the owning file; the resolver falls back on `<= 0`.
- **RULINGS #2:** no load-bearing state outside the DB. This design stores nothing new — the reserve is derived.
- **No feature flags, no default-off seams, no arming knobs** (standing Admiral order). `heavy_cap` is an operator dial with default 5, not an on/off switch.
- **The lockstep invariant:** `HeavyReserve` exists in exactly one package. Both callers use that function. A compile-time guard fails the build if a second definition or a divergent constant appears. This is the single most important invariant in the build.
- **Reserve one heavy at a time**, never `cap − owned`.
- **`heaviesOwned` counts every owned heavy**, not only trade-tagged ones (under-counting authorises re-buying a hull we own).
- **A bought heavy MUST be tagged `trade` explicitly** — verified in sp-hv4f6: nothing auto-adopts an untagged hull into trade, and the sole adopter of an undedicated hull routes it to contract work instead.
- Protected paths, never modified: `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/**`.
- Gates from `gobot/`: `gofmt -l internal cmd | (grep . && exit 1 || true)`, `go vet ./...` (type-checks test files; `go build` does not), `go test ./... -race -count=1`. Never `make test` — it swallows exit codes.
- Commits: `rtk git add <paths> && rtk git commit --no-verify -m "..."`, referencing sp-fwk8z.
- Adversarial fakes: a fallible fake returns a WRONG value alongside its error, so a swallowed error fails the test.
- **Branch dependency:** this branches off `sp-k6v8z` and rebases onto its final state before gating.

---

### Task 1: Recon, the shared reserve predicate, and heavy inventory queries

This task gates the rest of the plan. Its three recon answers may change Tasks 2–4; report them before building past the predicate.

**Files:**
- Create: `gobot/internal/application/common/heavy_reserve.go` (+ test)
- Create or extend: heavy-yard query on the shipyard-inventory repository (+ test)
- Create or extend: owned-heavy census query on the ship repository (+ test)

**Recon deliverables (report before writing Task 2's code):**
1. The exact heavy ship-type constant in use. SpaceTraders names it `SHIP_HEAVY_FREIGHTER`; a `heavy_ship_types` config key has been seen referenced — determine which is authoritative and whether "heavy" is a type list, a frame, or a cargo-capacity threshold.
2. Whether the fleet autosizer reads live config (`liveconfig.Reader`) or is launch-frozen. Live-tunability of `heavy_cap` is a hard requirement, so wiring it is in scope for Task 2 if frozen.
3. Whether tour construction or the routing/VRP request carries a cargo-capacity assumption tuned to light hulls (spec risk 4). Report what you find; fix nothing here.

**Interfaces produced (consumed verbatim by Tasks 2–3):**

```go
package common

// HeavyReserveInputs are the three derived facts the predicate needs. All come
// from durable tables; nothing here is stored or cached.
type HeavyReserveInputs struct {
    CapabilityOpen        bool  // any known yard sells the heavy hull type
    HeaviesOwned          int   // every owned heavy, regardless of fleet tag
    HeavyCap              int   // operator dial
    CheapestKnownPrice    int64 // min positive purchase_price across known heavy yards
}

// HeavyReserve is the ONE definition of how much treasury is held back for the
// next heavy purchase. Both the fleet autosizer and the sensing buy-floor call
// THIS function; a second copy is how a reservation silently drifts.
func HeavyReserve(in HeavyReserveInputs) int64
```

Rules the tests pin: closed capability → 0; owned ≥ cap → 0; cap ≤ 0 → 0; non-positive price → 0 (an unpriced yard reserves nothing); otherwise exactly `CheapestKnownPrice` — one heavy, never `cap − owned` multiples.

- [ ] **Step 1: Recon.** Answer the three deliverables with file:line evidence. If (3) shows a capacity assumption that a heavy would break, STOP and report — the plan changes.

- [ ] **Step 2: Write the failing predicate test.**

```go
func TestHeavyReserve(t *testing.T) {
	base := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, CheapestKnownPrice: 1_200_000}
	cases := []struct {
		name string
		in   HeavyReserveInputs
		want int64
	}{
		{"open with room reserves exactly one heavy", base, 1_200_000},
		{"closed capability reserves nothing", func() HeavyReserveInputs { c := base; c.CapabilityOpen = false; return c }(), 0},
		{"at cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 5; return c }(), 0},
		{"over cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 9; return c }(), 0},
		{"zero cap is a legitimate hold", func() HeavyReserveInputs { c := base; c.HeavyCap = 0; return c }(), 0},
		{"unpriced yard reserves nothing", func() HeavyReserveInputs { c := base; c.CheapestKnownPrice = 0; return c }(), 0},
		{"negative price reserves nothing", func() HeavyReserveInputs { c := base; c.CheapestKnownPrice = -5; return c }(), 0},
		{"room for four still reserves only one", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 1; return c }(), 1_200_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeavyReserve(tc.in); got != tc.want {
				t.Fatalf("HeavyReserve = %d, want %d", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify failure** — `cd gobot && go test ./internal/application/common/ -run TestHeavyReserve -v` → FAIL (undefined).

- [ ] **Step 4: Implement `HeavyReserve`** exactly per the rules, with a doc comment stating the lockstep requirement and naming both callers.

- [ ] **Step 5: Add the lockstep guard.** Follow the existing compile-time guard that ties the two 50k floors together (find it in `internal/application/common/` — it fails the build LOUD if the constants drift). Mirror that mechanism so a second `HeavyReserve` definition or a divergent heavy-type constant breaks the build rather than the economics.

- [ ] **Step 6: Heavy inventory + census queries.** Cheapest-known-heavy-yard over `shipyard_inventory` (positive prices only; return yard waypoint + price + a not-found signal) and an owned-heavy count over the ships table. Both DB-only, both era-scoped the way their sibling methods are — follow each repository's existing conventions rather than inventing. sqlite tests for both, including: no heavy rows → capability closed; unpriced rows excluded; cheapest wins across multiple yards.

- [ ] **Step 7: Gates + commit** — `rtk git add gobot/internal && rtk git commit --no-verify -m "feat(common): derived heavy-purchase reservation + heavy yard queries (sp-fwk8z T1)"`

---

### Task 2: The autosizer heavy purchase loop — **SUPERSEDED BY RECON**

> Task 1's recon found the purchase loop already exists. **Do not build this section.** See
> "Task 2 (REVISED)" at the end of this plan, and spec §RECON CORRECTIONS C1–C3.

**Files:**
- Modify: the fleet autosizer command/handler (its package is a Task 1 recon output)
- Modify: the autosizer's tune registry entry + defaults, and its liveconfig wiring if Task 1 found it launch-frozen
- Test: alongside the autosizer's existing tests

**Consumes:** `common.HeavyReserve`, Task 1's two queries, the existing purchase command (which navigates and docks the purchasing hull itself), `AssignFleet`.

**Knob:** `heavy_cap`, default **5**, bounds `[0, 50]`, live-tunable, description stating that 0 is a legitimate hold rather than a disable seam.

- [ ] **Step 1: Failing test — the gate.** Capability closed (no heavy rows in inventory) → zero purchase attempts, zero quotes. Adversarial: the inventory fake errors AND returns a populated yard list; assert no purchase.
- [ ] **Step 2: Failing test — the cap.** `heaviesOwned == heavy_cap` → no attempt. `heavy_cap = 0` → no attempt, and the reserve the autosizer reports is 0.
- [ ] **Step 3: Failing test — the floor.** Treasury one credit below `actualPrice + heavyBuyFloor` → no buy; one credit above → exactly one buy. Adversarial: treasury read errors AND returns a huge balance → no buy.
- [ ] **Step 4: Failing test — quote/actual binding.** Live quote below the eventual actual price → the buy is recorded at the ACTUAL price and the loop halts for the tick rather than proceeding on the stale quote (mirror the sensing buy queue's proven pattern — read it first).
- [ ] **Step 5: Failing test — the tag.** A successful purchase assigns the hull to the `trade` fleet. Mutation-probe this one specifically: delete the tag call, confirm the test fails by name. An untagged heavy goes to contract work instead — this assertion is the guard against that.
- [ ] **Step 6: Failing test — cheapest-with-presence.** Two known heavy yards, the cheaper one without presence → the buy happens at the more expensive yard that has presence, and the cheaper yard is not silently skipped from future consideration.
- [ ] **Step 7: Implement the loop** per spec §4: capability → cap → cheapest-with-presence → re-quote → floor check → buy → tag. Fail closed on every unreadable input. One heavy per tick maximum.
- [ ] **Step 8: Knob wiring** — bounds registry, defaults mirror, resolver with the RULINGS #5 fallback; wire `liveconfig.Reader` if Task 1 found the autosizer frozen (follow the pattern the sensing coordinator uses).
- [ ] **Step 9: Gates + commit** — `rtk git add gobot/internal && rtk git commit --no-verify -m "feat(autosizer): heavy-hauler purchase loop + heavy_cap dial (sp-fwk8z T2)"`

---

### Task 3: Sensing integration — the reserve and quartermaster coverage

**Files:**
- Modify: the sensing buy-floor path (`internal/application/parkedsensing/buyqueue.go` and its ports) to add the reserve term
- Modify: the sensing screen's yard selection so quartermaster slots cover heavy-selling yards
- Modify: the sensing heartbeat to surface the reserve
- Test: alongside each

- [ ] **Step 1: Failing test — the floor rises.** With a heavy reserve of R, the probe-buy floor is exactly R higher, so a treasury that would have bought a probe now does not; when the reserve drops to 0 (heavy bought), the same treasury buys again. This is the accumulate-then-resume cycle — pin it end to end.
- [ ] **Step 2: Failing test — one definition.** The sensing path's reserve value equals `common.HeavyReserve` for the same inputs (call both, assert equality) so a future divergence fails a test as well as the build guard.
- [ ] **Step 3: Implement the floor term.** Add the reserve to the existing capex slot in the probe-buy floor. The floor must still never fall below `ImmutableReserveFloor`; re-run the existing RULINGS #4 pin test.
- [ ] **Step 4: Failing test — quartermaster coverage.** A system whose shipyard sells heavies but not probes still gets a YARD slot planned. A system selling both gets exactly one slot (the waypoint-keyed table cannot hold two).
- [ ] **Step 5: Implement quartermaster coverage** in the screen's yard selection, honouring the existing MARKET-wins-collision contract documented at the planner's collision site.
- [ ] **Step 6: Heartbeat.** Surface the reserve value and heavies-owned so an operator can distinguish "saving for a heavy" from "sensing is broken" (spec risk 3). Follow the existing `buy_*` field naming.
- [ ] **Step 7: Gates + commit** — `rtk git add gobot/internal && rtk git commit --no-verify -m "feat(parkedsensing): reserve for the next heavy + quartermasters at heavy yards (sp-fwk8z T3)"`

---

### Task 4: Metrics and operator documentation

**Files:**
- Create: metrics collector additions (follow `internal/adapters/metrics/scout_metrics.go` shape — nil-safe, best-effort, registered once)
- Modify: `configs/grafana/dashboards/performance.json`, `gobot/config.yaml.example`

- [ ] **Step 1: Metrics.** Gauges for the current heavy reserve, heavies owned, and cap; a counter or gauge for price-paid-vs-cheapest-known per purchase (spec measurement 3). Label cardinality stays at `player_id` plus at most one dimension.
- [ ] **Step 2: Dashboard.** A panel showing the reserve against probe-buy activity, so the accumulation stall is visible as a cause rather than a mystery.
- [ ] **Step 3: Docs.** `config.yaml.example` gains `heavy_cap` with its default, its live-tunability, and the explicit note that 0 is a hold rather than a disable.
- [ ] **Step 4: Gates + commit** — `rtk git add gobot configs && rtk git commit --no-verify -m "feat(metrics): heavy reserve + purchase-price observability, operator docs (sp-fwk8z T4)"`

---

## Deploy & grading

Rebase onto the final `sp-k6v8z` state, gate via captain-gate, deploy with the daemon restart. Grade on the spec's measurement list — headline is **credits per tour-hour, heavy vs light**; if a heavy does not out-earn a light hull per hour on comparable routes, the cap should not rise and the feature is not paying for itself. Watch time-to-first-heavy and the expansion stall it costs, and price-paid vs cheapest-known to price the presence-lag simplification.

## Self-review

- Spec coverage: §1 gate → T1 queries + T2 gate check; §2 ownership → T2; §3 reserve → T1 predicate + T3 consumption; §4 loop → T2; §5 re-targeting → falls out of T2's per-tick recompute (no task needed, asserted by T2 Step 6); §6 presence → T3; §7 post-purchase tag → T2 Step 5; §8 knob → T2 Step 8; risks 3–4 → T4 heartbeat/metrics and T1 recon.
- The three unverified facts are Task 1 recon deliverables with an explicit STOP if the capacity assumption breaks.
- Type consistency: `HeavyReserveInputs`/`HeavyReserve` named identically in T1, T2, T3.

---

## Task 2 (REVISED post-recon): heavy census, `heavy_cap`, live-tunability, and wiring the reserve

Replaces the superseded Task 2. Recon (spec §RECON CORRECTIONS) found the purchase loop, the yard
targeting, the `trade` tagging and the demand auto-scaling all already exist in the fleet autosizer.
What is missing is a heavy-hull census, a cap on heavy hulls (distinct from the existing trade-pool
ceiling), the ability to tune it live, and the reservation wired into the existing buy path.

**Read first:** spec §RECON CORRECTIONS C1–C3, and Task 1's report addendum (on disk at
`.superpowers/sdd/2026-07-27-heavy-trade/task-1-report.md`; it is git-ignored by design) for the exact
call chain `guardFleetCeiling → act.go → heavies.go → ports.go countShips`.

### 2a. The heavy census — frame list primary, capacity safety net

`FleetCeilingHeavies` counts `DedicatedFleet() == "trade"`. That is a **trade-pool** count and must not
be reused: a heavy tagged anything else goes invisible, leaving the reserve open and authorising a
re-buy (spec C2).

Build a separate census: a hull is heavy if **its frame is in the known-heavy list OR its cargo
capacity is ≥ the heavy threshold.** The threshold lives in the empirically-verified gap — the largest
hull the fleet owns today is 80 (`FRAME_LIGHT_FREIGHTER`), a heavy freighter is ~225 — so anything
ambiguous counts as heavy, over-counting and therefore buying *fewer*: the safe direction.

**A hull that trips the capacity net with an unrecognised frame MUST log loudly** (WARNING, naming the
frame and the hull). That log is the only signal available that the frame list is incomplete, because
the fleet owns no heavy today and the two frame constants are inferred rather than observed. Treat the
first such line in production as the frame list needing correction.

- [ ] Failing test: a hull with a known-heavy frame counts. A hull at/above the threshold with an
      unknown frame counts **and** emits the warning. A hull below the threshold with an unknown frame
      does not count and does not warn. An 80-cargo light freighter never counts.
- [ ] Failing test: the census is independent of `dedicated_fleet` — a heavy tagged `contract`,
      `sensing_parked`, or untagged still counts. This is the money-guard pin; mutation-probe it.
- [ ] Implement, wire as `HeavyReserveInputs.HeaviesOwned`.

### 2b. `heavy_cap` — a new dial, not an extension of `FleetCeilingHeavies`

The two ask different questions (capital exposure in large hulls vs trade-pool size) and must both
apply. Default **5** per the Admiral; bounds `[0, 50]`; `0` is a legitimate operator hold, not a
disable seam.

- [ ] Failing test: `heavy_cap` binds independently of `FleetCeilingHeavies` — at heavy cap with pool
      headroom, no heavy is bought; at pool ceiling with heavy headroom, no heavy is bought.
- [ ] Implement as a distinct guard alongside the existing ceiling guard.

### 2c. Live-tunability — the first `autosizer_*` tune key

Recon: `reconcileOnce` already calls `resolveFleetAutosizerConfig(cmd)` **every tick**; only the
*source* is static (knobs captured on the command struct at container build). So this is "add a tune
key + a liveconfig read", not a restructure.

- [ ] Register `heavy_cap` in the tune bounds registry under the autosizer's container type, with the
      defaults mirror the anti-drift test expects.
- [ ] Point the heavy-cap resolve at a liveconfig snapshot, falling back to the command value when the
      reader is nil or errors (the `liveconfig.Reader` contract: a failed snapshot means launch config,
      never an empty one).
- [ ] Failing test: a tuned value takes effect on the next tick without a rebuild; a nil reader keeps
      launch behaviour; a snapshot error falls back rather than zeroing the cap.

### 2d. Wire the reservation into the existing buy path

- [ ] Failing test: with a heavy reserve outstanding, the floor rises by exactly that amount **for
      every hull class except heavy**, so other purchases stand down while treasury accumulates.

      **CORRECTION (2026-07-27):** an earlier draft of this step said the raised floor should stop a
      heavy purchase too. That was wrong and contradicted spec §4. The reserve *equals* the cheapest
      heavy price, so charging it against the heavy buy demands roughly twice the hull's price — a
      permanent deadlock, the exact circularity §4 forbids. The waiver belongs **inside the pure
      guard**, not at the call sites: a caller that forgot would silently never buy a heavy again.
- [ ] Implement using `common.HeavyReserve` from Task 1. One call, no second copy of the predicate.

### Gates and commit

Gates per Global Constraints. Commit: `rtk git add gobot/internal && rtk git commit --no-verify -m
"feat(autosizer): heavy census, heavy_cap dial, live tuning, reservation wiring (sp-fwk8z T2)"`

### Note on measurement

`sp-wl35j` records that the per-tour spend cap is hull-blind (25% of live treasury, cumulative per
tour), so a heavy is under-filled for budget reasons whenever `unit_price × hold > 0.25 × treasury`.
Early heavy-vs-light readings are a **floor** on the true advantage, not an estimate. Do not tune
`heavy_cap` on them.
