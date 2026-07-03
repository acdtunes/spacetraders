# Standing strategy

## KPI targets
- Credits/hour: establish a baseline over the first 24h of operation, then set
  a target 20% above baseline. Record the baseline here when known.
- Fleet utilization: no ship idle > 60 minutes without a recorded reason.

## Current posture
- Early-growth mode: treasury is **172,451 credits** (s9 ledger, 11 txns). NOTE:
  this includes a phantom `PURCHASE_CARGO -2,080` for cargo the server says the
  ship never received (see Degraded → Phantom cargo), so the true server balance
  is likely ~2,080 higher. Real capital, plenty of runway. No credits/hour
  baseline yet. Capital is available for a considered ship purchase once a route
  is validated (guardrail: <=50% of treasury per decision, ~86k cap).
- **Socket healthy (s9, s10, s11).** `health` OK across all three; the s6/s7/s8
  "hang" was a manual-restart PID-lock race (operator addendum), NOT a bot fault.
- **POSTURE: HOLD on TORWIND-1 (s14, 6th consecutive) — BOUNDED, FIX NOW IN
  QUEUE.** Phantom cargo persists into s14 (`ship info` 40/40, server=0). **s14
  material change: the phantom-cargo report went `status: new` → `awaiting_human`**
  — after 5 sessions unpicked, the fix pipeline finally worked the critical blocker
  and PROPOSED a fix branch, now gated behind the user's manual merge (propose-only
  mode, `captain.auto_merge: false`). The blocker's fix is real and queued, not
  lost. **The d-16 exit condition (promote priority-ordering if still `status:new`)
  is MOOT** — it left `new`. (Root cause remains whole-cache desync, not cargo-only:
  the s13 scout POSITION desync — daemon H64/server H65 → API 4204 — was the same
  class.) HOLD outcome unchanged because the fix is NOT merged and the daemon has
  NOT restarted: `ship info` still reads phantom 40/40, so the purchase-then-deliver
  class stays unreliable and buying a ~50k hauler still risks bricking a 2nd ship
  (L16/L32). Keep the free scout running, defer TORWIND-1, no hauler. Holding is
  ~free (treasury flat 172,451; idle ships don't bleed). **Off-ramp:** user merges
  the proposed `captain/fix-*` branch → daemon restart (`--force`/`make
  restart-daemon`) → verify `ship info` reads 0/40 → run ONE clean batch-contract.
- **Scouting done: IRON_ORE purchasable at X1-PZ28-B7 (~52).** TORWIND-2 scout
  RUNNING again (solar, free) — the one productive asset; its metadata carries all
  26 X1-PZ28 markets on an infinite tour, so coverage is self-healing at zero cost.
  **s13: the scout crash-looped on a POSITION cache desync (API 4204, daemon H64 /
  server H65) and was RECOVERED** by manually `ship navigate`-ing it to a THIRD
  waypoint (H66), which reconciled the position cache; then relaunched the tour
  (`scout-tour-...-48adae90` RUNNING, no 4204). If it 4204-crashes again on a later
  hop, repeat the third-waypoint navigate (see Degraded → Scout position desync). **Contract BLOCKED by phantom cargo, NOT the socket
  (s9/s10, d-12/d-13/d-14):** batch-contract bought 40 IRON_ORE (PURCHASE_CARGO
  -2,080, 23:16Z) and navigated to the delivery waypoint X1-PZ28-H63, but delivery
  fails deterministically — the game server reports the ship has **0** IRON_ORE
  while `ship info` shows a phantom **40/40** (persistent across socket recovery,
  the d-12 relaunch, AND the s10 session boundary). Manual sell to recover CRASHES
  the CLI (nil-pointer panic). Both offload paths for TORWIND-1 are dead. Two bugs
  filed (see Degraded). **Do NOT re-launch batch-contract on TORWIND-1, and do NOT
  buy a replacement hauler to work around it** — the phantom-cargo bug is a
  purchase/cargo-consistency defect that would strand fresh capital on ANY ship
  running purchase-then-deliver. No Captain verb reconciles the cargo cache; only a
  daemon restart re-fetches true state (L34). Once `ship info` reads 0/40,
  re-negotiate/re-run a clean contract.
- **Next after contract:** evaluate a trade route. Known sinks at A1
  (QUANTUM_DRIVES sells @141,736, MEDICINE/CLOTHING @~10k+, all import-SCARCE) —
  find their cheap export source. B7 exports (URANITE @317, MERITIUM @1,189,
  GOLD/SILVER/PLATINUM ores) are candidate buy-low goods; find their importers.

## Operational constraints (learned 2026-07-02 s2)
- **Launch heavy workflows ONE AT A TIME.** Concurrent heavy launches (VRP
  scout-fleet-assignment + contract negotiation) transiently hung the daemon
  socket (~2 min, context deadline exceeded) and killed a coordinator mid-spawn.
  Launch → confirm `health` ok + container RUNNING → then launch the next.
- **Sequence: scout → wait for market data → batch-contract.** batch-contract
  (and any purchase-planning workflow) fails fast without cached market data
  (`no profitability/market data available (scout markets first)`).

## Degraded capabilities (as of 2026-07-02 s2)
- **Actuation: RESTORED.** Phase-2 widened the allowlist to the mutating verbs;
  the fleet can be moved. (Minor gap: `player info` not allowlisted — use
  `ledger`/`player list` instead.)
- **Market/ledger DB: RECOVERED.** player/market/ledger commands respond (no
  SQLSTATE 28P01). Market data is simply empty until the scout populates it.
- **Socket path: RECOVERED (s9).** Verbs respond; daemon healthy. The s6/s7/s8
  "hang" was a manual-restart PID-lock race (operator addendum on the socket-hang
  report), not the bot. The socket-hang bug report's REAL scope is now only s2's
  concurrent-launch stall — keep launching heavy workflows ONE AT A TIME (L22/L25)
  and it stays a non-issue. Report `2026-07-02-daemon-socket-hang.md` went
  `gate_failed` (s10) -> **`status: new` again** (s11) — a gate_failed report is
  NOT terminal; the pipeline re-queued it. Low-priority given the narrowed scope;
  leave it to the pipeline. (This report status is the Captain's window into
  pipeline progress: new = unpicked/re-queued, gate_failed = attempted-but-blocked,
  merged = landed — L35.)
- **Contract fulfillment: DEGRADED — PHANTOM CARGO (s9 filed, PERSISTS s14; FIX
  PROPOSED s14).** TORWIND-1's `ship info` shows 40/40 IRON_ORE that the game server
  says does not exist (0 units); contract delivery fails deterministically (API
  4219). The local `PURCHASE_CARGO -2,080` committed without the server adding cargo
  → local financial state is also desynced by ~2,080. The phantom has survived
  socket recovery, the d-12 relaunch, and the s10→s14 session boundaries — `ship
  info` still reads 40/40 in s14. **s14: report
  `reports/bugs/2026-07-02-phantom-cargo-contract-delivery.md` went `status: new` →
  `awaiting_human`** — the pipeline proposed a fix branch, now pending the user's
  manual merge (propose-only mode). The fix is queued but NOT landed. WORKAROUND
  (unchanged until it lands): do not re-launch batch-contract on TORWIND-1; no
  Captain verb reconciles the cargo cache (navigate/orbit/dock/refuel return
  nav+fuel only, never cargo — L34), so wait for the user to merge the fix + a
  daemon restart to re-fetch true ship state, then verify `ship info` reads 0/40
  before any fresh contract. Trust the SERVER over `ship info` on cargo.
- **Scout position desync: DEGRADED — RECOVERABLE (s13 filed).** TORWIND-2's
  cached position lagged the server by one waypoint (daemon H64 / server H65),
  crash-looping scout-tour with API 4204 "already at destination" (3× on
  2026-07-03; auto-restart re-spawned and re-crashed). Same root class as phantom
  cargo (whole-cache desync, L37) but a DIFFERENT field. Filed
  `reports/bugs/2026-07-03-scout-position-cache-desync.md` (status:new). WORKAROUND
  (confirmed): manually `ship navigate` to a THIRD waypoint (not the stale-cached
  one, not the phantom "already-at" one) → executes from true position, daemon
  reconciles on arrival → relaunch the tour. Position IS Captain-recoverable in-band
  (cargo is NOT — L34).
- **`ship sell`: DEGRADED — CRASHES (s9, bug filed).** `ship sell` panics with a
  nil-pointer SIGSEGV in `APIMetricsCollector.RecordRateLimitWait`
  (api_metrics.go:134) on the rate-limit-wait branch. Filed
  `reports/bugs/2026-07-02-ship-sell-nil-panic.md` (status:new, kind:fix). Manual
  cargo offload/recovery is unavailable until fixed — avoid `ship sell`.
- **Treasury/credits readout: LIKELY FIXED as of s6 (was UNRELIABLE s5).** The
  ledger Balance column now shows correct running totals (176,547 → … → 175,251)
  and the credits.threshold event reads the true balance, not a garbage negative
  — the s5 "REFUEL Balance = txn amount" bug is gone. STILL confirm before
  trusting fully (`player list` may still omit credits; `player info` denied).
  If a reading looks wrong again, fall back to hand-summing ledger AMOUNTS from
  the last CONTRACT_* anchor (L28).
- **Contract visibility: NONE.** Still no command to list contracts
  (`contract list` doesn't exist; only `contract start`). Contract state is only
  observable indirectly via batch-contract container logs — including deadlines,
  which remain unobservable. Track accepted contracts by hand in the log.
- **Daemon restart: NOT AVAILABLE to the Captain.** No CLI verb restarts the
  daemon and process control is permission-denied; on a hang, rely on self-
  recovery and record the incident.

## Revision protocol
Revise this file at any heartbeat where actuals diverge from targets for 2+
consecutive sessions. Note the revision + reason in captain-log.md.
