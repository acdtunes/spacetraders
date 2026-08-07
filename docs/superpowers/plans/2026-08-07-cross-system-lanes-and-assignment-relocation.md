# Cross-System Lane Counting + Assignment-Based Relocation

> **For agentic workers:** Use `superpowers:subagent-driven-development` to execute this
> task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let the fleet see the ~900 cross-system trade lanes it currently cannot count, then place
hulls across them by global assignment instead of per-hull greedy scoring.

**Architecture:** Two phases, strictly ordered. Phase 1 corrects the lane census that gates hull
purchasing. Phase 2 replaces the relocator's greedy scoring with an OR-Tools assignment over the
corrected lane set. Phase 2 is worthless before Phase 1 — optimising over eight visible lanes changes
nothing.

**Tech Stack:** Go (daemon, coordinators), Python + OR-Tools 9.15.6755 (routing service, already
in production for the tour sequencer), gRPC between them.

## The measurement this plan rests on

Run against the staging market cache 2026-08-07, **calibrated first**: restricted to within-system
pairs the query returns 8, exactly reproducing the code's live `profitable = 8`. The same query
without that restriction returns:

| | |
|---|---|
| distinct cross-system lanes clearing the 1000/unit floor | **927** |
| median volume cap | 22 units |
| max volume cap | 180 units |
| lanes with ≥60 units depth | 130 |
| median value per trip | 48,160 cr |
| best *within-system* lane, for comparison | 52,260 cr |

**Cross-system lanes are not deeper — they are more numerous.** Median depth 22 units against 20–39
within-system; median value 48,160 against a best-in-system 52,260. Per-lane economics are the same.
There are simply ~116× more of them.

This kills "find deeper markets" as a strategy and establishes the real one: fly more lanes in
parallel. It also means the fleet currently has **more hulls (9) than visible lanes (8)** — which is
what the wave reports, correctly, given what it counts.

## Global Constraints

- **RULINGS #4:** money guards fail CLOSED and are never weakened. A corrected count must never
  authorise a purchase against an unreachable or unprofitable pairing.
- **RULINGS #7:** hull-ownership protections (command frigate, pinned) are never waived.
- **NO FEATURE FLAGS.** Ship armed. No default-off seam, no env override, no `-short` bypass.
- **Protected paths, never modify:** `gobot/internal/captain/**`, `cmd/captain-gate/**`,
  `city/agents/**`. `gc` source is off-limits entirely.
- **Do NOT lower `MinBidMargin` (1000/unit).** It is a money guard and a different lever. The defect
  is the SCOPE of what is counted, not the threshold applied to it.
- **Comment density is gated** by `go test ./cmd/comment-audit/`. The per-file ceiling applies to
  files a lane CHANGED. No bead ids, no archaeology, no pinned measured numbers in code comments —
  those belong in the commit message.
- **Python changes are now gated too** (`cmd/routing-service/pytest_gate_test.go`, 271 tests). It
  caches on mtime, so a Python edit costs ~127s per `go test ./...`.

---

## Phase 1 — Count the lanes that exist

### Task 1: Pool listings across systems, and count every lane rather than the best per good

**Files:**
- Modify: `gobot/internal/application/trading/queries/profitable_lane_reader.go`
- Test: `gobot/internal/application/trading/queries/profitable_lane_reader_test.go`

**THE SUBTLETY THAT MAKES THE OBVIOUS FIX WRONG.** `trading.RankSpreads` groups by good and returns
**one** lane per good via `bestLaneForGood`. Today `CountProfitableLanes` calls it once per system, so
the count is "best lane per good, per system, deduped" — that is why it is 8. **Simply pooling all
listings and calling `RankSpreads` once would return one lane per good GLOBALLY and make the number
SMALLER.** `RankSpreads` is a selection primitive answering "which lane should this hull fly"; the
wave needs a census answering "how much profitable work exists". They are different questions and
reusing the selector for the census is the original error.

- [ ] **Step 1: Write the failing test.** Two markets in system A and two in system B, priced so that
      a cross-system pair clears the floor and no within-system pair does. Assert the count is the
      number of distinct floor-clearing `(good, source, dest)` lanes — not 0, and not 1-per-good.
- [ ] **Step 2: Run it, confirm it fails** against today's per-system loop.
- [ ] **Step 3: Implement.** Collect listings once across all systems. Enumerate every
      floor-clearing pair, deduped by `(good, sourceWaypoint, destWaypoint)`. Keep the existing
      fail-closed behaviour: a market-list read error still fails the WHOLE count, never a partial
      surface feeding a spend decision.
- [ ] **Step 4: Run tests.**
- [ ] **Step 5: Commit.**

### Task 2: Bound by reachability using the existing gate graph

**Files:**
- Modify: `gobot/internal/application/trading/queries/profitable_lane_reader.go`
- Consume: `gobot/internal/application/system/gategraph` (already resolves multi-jump cross-system routes)

**Do not invent a second reachability notion.** The exclusion this plan removes existed precisely
because an unreachable pairing would over-count. `gategraph` is what the planner already routes with;
the census must agree with it or the two will drift.

- [ ] **Step 1:** Write a failing test: an unreachable pairing must NOT count.
- [ ] **Step 2:** Run it, confirm it fails.
- [ ] **Step 3:** Implement via `gategraph`. On an unreadable gate graph, fail CLOSED (readable=false)
      — an unreadable reachability surface is not evidence of reachability.
- [ ] **Step 4:** Run tests.
- [ ] **Step 5:** Commit.

### Task 3: Price the jump into the spread test

**Files:**
- Modify: `gobot/internal/application/trading/queries/profitable_lane_reader.go`
- Test: same package

The jump fee is a per-origin-gate constant (~5,000 cr observed). At a median 48,160 cr trip it is
roughly a 10% haircut — not fatal, but it must be subtracted, not assumed away, or the count
over-states. Context for calibration: at `MinBidMargin` 1000/unit an 80-unit light covers a 5k jump at
62.5/unit and a 225-unit heavy at 22.2/unit, so the fee is not binding at today's floor.

- [ ] **Step 1:** Failing test — a lane whose spread clears the floor but NOT the floor-plus-jump-cost
      must not count.
- [ ] **Step 2:** Run it, confirm it fails.
- [ ] **Step 3:** Implement. Multi-jump routes pay per gate crossed.
- [ ] **Step 4:** Run tests.
- [ ] **Step 5:** Commit.

### Task 4: Check what the corrected number does to its consumers BEFORE arming

**Files:**
- Read: `gobot/internal/application/common/growth_wave.go`,
  `gobot/internal/application/fleet/commands/` (heavy demand)
- Test: wherever the wave's inputs are exercised

**This is the riskiest step in the plan and it is not a code change — it is a decision.** `unserved`
feeds `DeriveWave` AND the growth coordinator's heavy demand. Both were calibrated against a number
that has been pinned at 0 for the fleet's entire life. Going from 8 to several hundred could authorise
the full `heavy_cap` on the first corrected tick.

- [ ] **Step 1:** Enumerate every reader of `UnservedLaneCount` and state, in the report, what each
      does when the value jumps ~100×.
- [ ] **Step 2:** Write a test pinning the wave's behaviour at a large unserved value.
- [ ] **Step 3:** **STOP AND REPORT** with the enumeration and the projected first-tick behaviour.
      Do not arm. The lead decides whether a rate limit on heavy purchases is needed before this
      lands — RULINGS #4 says a money path fails closed, and a 100× input jump deserves an explicit
      ruling rather than an inference.

### Task 5: Publish the corrected terms

**Files:**
- Modify: `gobot/internal/adapters/metrics/fleet_growth_metrics.go` (the `growth_lane_surface` gauge
  added by sp-lqd16)

The existing gauge publishes `profitable / trade_pool / unserved / systems_scanned / readable`. Add
the new terms so the corrected count is as legible as the old one: cross-system lanes counted,
lanes rejected as unreachable, lanes rejected on jump cost. Without these, a future reader cannot tell
a genuinely thin surface from a reachability bug — which is exactly the blindness sp-lqd16 fixed one
layer up.

- [ ] Steps 1–5 as above: failing test, confirm, implement, verify, commit.

---

## Phase 2 — Assignment-based relocation

**Do not start until Phase 1 is merged, deployed, and the corrected lane count is observed live.**

### Task 6: Model hull placement as an assignment problem

**Files:**
- Create: `gobot/services/routing-service/handlers/placement_handler.py`
- Create: `gobot/services/routing-service/utils/placement_solver.py`
- Test: `gobot/services/routing-service/tests/test_placement_solver.py`

OR-Tools is already pinned and in production for the tour sequencer, so this adds no new dependency.

**Why assignment and not a second VRP:** the tour solver already routes *within* a tour. Relocation is
placement — which hull to which system/region. Smaller and better-conditioned than what already runs.

**The objective must price CONTENTION, which is the whole reason to do this.** Greedy scores each hull
independently and therefore cannot see that two hulls sent to the same 22-unit lane split it rather
than doubling it. With median depth 22 units against 80-unit hulls, contention is the dominant term,
not a correction.

**This also dissolves the uplift bar rather than tuning it.** The relocator today declines nearly
everything because nothing clears a 150% uplift threshold in a market where every lane is worth
~48k (observed: 74 `mid_tour`, 6 `no_uplift`, 1 relocation). That bar exists because greedy needs a
churn threshold. A global objective does not — it moves a hull when the assignment improves.

- [ ] **Step 1:** Failing test — two hulls, one deep lane and one shallow. Assert the solver does NOT
      put both on the deep lane when splitting earns less than spreading.
- [ ] **Step 2:** Run, confirm it fails.
- [ ] **Step 3:** Implement the OR-Tools assignment. Objective: maximise total fleet throughput =
      Σ(lane value realised), with per-lane absorption as a capacity constraint and travel cost
      (jump fees + time) as an edge cost.
- [ ] **Step 4:** Run tests.
- [ ] **Step 5:** Commit.

### Task 7: gRPC surface for placement

**Files:**
- Modify: `gobot/services/routing-service/generated/` protos, `server/`
- Modify: `gobot/internal/adapters/routing/` (Go client)

**Known hazard, documented in this repo:** `tour_handler.py` rebuilds the constraints dict field by
field, so a proto field that is not explicitly copied is dead on arrival. Any new field needs a
handler bridge AND a test that drives the real RPC — a survivor mutation at a proto boundary is how
sp-g2vi0 nearly shipped a fix that carried no data.

- [ ] Steps 1–5 as above.

### Task 8: Replace the relocator's scoring with the solver's answer

**Files:**
- Modify: `gobot/internal/application/trading/commands/run_opportunity_relocator_scoring.go`
- Modify: `gobot/internal/application/trading/commands/run_opportunity_relocator.go`

- [ ] **Step 1:** Failing test — the relocator acts on the assignment rather than per-hull uplift.
- [ ] **Step 2:** Run, confirm it fails.
- [ ] **Step 3:** Implement. Keep `hullProtected` and `actuationVerdict` exactly as they are —
      RULINGS #7 protections are not part of this change and must still run before every move.
- [ ] **Step 4:** Run tests, including `-race`.
- [ ] **Step 5:** Commit.

---

## Open questions, deliberately not answered here

1. **Rate-limiting heavy purchases** after the count jumps. Task 4 surfaces the facts; the ruling is
   the lead's.
2. **How often to re-solve.** The relocator ticks at 120s. An assignment over ~900 lanes may not want
   that cadence, and the solver's runtime is unmeasured.
3. **Whether the corrected count should feed the growth coordinator's heavy demand unchanged**, or
   whether demand wants a different function of it than the wave does. They currently share one
   number.

## What this plan does NOT claim

It does not make markets deeper. Median cross-system depth is 22 units — statistically identical to
within-system. Per-lane value is unchanged. The entire gain is **parallelism**: ~900 lanes worth ~48k
each instead of 8, and enough hulls to fly them. If the real constraint later proves to be absorption
across the whole fleet rather than lane count, this plan will not fix that and a different one is
needed.
