---
title: construction depth-3 planning aborts on MODERATE supply — HIGH/ABUNDANT-only market filter (plus all-or-nothing planning) keeps the jump gate unbuildable
status: merged
kind: fix
---

## Failure signature

- Command: `spacetraders construction start X1-PZ28-I67 --depth 3 --player-id 1`
- Error: `failed to start construction pipeline: ... failed to create tasks for ADVANCED_CIRCUITRY: no market with good supply for ADVANCED_CIRCUITRY`
- Occurred 2026-07-05 ~03:15Z on the freshly deployed re-plan fix (f7d644d). The re-plan half WORKED: the stale empty pipeline 6c02cbe3 was terminalized (FAILED) and `construction status` no longer reports an active pipeline. Planning then aborted at task creation.

## Evidence

- `market get --waypoint X1-PZ28-D45` (data 03:07Z, minutes old): ADVANCED_CIRCUITRY supply **MODERATE**, activity RESTRICTED, buy price 1,893, volume 20. The good IS exported and purchasable in-system — the planner just refuses MODERATE.
- `market get --waypoint X1-PZ28-F56` (data 02:59Z): FAB_MATS supply **ABUNDANT** @520 — passes the filter fine. The whole plan (including the perfectly sourceable 1600-unit FAB_MATS leg, ~832k) is discarded because ONE material is merely MODERATE.
- D45 is the only known ADVANCED_CIRCUITRY exporter in X1-PZ28 (s86 system sweep; the planner's own DB sweep tonight found no HIGH/ABUNDANT one either).
- Waiting for organic supply improvement is not credible: D45's activity is RESTRICTED and its own inputs (ELECTRONICS SCARCE, MICROPROCESSORS SCARCE at D45) are starved, so its export supply is not trending up.

## Expected vs actual

- Expected: depth 3 ("buy final") creates one DELIVER_TO_CONSTRUCTION task per outstanding material sourced from the best in-system exporter, tolerating average (MODERATE) prices for a one-shot, mission-critical buy (~1.59M total vs a 7.7M treasury and a 2.2M ceiling).
- Actual: planning hard-fails the ENTIRE pipeline unless every material has a HIGH or ABUNDANT exporter. MODERATE costs 0–15% over base (≤ ~100k premium on the 757k circuitry leg) — trivial against the gate being the mission's only route out of the system, blocked indefinitely.

## Impact

Jump-gate construction (mission priority #1) remains hard-blocked after THREE sequential fixes (73f3f08 execution wiring, f7d644d re-plan) finally cleared the code and state paths. Everything else is ready: treasury 7.7M, materials buyable in-system, coordinator running, executor registered. This filter is now the only blocker. Depth 2 is no workaround: the same `FindExportMarketWithGoodSupply` gate is applied to every intermediate/raw input (ELECTRONICS passes at F56 HIGH, but MICROPROCESSORS, IRON, QUARTZ_SAND etc. are SCARCE at their factories), so depth-2 planning aborts identically.

## Code checked

- `gobot/internal/application/manufacturing/services/construction_pipeline_planner.go:222-231` — the depth>=3 / raw-material branch calls `p.marketLocator.FindExportMarketWithGoodSupply(...)`; `nil` result → `fmt.Errorf("no market with good supply for %s", targetGood)`. Same call in `createBuyAndDeliverTask` (`:339-345`) and `createAcquireDeliverTask` (`:368-374`, used by depth 1–2 input sourcing).
- `construction_pipeline_planner.go:167-172` — materials loop: the first `createTasksForMaterial` error aborts `StartOrResume` entirely; nothing is persisted (pipeline Create happens later at `:182`), so already-planned sourceable materials (FAB_MATS) are thrown away too.
- `gobot/internal/application/manufacturing/services/market_locator.go:362-457` (`FindExportMarketWithGoodSupply`) — the filter: `if supply != "HIGH" && supply != "ABUNDANT" { continue }` (`:412-414`); zero candidates returns `nil, nil` (`:435-437`). Doc comment (`:352-361`) says MODERATE = "0-15% (average prices)"; the NEVER-BUY tier it warns about is SCARCE (+30-70%).
- `market_locator.go:243-350` (`FindExportMarketBySupplyPriority`) — an EXISTING locator that does exactly what construction needs: accepts MODERATE+ (`supplyPriority` map `:272-276`), still skips SCARCE/LIMITED (`:294-298`), ranks ABUNDANT > HIGH > MODERATE then WEAK-first activity then price, and returns a descriptive error naming the threshold (`:316-318`). Its own doc says it "is used for raw material acquisition in manufacturing pipelines" — the strict HIGH+ variant is meant for margin-sensitive supply-gated loops, not a one-shot construction bill.
- Conclusion: the capability (MODERATE-tolerant export sourcing) already exists; the construction planner is simply wired to the wrong locator, and its all-or-nothing loop amplifies one unsourceable material into a total planning failure.

## Suspected root cause and suggested fix

The construction planner reuses the manufacturing supply-gate (`FindExportMarketWithGoodSupply`, tuned to only buy when prices are BELOW average) for a context where completing the purchase matters far more than a 0–15% price premium.

Minimal fix: in `construction_pipeline_planner.go`, replace the three `FindExportMarketWithGoodSupply` calls (`:225`, `:339`, `:368`) with `FindExportMarketBySupplyPriority` (MODERATE+, already SCARCE/LIMITED-safe). Its non-nil error return on no-candidates also improves the error message for free.

Optional hardening (separate concern, fine to skip for minimal diff): make the materials loop at `:167-172` plan per-material — skip-and-log an unsourceable material instead of aborting, so a pipeline can start delivering FAB_MATS while ADVANCED_CIRCUITRY waits for supply. Only do this if pipeline completion semantics tolerate a partial-material pipeline; otherwise leave as-is.

Merged directly by harbormaster correctness wave (Admiral-ordered), 2026-07-05 — planner rewired to FindExportMarketBySupplyPriority; all-or-nothing loop left as-is.
