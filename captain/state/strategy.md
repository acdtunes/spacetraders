# Standing strategy

## KPI targets
- Credits/hour: **PROVISIONAL baseline ~6,600/hr** (s20: 24h delta +158,758). Treat as
  weak — that window was mostly dead time under the phantom-cargo blocker AND is dominated
  by one lucky ~+155k contract (L41: contract payouts are lumpy). RE-DERIVE a firm
  steady-state rate from >=3 contracts of the s20 continuous loop, then set the target 20%
  above THAT firm figure.
- Fleet utilization: no ship idle > 60 minutes without a recorded reason.

## Current posture
- **Growth mode: treasury is ~503,700 credits** (s20 ledger, last
  `CONTRACT_FULFILLED +184,744 -> balance 503,700`; anchor to the last `CONTRACT_*` row,
  L28 — the Balance column glitches negative mid-batch, ignore it). TWO mega-contracts
  landed in s20 (+167,097 then +184,744); the negotiator is finding unusually rich contracts
  in X1-PZ28 right now. Guardrail: <=50% of treasury per decision = **~250k cap**. The old
  phantom `PURCHASE_CARGO -2,080` remains a SUNK cosmetic local-ledger desync.
- **POSTURE: CONTINUOUS CONTRACTS via `contract start` (s20, d-25) — VERIFY NEXT SESSION.**
  The contract path is decisively proven net-profitable across 4 fulfillments (+1,547,
  +8,806, +167,097, +184,744). KEY FINDING: `batch-contract --iterations N` does NOT loop —
  it self-completes after ONE contract regardless of N (observed with both `5` and `-1`;
  L43). So for TRUE continuous operation I pivoted to the `contract start` fleet coordinator
  (container `contract_fleet_coordinator-player-1-35df0a9f`), which "continuously
  negotiate[s] and execute[s] contracts" until stopped. **UNVERIFIED**: right after launch
  the socket hung (see Degraded → socket), so I have NOT confirmed the coordinator picked up
  TORWIND-1. NEXT SESSION MUST: (a) confirm socket recovered + coordinator RUNNING; (b)
  confirm the COMMAND-role TORWIND-1 qualifies as a "light hauler" for the coordinator — if
  it finds 0 eligible ships it idles/exits and I FALL BACK to per-contract `batch-contract`
  relaunches (still profitable, just needs a relaunch each heartbeat). TORWIND-1 carries 22
  leftover FOOD (real surplus, not a phantom; unsellable — ship-sell DEGRADED); doesn't
  block contracts.
- **Socket: DEGRADED (s20) — spontaneous L30-class hang.** Healthy s9–s20 until, right after
  launching `contract start`, `health` + `container list` returned `context deadline
  exceeded` while the DB path (ledger) answered instantly (L19/L30: socket subsystem hung,
  daemon/DB alive). Likely triggered by the coordinator's heavy discovery iteration (SINGLE
  launch, not a concurrent-launch violation). No Captain-side restart exists; recovers
  between sessions. This is genuine spontaneous-hang evidence (s2-class), distinct from the
  debunked s6/s7/s8 PID-lock class. If still hung at next session start, append to
  `2026-07-02-daemon-socket-hang.md` as a real occurrence.
- **Scout RUNNING: TORWIND-2** (`scout-tour-...-48adae90`, solar/free, IN_TRANSIT at I67),
  infinite tour of all 26 X1-PZ28 markets — self-healing coverage at zero cost. IRON_ORE
  buyable at B7 (~52); A1 imports CLOTHING/MEDICINE/QUANTUM_DRIVES at a premium (sells
  11,192 / 10,270 / 141,736). If it 4204-crashes on a hop, recover via the third-waypoint
  navigate (Degraded → Scout position desync).
- **Next:** (1) Next session, derive a FIRM credits/hour baseline from >=3 CONTRACT_FULFILLED
  rows of the continuous loop (the ~6,600/hr provisional is lumpy, L41) and set the KPI
  target 20% above it. (2) Evaluate a trade route in parallel: A1 premium sinks
  (QUANTUM_DRIVES @141,736, CLOTHING @11,192, MEDICINE @10,270, all import-SCARCE) need a
  cheap export source; B7 exports (URANITE, MERITIUM, GOLD/SILVER/PLATINUM ores) are
  buy-low candidates needing importers. (3) With ~503k treasury a 2nd hauler is justifiable
  IF a validated route needs capacity (guardrail ~250k) — but validate the route first (L16).

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
- **Contract fulfillment: RESOLVED (s17) — was PHANTOM CARGO (s9–s16).**
  `reports/bugs/2026-07-02-phantom-cargo-contract-delivery.md` reached `merged`
  (s16); the daemon restart re-fetched `GET /my/ships` and TORWIND-1's `ship info`
  now reads true **0/40** (was a phantom 40/40 the server called 0 → deterministic
  API 4219 for 6 sessions). The s17 batch-contract runs the purchase-then-deliver
  path cleanly (read 0 cargo → real purchase → navigate to buy). Residual: the old
  `PURCHASE_CARGO -2,080` is a sunk local-ledger row (cosmetic). LESSON RETAINED:
  trust the SERVER over `ship info` on cargo (L32); a whole-cache desync corrupts
  cargo AND position (L37). If 4219 recurs on this contract, the fix regressed —
  stop, do not loop, re-note the report.
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
- **`ship sell`: DEGRADED — FIX REGRESSED / STALE BINARY (s20).** Re-verified at s20:
  `ship sell CLOTHING 10` STILL panics with the identical nil-pointer SIGSEGV in
  `APIMetricsCollector.RecordRateLimitWait` (api_metrics.go:134), despite the report being
  marked `merged` (s16) and the source fix `cfad670 fix(metrics): make APIMetricsCollector
  recording nil-safe` being in `git log`. The whole panic stack is in-process/client-side
  (not via the daemon socket), so the likely gap is a **stale `bin/spacetraders` CLI binary**
  built before cfad670 — a rebuild/redeploy issue, not necessarily a code regression.
  REOPENED `2026-07-02-ship-sell-nil-panic.md` (merged -> new, d-24). Do NOT rely on ship
  sell for cargo offload until a rebuilt binary is confirmed crash-safe. Low impact:
  contracts (the earner) never touch this path.
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
