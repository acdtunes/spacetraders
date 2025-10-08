# Vitest OOM Investigation Notes

## Summary

Running `npx vitest run` inside `web/` consistently ends with Node reporting `FATAL ERROR: Reached heap limit Allocation failed - JavaScript heap out of memory` after **all test files finish successfully**. The crash occurs during Vitest worker shutdown, so the CLI exits with a failure despite green test output.

## Observations

- Heap traces from multiple runs show repeated `Scavenge` attempts followed by long `Mark-Compact` cycles before the termination. Example tail (Node v20.18.1):
  - `Scavenge 3947 -> 3933 MB` repeating
  - `Mark-Compact 4522 -> 4124 MB` taking 4–9 seconds
  - Stack trace points to `node::worker::MessagePort::OnMessage` while Vitest tears down worker threads.
- Increasing the heap to 8 GB with `NODE_OPTIONS="--max-old-space-size=8192"` only delays the failure; the run still OOMs after ~28 minutes.
- The problem reproduces with the new store/overlay tests as well as the earlier suite—so it’s not caused by the recent additions alone.
- Targeted runs (`npx vitest run src/store/__tests__/useStore.test.ts`) complete in <1 s and never exceed ~120 MB.
- Running `node --trace-gc --trace-gc-ignore-scavenger node_modules/vitest/vitest.mjs run` (4 GB heap) captured explicit GC telemetry showing the live set stuck around 3.9 GB: multiple `Mark-Compact (reduce) 3900.8 -> 3900.2 MB` entries occur before the fatal collection. This confirms the leak persists even when workers run sequentially and the GC explicitly attempts compaction.

## Mitigations Attempted

1. **Limit Vitest concurrency** (current config)
   - Forced `pool: 'threads'` with `minThreads/maxThreads = 1` and `maxConcurrency = 1`.
   - Outcome: Single-worker runs avoid parallel overhead but the global run still OOMs while exiting.

2. **Exclude heavy paths**
   - Skipped `dist/`, `node_modules/`, and `src/mocks/` in Vitest config to keep large mock assets out of the graph.
   - No visible improvement for the full run.

3. **Prevent mock scenario timers during tests**
   - Added a `vitest` guard in `mockScenario.ts` so the fake API doesn’t schedule infinite `setTimeout` loops under test.
   - Reduced CPU churn in targeted suites but the end-of-run OOM persists.

4. **Increase Node heap**
   - Tried `NODE_OPTIONS="--max-old-space-size=8192"` and variants with `--trace-gc`.
   - Heap exhaustion still occurs (just later), confirming a leak or runaway allocation inside Vitest workers.
5. **Direct `node --trace-gc` invocation**
   - Because `--trace-gc` is blocked in `NODE_OPTIONS`, executed Vitest via `node --trace-gc --trace-gc-ignore-scavenger node_modules/vitest/vitest.mjs run`.
   - The run aborted at ~312 s with the same OOM signature; trace logs (see `/tmp/vitest-trace.log`) show the process hovering at ~4 GB before crashing, reinforcing that GC reclamation fails during worker teardown.

## Recommended Workflow (until fixed)

- Run suites in **small batches**: `npx vitest run src/hooks/__tests__/...` or `npx vitest run src/store/__tests__/useStore.test.ts`.
- For broader coverage, script sequential runs per directory (e.g. `hooks`, `domain`, `components`) rather than relying on a single `vitest run`.
- If CI requires one command, use `pnpm`/`npm` to launch a custom script that chains smaller invocations while capturing exit codes.

## Next Steps / Ideas

1. **Upgrade Vitest**
   - The project is on `vitest@3.2.4`; check release notes for fixes related to worker leaks (3.3+ has several memory patches).
2. **Instrument memory usage**
   - Run `node --trace-gc --trace-gc-ignore-scavenger node_modules/vitest/vitest.mjs run` and inspect which files load right before the growth spike.
   - Consider `--inspect` with Chrome DevTools to capture a heap snapshot near shutdown.
3. **Split heavyweight imports**
   - Large modules (e.g., `mockScenario.ts`, `SpaceMap` components) may load during tests even if unused. Lazy-loading or stubbing these in tests could shrink the resident set.
4. **Explore alternative runners**
   - If the leak persists after upgrading, evaluate running key unit suites under a lighter runner (e.g., plain `ts-node` + `uvu`) for profiles that don’t need the full Vitest harness.

_Last updated: `2025-10-08`_
