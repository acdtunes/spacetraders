---
title: ship sell command panics with nil pointer dereference in APIMetricsCollector.RecordRateLimitWait
status: merged
kind: fix
---

## Failure signature

`spacetraders ship sell` crashes the CLI process with a SIGSEGV (nil pointer
dereference) instead of selling cargo or returning an error:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x2 addr=0x18 pc=0x10164d7ec]
github.com/andrescamacho/spacetraders-go/internal/adapters/metrics.(*APIMetricsCollector).RecordRateLimitWait
	internal/adapters/metrics/api_metrics.go:134
github.com/andrescamacho/spacetraders-go/internal/adapters/api.(*SpaceTradersClient).request
	internal/adapters/api/client.go:1569
github.com/andrescamacho/spacetraders-go/internal/adapters/api.(*SpaceTradersClient).SellCargo
	internal/adapters/api/client.go:768
internal/application/ship/strategies/cargo_transaction_strategy.go:100 (SellStrategy.Execute)
internal/application/ship/commands/cargo/cargo_transaction.go:223 (executeTransactions)
internal/adapters/cli/ship.go:633 (newShipSellCommand.func1)
```

## Reproduction

```
bin/spacetraders ship sell --ship TORWIND-1 --good IRON_ORE --units 10 --player-id 1
```

Ship TORWIND-1 was DOCKED at X1-PZ28-H63 (a market that imports/buys IRON_ORE at
134/unit). The command panicked immediately (exit code 2) rather than completing.

## Expected vs actual

- **Expected:** the sell either succeeds (cargo -N, credits +N*price, a
  `SELL_CARGO` ledger row) or returns a clean error (e.g. "ship has 0 units").
- **Actual:** the CLI process segfaults. No sell, no ledger row, no recoverable
  error string.

## Suspected root cause

The panic is at `APIMetricsCollector.RecordRateLimitWait` (api_metrics.go:134),
called from `SpaceTradersClient.request` (client.go:1569). The call site is the
rate-limit-wait path, so the API returned a 429 (or the client throttled) and
the code then dereferenced a nil field/receiver while recording the wait — the
`RecordRateLimitWait` receiver or one of the metric handles it touches (line 134)
was never initialized on this client-construction path. This is a plain
construction/nil-guard bug in the metrics adapter, independent of the sell logic
itself; any command that hits the rate-limit-wait branch through this client
instance would panic the same way.

## Impact

- `ship sell` is unusable whenever the rate-limit-wait branch fires — the CLI
  crashes rather than degrading gracefully.
- It blocks manual recovery of stranded cargo. This session it prevented the
  Captain from selling / diagnosing TORWIND-1's IRON_ORE after the contract
  workflow could not deliver it (see the phantom-cargo desync note appended to
  `2026-07-02-daemon-socket-hang.md`). With both the contract-delivery path and
  the manual-sell path broken, TORWIND-1 has no way to offload its cargo.

## Fix direction

Nil-guard / initialize the metrics handle used at api_metrics.go:134, or ensure
`APIMetricsCollector` (and its metric fields) is always non-nil for every
`SpaceTradersClient` construction path. A crash in a metrics side-channel must
never take down the request; recording failures should be best-effort.

## Recurrence — 2026-07-03 (session 20), REOPENED from `merged`

The source fix DID land — `git log` shows commit
`cfad670 fix(metrics): make APIMetricsCollector recording nil-safe` — and this
report was marked `merged` at s16. **But the crash still reproduces byte-for-byte.**
Re-ran `bin/spacetraders ship sell --ship TORWIND-1 --good CLOTHING --units 10
--player-id 1` (TORWIND-1 DOCKED at X1-PZ28-A1, which imports CLOTHING @ sell
11192) and got the identical SIGSEGV at `api_metrics.go:134`
(`RecordRateLimitWait`), same stack, only the `pc=` address differs (0x100c9d7ec
vs the original 0x10164d7ec — expected, different build).

The entire panic stack is in-process in the CLI binary (main.main →
cli.Execute → SellCargoHandler.Handle → SpaceTradersClient.SellCargo →
client.request → RecordRateLimitWait); the sell path does NOT go through the
daemon socket. So the most likely cause of "fix committed but crash persists" is
that the deployed `bin/spacetraders` CLI binary is **stale — built before
cfad670**. This is a rebuild/redeploy gap, not necessarily a source-code
regression: please verify `bin/spacetraders` is rebuilt from a commit that
includes cfad670 (and, if it is, that cfad670 actually guards line 134 on the
rate-limit-wait branch this reproducer hits).

Impact remains low this session — contracts (the real earner) are unaffected and
worked (treasury 333,758). `ship sell` stays DEGRADED; the Captain will not rely
on it for cargo offload until a rebuilt binary is confirmed crash-safe.
