# Standing strategy

## KPI targets
- Credits/hour: **~21,900/hr (24h aggregate). FRAMING REVISED s29 (d-35, Admiral challenge): the binding
  constraint is CYCLE TIME, not contract supply.** Decomposing real ledger+coordinator-log timestamps:
  contract EXECUTION (accept→fulfill = travel+buy+deliver) = **67% of wall-clock** vs negotiation/idle gaps
  **33%** — travel dominates 2:1, and cadence is ENDOGENOUS (the coordinator negotiates the next contract on
  fulfillment, so cadence = delivery cycle time). Bimodal: megas fulfil in ~90s at ship-to-provider
  **distance 0.00**; small contracts drag **21–28 min at distance 630–714 units** with a dozen+ refuel hops
  (86% of execution time for 1.5% of revenue). Root cause: the fleet owns **ZERO light haulers** ("no hauler
  ships exist" → command-ship fallback), so the coordinator's "select closest ship" position-balancer — its
  whole value-add — is INERT with a 1-ship pool. LEVER (d-35 experiment): buy 1 light hauler → real 2-ship
  pool → shorter buy legs → more cycles/hour → higher $/h. **This SUPERSEDES the prior "supply-gated,
  execution can't be willed" framing** (below, kept for history — it was wrong: it treated cadence as
  exogenous when 67% of it is compressible travel).
  Prior (s24, d-29) NET economics still stand: three mega-contracts netted **+155,443 / +169,942 / +197,680
  (avg ~+174k) at ~67-73% margin** after paired cargo+fuel — execution is robustly net-positive; that was
  never in doubt. Watch for margin erosion (rising cargo cost vs payout) as the saturation signal (L13).
- Fleet utilization: no ship idle > 60 minutes without a recorded reason.

## Horizon plan (Admiral challenge, s53 d-60) — mission beyond credits/hour

**Verdict on the Admiral's framing: AGREE the jump gate is the mission spine; REBUT the premise
that idle capital is the problem.** Evidence gathered s53: treasury ~1.72M compounding at ~63k/hr
autonomously — capital is NOT scarce (50% guardrail ~860k/decision). The reason no horizon is moving
is **TOOLING, not treasury.** Two missing verbs gate everything:
  1. **No waypoint/system-discovery verb.** Market cache holds only physically-VISITED marketplaces
     (29 in X1-PZ28). The jump gate is not a marketplace → INVISIBLE; I cannot even address it to
     `ship navigate` there, nor name a neighboring system to `ship jump` to. The daemon HAS this data
     (`ship jump` auto-navigates to the nearest gate) but exposes no READ verb. So JUMP GATE and
     EXPLORATION intel cannot be gathered even with the idle command ship. (L49-class: cache = visited-only.)
  2. **No `ship buy` verb.** Ship subcommands are dock/info/jump/list/navigate/orbit/refresh/refuel/sell
     only — cargo acquisition is workflow-internal (contract/goods/operations pipelines). Manual arbitrage
     is UNEXECUTABLE regardless of the ship-sell fix (d-34) or hauler reservation (L46). This is a NEW,
     more fundamental blocker than L46's three layers → folded into L46 as layer (d).

Capital thresholds are therefore the WRONG trigger for the near term; **tooling-unlock is the real gate.**

### Ranked portfolio (cost-to-unblock × mission value)
- **#1 JUMP GATE — the progression spine.** Unlocks the interstellar network → new systems/markets/
  shipyards/contracts → every other horizon. Characterize cost ~free (one idle-command-ship survey);
  completion cost UNKNOWN (gates need large advanced-material bills — the `construction` verb, never once
  invoked, handles this: depth 3 = buy-final-and-deliver). BLOCKER: can't locate/address the gate
  (discovery-verb gap #1).
- **#2 EXPLORATION — unlocks #1's payoff.** Gated on jump capability. Command ship has NO jump drive.
  Path (i) via the completed gate (free), or (ii) a warp/jump-drive ship. BLOCKER: neighboring-system
  symbols unknown (discovery-verb gap #1) + no jump drive. Recon-first with a cheap probe, never a
  capital ship blind.
- **#3 TRADING — a cash hedge, tooling-blocked.** Spread is REAL and still live s53 (CLOTHING J70 buy
  4781 → A1 sell 11142 = +6,361/u; MEDICINE +5,571/u) and a per-ship arbitrage rate would rival the
  contract rate — BUT no buy verb (unexecutable), SCARCE supply + volume-20 (thin/self-collapsing),
  single-snapshot intel (stability unproven). LOWEST near-term priority: side-quest, not progression,
  most tooling-blocked.
- **#4 FLEET — derived, demand-pulled.** #1 construction needs 1–2 haulers to ferry materials (collides
  with the coordinator's auto-claim, L46c — reservation needed). #2 needs 1 cheap probe (or warp scout).
  #3 needs 1 hauler held out of the coordinator. RULE (L16): buy only against a validated, unblocked
  mission — NOT because treasury is big.

### Sequencing (dependency-ordered) + triggers
0. **NOW → next meta-review:** promote **(a) a waypoint/system-discovery verb** (expose the daemon's
   jump-gate + connections + waypoint-type data) as the #1 feature ask, **(b) a `ship buy` verb** as #2.
   These unlock #1/#2 and #3 respectively. No treasury gate — they ARE the gate. (Cannot file features in
   a heartbeat per CLAUDE.md; queued here for the next meta-review.)
1. Discovery verb lands → survey the jump gate (idle command ship, ~free) → `construction status <gate>`
   to read material requirements.
2. START `construction start --depth 3` the gate WHEN: (a) requirements read; (b) est. material cost
   ≤ 50% treasury at that time; (c) a hauler is sparable without starving the contract earner. At +63k/hr
   treasury clears most single-gate bills within hours.
3. Gate operational → cheap probe recon of the nearest connected system (markets/shipyards).
4. Recon reveals a concrete opportunity → size a fleet expansion to it.
5. Trading revisited ONLY if a `ship buy`/trade-route verb appears → then a single ≤20u round trip on the
   reservable command ship to validate realized-vs-paper net before scaling.

## Current posture
- **s53 (d-60): CLEAN HEARTBEAT + HORIZON PLAN delivered (Admiral challenge). No 404 recurrence.** Treasury
  ~1,721,194 (unchanged from s52; ledger anchor CONTRACT_ACCEPTED +2,635 @14:30:30 → 1,726,838 net real
  refuel/cargo; the -4,996 Balance field is L28 garbage on REFUEL/PURCHASE_CARGO rows). Socket HEALTHY
  (**32nd consecutive clean**: s22 hung, s23–s53 clean); health OK, 3 containers RUNNING (coordinator
  35df0a9f + worker contract-work-TORWIND-3-70030710 + scout-tour 48adae90). The s52 404 signature did NOT
  recur (worker 70030710 running clean since 17:42:50) — the 3-session escalation counter does NOT advance;
  TORWIND-3 mid-delivery (IN_TRANSIT H66, 64/80 PRECIOUS_STONES cmr57lli0). Answered the Admiral with the
  **Horizon plan** (above): centred on the JUMP GATE as the mission spine, and named the true binding
  constraint on ALL non-contract horizons as **TOOLING, not capital** — no waypoint/system-discovery verb
  (can't locate the gate or name neighboring systems) and **no `ship buy` verb** (manual arbitrage
  unexecutable). Both queued as the top-2 meta-review feature asks. HELD, no actuation; d-37 24h verdict
  still the decider (due 2026-07-04T14:00Z, ~20h out), trending strongly toward VALIDATED. Guardrail <=50%
  of ~1,721,194 = **~860k cap**.
- **s52 (d-59): NEW crash signature (404 page-not-found) — a REAL but TRANSIENT API burst that SELF-HEALED; treasury NEW HIGH ~1.72M.**
  First-ever failure signature `API error (status 404): 404 page not found` on `dock ship` / `reload ship: get ship`.
  Pending [130]-[134] + [136]-[140] = TWO consecutive TORWIND-3 workers (b9ce3620 @17:41:46-48, 4d2aa5f2 @17:42:19-20)
  crash+fail on it. Inspected per playbook: health OK; b9ce3620 ran clean until a ~30s 404 burst (17:41:47-17:42:20),
  burned 3 fast retries inside it, released the ship; the coordinator re-spawned worker **70030710 @17:42:50 (AFTER the
  window), RUNNING clean** (dock/GET/refuel all succeed through 17:47:19, restart_count 0). TORWIND-3 never stranded:
  IN_TRANSIT at I68, cargo 64/80 PRECIOUS_STONES, delivering cmr57lli0. **Diagnosis: transient upstream SpaceTraders API
  404, NOT a ship-identity/routing defect** (ship exists; every post-window call succeeds) = **L40-class self-heal** (its
  new addendum). Per playbook, NO Captain correction (stopping the running worker would sabotage the live delivery). Per
  escalation rule (SAME signature 3+ SESSIONS), FIRST session of this signature + self-healed → do NOT file; recorded the
  signature to count next session. **Treasury ~1,721,194** (anchor CONTRACT_ACCEPTED +2,635 @14:30:30 → 1,726,838 net real
  refuel/cargo), a NEW HIGH; this cycle's credits.threshold [126]-[129] are UP (1,726,838), real not L28 garbage; +19,958
  fulfilled clean @17:30 ([125]). Socket HEALTHY (**31st consecutive clean**: s22 hung, s23-s52 clean). **WATCH:** if the
  404-on-dock/get-ship signature recurs in 2 more sessions, or a worker crash-LOOPS with no clean re-spawn and TORWIND-3
  sits idle >60min holding cargo (real strand), ESCALATE via reports/bugs. HELD, no actuation; d-37 24h verdict still the
  decider (due 2026-07-04T14:00Z, ~20h out), trending toward VALIDATED. Guardrail <=50% of 1,721,194 = **~860k cap**.
- **s51 (d-58): TREASURY ALARM = PURE L28 GARBAGE — real treasury NEW HIGH ~1.70M, rate ~63.7k/hr (highest yet).**
  Fleet report opened scary: Credits **-8,955**, FOUR credits.threshold DOWN events at once ([121]/[122]/[123]/[124]:
  100k/250k/500k/1M) + a garbage 24h delta -183,955 (-7,664/hr). **Per L28, checked the ledger BEFORE acting:** the -8,955
  is a DESYNCED Balance column on ONE PURCHASE_CARGO -435 row (14:29:26) — the prior row read 1,704,824 and -435 cannot
  take it there. **TRUE treasury ~= 1,704,389** (last sane 1,704,824 net of the real -435), a NEW HIGH up from s50's
  1,639,884; anchor CONTRACT_FULFILLED +105,963 @14:28:17 -> 1,708,183 -> ACCEPTED +5,305 -> 1,713,488 -> refuel/cargo
  hops. Real treasury never dropped, it ROSE — the four DOWN thresholds are all spurious. Socket HEALTHY (**30th
  consecutive clean**: s22 hung, s23–s51 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + worker +
  scout-tour 48adae90). TORWIND-2 fuel 0/0 = normal solar-scout state. Pending [120] = the s50 JEWELRY far-haul cmr5720iu
  @761.64 (worker b6940c9e) fulfilling CLEANLY +105,963, NOT a failure. On fulfillment the coordinator re-cycled TWICE:
  ACCEPTED +5,305 (worker fe58f258), then negotiated PRECIOUS_STONES cmr57lli0 -> **Selected TORWIND-3 @distance 713.79**
  (worker b9ce3620, now in flight 63/80 cargo at A4) — another sole-eligible-hauler far-haul (L48 addendum s37/s44),
  speed-blind selection INERT, NO escalation. **Real 24h delta ~= 1,704,389 - 175,000 = ~1,529,389 ≈ +63,725/hr** — UP
  from s50's +61,036/hr, now **~2.91× the ~21,900 KPI**, the highest rate yet. HELD, no actuation; d-37 24h verdict still
  the decider (due 2026-07-04T14:00Z, ~20.5h out), trending strongly toward VALIDATED. Guardrail <=50% of 1,704,389 =
  **~852k cap**. **NEW friction flagged for meta-review:** the L28 desynced-Balance false-alarm is a recurring
  observability tax (one corrupt row -> 4 DOWN thresholds + a negative $/hr) — candidate feature: reconcile the Balance
  column or compute credits.threshold off the CONTRACT_* anchor, not the raw row.
- **s50 (d-57): CLEAN MONITORING HEARTBEAT — same JEWELRY far-haul mid-flight (~8min after s49); rate holds ~61k/hr.**
  Treasury **1,639,884** (ledger-confirmed: last REFUEL -360 @14:23:06 → 1,639,884 lands exactly at the fleet report; a
  mild mid-contract dip from the s49 peak 1,640,748 on the JEWELRY PURCHASE_CARGO -53,568 @14:04:59 + refuel hops — normal
  L28/L40, rebounds on fulfillment). Socket HEALTHY (**29th consecutive clean**: s22 hung, s23–s50 clean). Health OK, 3
  containers RUNNING (coordinator 35df0a9f + worker contract-work-TORWIND-3-b6940c9e + scout-tour 48adae90). Pending [119]
  = TORWIND-1 ship.idle DOCKED at D45 = the EXPECTED benched-command-ship state (COMMAND, fallback-only, excluded from the
  hauler pool; idle costs nothing, reason logged). The SAME JEWELRY contract cmr5720iu (TORWIND-3 @distance **761.64**, the
  running-max far-haul, selected 17:15:15) is still mid-execution — coordinator log confirms no new selection since
  17:15:15 ("Waiting for TORWIND-3 to complete contract"). Sole-eligible-hauler case (L48 addendum s37/s44), speed-blind
  selection INERT, NO escalation. **24h delta +1,464,884 ≈ +61,036/hr** — essentially flat with s49's +61,072/hr, still
  **~2.79× the ~21,900 KPI**. The 2-ship pool holds ~61k/hr through its worst-case max far-haul (761.64) without sagging.
  HELD, no actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z, ~20.5h out), trending strongly toward
  VALIDATED. Guardrail <=50% of 1,639,884 = **~820k cap**.
- **s49 (d-56): CLEAN HEARTBEAT — MIXED cycle (big CLOTHING @0.00 payout, then a far-haul JEWELRY @761.64); NEW HIGH 1.64M, rate ~61.1k/hr.**
  Treasury **1,640,748**, a new high (ledger-confirmed: CONTRACT_ACCEPTED +29,887 @14:15:18 lands exactly at the fleet report;
  the driver was CONTRACT_FULFILLED **+129,147** @14:15:15). Socket HEALTHY (**28th consecutive clean**: s22 hung, s23–s49
  clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + FRESH worker contract-work-TORWIND-3-b6940c9e + scout-tour
  48adae90). Pending [118] = TORWIND-3 workflow.finished success=true (container 75eb6669) = the CLOTHING @distance 0.00
  contract cmr56mo7s fulfilling CLEANLY, NOT a failure. Coordinator log: the near-cluster CLOTHING @**0.00** fulfilled
  **+129,147**, then re-cycled — "Idle light haulers discovered" → Negotiated JEWELRY cmr5720iu → **Selected TORWIND-3
  @distance 761.64** (running-max far-haul, now in flight worker b6940c9e). So a far-haul returns after the s46–s48
  near-cluster window — the distribution keeps mixing far and near (L48 addendum: penalty bounded to isolated far contracts,
  not a per-cycle tax). Sole-eligible-hauler case (L48 addendum s37/s44): selection among LIGHT HAULERS only, TORWIND-1
  (COMMAND) excluded, speed-blind selection INERT, no escalation. **24h delta +1,465,748 ≈ +61,072/hr** — UP from s48's
  +56,729/hr, now **~2.79× the ~21,900 KPI**, the highest rate yet. Over s43–s49 the coordinator dealt BOTH extremes
  repeatedly and the aggregate rate rose the whole way (47.3k → 47.3k → 50.1k → 56.7k → 61.1k). HELD, no actuation; d-37 24h
  verdict still the decider (due 2026-07-04T14:00Z, ~20.75h out), trending strongly toward VALIDATED. Guardrail <=50% of
  1,640,748 = **~820k cap**.
- **s48 (d-55): CLEAN HEARTBEAT — short-cluster streak CONTINUES + a big +123,978 payout; NEW HIGH 1.54M, rate ~56.7k/hr.**
  Treasury **1,536,506**, a new high (ledger-confirmed: CONTRACT_ACCEPTED +38,577 @14:03:24 lands exactly at the fleet
  report; the driver was CONTRACT_FULFILLED **+123,978** @14:03:19). Socket HEALTHY (**27th consecutive clean**: s22 hung,
  s23–s48 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + FRESH worker contract-work-TORWIND-3-75eb6669 +
  scout-tour 48adae90). Pending [113]/[115]/[116]/[117] = TORWIND-3 workflow.finished success=true = clean fulfillments,
  NOT failures; [114] = TORWIND-1 ship.idle DOCKED at D45 = the EXPECTED benched-command-ship state (COMMAND, fallback-only,
  excluded from the hauler pool; idle costs nothing, reason logged). Coordinator log shows near-cluster contracts back-to-back:
  SILICON_CRYSTALS @distance **89.94** → CLOTHING @**49.52** → CLOTHING @**0.00** — no far-hauls, all the sole-eligible-hauler
  case (L48 addendum s37/s44), speed-blind selection INERT, NO escalation. Treasury jumped **+160,161** from s47's 1,376,345.
  **24h delta +1,361,506 ≈ +56,729/hr** — UP from s47's +50,056/hr, now **~2.59× the ~21,900 KPI**, the highest rate yet.
  Over the s43–s48 span the coordinator dealt BOTH extremes (594–761 far s43–s45, then 0–180 near s46–s48) and the aggregate
  rate rose the whole way (47.3k → 47.3k → 50.1k → 56.7k) — direct, repeated evidence the far-haul penalty is
  transient/bounded, not a per-cycle tax. HELD, no actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z,
  ~21h out), trending strongly toward VALIDATED. Guardrail <=50% of 1,536,506 = **~768k cap**.
- **s47 (d-54): CLEAN HEARTBEAT — short-cluster streak + a big JEWELRY payout; NEW HIGH 1.38M, rate climbs to ~50k/hr.**
  Treasury **1,376,345**, a new high (ledger-confirmed: CONTRACT_FULFILLED +2,470 @13:47:58 → JEWELRY CONTRACT_ACCEPTED
  +22,935 / FULFILLED **+68,805** @13:50:34 → CONTRACT_ACCEPTED +1,129 → 1,376,345 lands exactly at the fleet report).
  Socket HEALTHY (**26th consecutive clean**: s22 hung, s23–s47 clean). Health OK, 3 containers RUNNING (coordinator
  35df0a9f + FRESH worker contract-work-TORWIND-3-13e7936c + scout-tour 48adae90). Pending [111]/[112] = TORWIND-3
  workflow.finished success=true = the QUARTZ_SAND @0.00 and JEWELRY @82.76 fulfillments, NOT failures. **d-53's
  QUARTZ_SAND @0.00 prediction VALIDATED** (fulfilled fast +2,470 — L48 bounding mechanism, ship already at provider
  market). The coordinator then ran a FAVORABLE short-cluster streak: JEWELRY @distance **82.76** (fulfilled **+68,805**
  in ~3min) then LIQUID_NITROGEN @distance **179.61** (current in-flight worker) — all near-cluster, NO far-hauls, all
  the sole-eligible-hauler case (L48 addendum s37/s44), speed-blind selection INERT, NO escalation. Treasury jumped
  **+66,677** on the JEWELRY payout. **24h delta +1,201,345 ≈ +50,056/hr** — UP from s46's +47,277, now **~2.28× the
  ~21,900 KPI**, the highest rate yet. This is the favorable counterweight to the s43–s45 far-haul window (630/761/594):
  one session later the coordinator dealt 0–180-unit contracts + a big payout, and the aggregate rate rose through both
  extremes — direct evidence the far-haul penalty is transient/bounded, not a per-cycle tax. HELD, no actuation; d-37 24h
  verdict still the decider (due 2026-07-04T14:00Z, ~21.1h out), trending strongly toward VALIDATED. Guardrail <=50% of
  1,376,345 = **~688k cap**.
- **s46 (d-53): CLEAN HEARTBEAT — DIAMONDS far-haul fulfilled; the far-haul run BREAKS to a distance-0.00 cluster cycle.**
  Treasury **1,309,668** (ledger-confirmed: CONTRACT_FULFILLED +7,823 → 1,310,248, then CONTRACT_ACCEPTED +960 → 1,311,208,
  then PURCHASE_CARGO -1,540 → 1,309,668 lands exactly at the fleet report). Socket HEALTHY (**25th consecutive clean**: s22
  hung, s23–s46 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + FRESH worker
  contract-work-TORWIND-3-be14d92a + scout-tour 48adae90). Pending [110] = TORWIND-3 workflow.finished success=true = the s45
  DIAMONDS far-haul cmr55dmgm @distance **594.23** fulfilling CLEANLY ("Contract completed by TORWIND-3" @16:46:08,
  CONTRACT_FULFILLED **+7,823**), NOT a failure. **KEY d-37 SIGNAL:** on fulfillment the coordinator re-cycled — "Idle light
  haulers discovered" → Negotiated QUARTZ_SAND cmr560kj9 → **Selected TORWIND-3 @distance 0.00** (ship ALREADY at the
  provider market). This ENDS the three-consecutive-far-haul run (630 s43 → 761 s44 → 594 s45) with a near-zero cluster
  cycle — the L48 bounding mechanism live (a slow hauler finishing a haul inside a cluster then churning short contracts),
  direct confirmation the far-haul penalty is BOUNDED to isolated far contracts, not a per-cycle tax. Still the
  sole-eligible-hauler case (L48 addendum s37/s44), speed-blind selection INERT, NO escalation. **24h delta +1,134,668 ≈
  +47,277/hr** — essentially flat with s45's +47,287, still **~2.16× the ~21,900 KPI**. HELD, no actuation; d-37 24h verdict
  still the decider (due 2026-07-04T14:00Z, ~21.2h out), trending strongly toward VALIDATED. Guardrail <=50% of 1,309,668 =
  **~655k cap**.
- **s45 (d-52): CLEAN HEARTBEAT — the max far-haul on a LOW-VALUE good fulfilled; rate STILL holds ~47.3k/hr.**
  Treasury **1,309,909** (ledger-confirmed: CONTRACT_ACCEPTED +2,608 @13:28:23 lands exactly there). Socket HEALTHY
  (**24th consecutive clean**: s22 hung, s23–s45 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + FRESH
  worker contract-work-TORWIND-3-c81cce75 + scout-tour 48adae90). Pending [109] = TORWIND-3 workflow.finished
  success=true = the s44 AMMONIA_ICE far-haul cmr54j10h @distance **761.64** (running-max distance) fulfilling CLEANLY
  ("Contract completed by TORWIND-3" @16:28:17, CONTRACT_FULFILLED **+4,817**), NOT a failure. **KEY d-37 SIGNAL:** that
  far-haul carried a SMALL-payout good — netted only +4,817 on 761 units, the textbook L48 far-drag ("86% of execution
  time for ~1.5% of revenue") — yet **24h delta +1,134,909 ≈ +47,287/hr**, essentially flat/up vs s44's +46,999 and
  still **~2.16× the ~21,900 KPI**. The pool's aggregate throughput absorbs even the worst single-contract case (max
  distance × low value) without the daily rate sagging. On fulfillment the coordinator immediately re-cycled: "Idle
  light haulers discovered" → Negotiated DIAMONDS cmr55dmgm → Selected TORWIND-3 @distance **594.23** — a
  THIRD-consecutive far-haul (630 s43 → 761 s44 → 594 now), all the sole-eligible-hauler case (L48 addendum s37/s44),
  speed-blind selection INERT, NO escalation. HELD, no actuation; d-37 24h verdict still the decider (due
  2026-07-04T14:00Z, ~21.5h out), trending strongly toward VALIDATED. Guardrail <=50% of 1,309,909 = **~655k cap**.
- **s44 (d-51): CLEAN HEARTBEAT — same AMMONIA_ICE far-haul mid-flight; rate holds ~47k/hr, verdict ~21.6h out.**
  Treasury **1,302,988** (ledger-confirmed: top REFUEL @13:23:52 lands exactly there; a small mid-contract dip from the
  s43 peak 1,307,320 on PURCHASE_CARGO -3,108 + refuel hops — normal L28/L40, rebounds on fulfillment). Socket HEALTHY
  (**23rd consecutive clean**: s22 hung, s23–s44 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + worker
  contract-work-TORWIND-3-6bfc923f + scout-tour 48adae90). Pending [108] = TORWIND-1 ship.idle DOCKED at D45 = the
  EXPECTED benched-command-ship state (COMMAND, fallback-only, excluded from the hauler pool; idle costs nothing, reason
  logged). The SAME AMMONIA_ICE contract cmr54j10h (TORWIND-3 @distance **761.64**, the running-max far-haul, selected
  16:04:30) is still mid-execution: TORWIND-3 IN_TRANSIT at J69 with 74/80 cargo delivering — sole-eligible-hauler case
  (L48 addendum s37/s44), speed-blind selection INERT, NO escalation. **24h delta +1,127,988 ≈ +46,999/hr** —
  essentially flat with s43's +47,180, still **~2.15× the ~21,900 KPI**. The 2-ship pool holds ~47k/hr through its
  worst-case far-haul without sagging. HELD, no actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z,
  ~21.6h out), trending strongly toward VALIDATED. Guardrail <=50% of 1,302,988 = **~651k cap**.
- **s43 (d-50): CLEAN HEARTBEAT — CLOTHING fulfilled; a TWO-CONSECUTIVE-FAR-HAUL window that STILL beats target 2×.**
  Treasury **1,307,320** (ledger-confirmed: top REFUEL @13:12:20 lands exactly there; the s41/s42 mid-CLOTHING dip
  rebounded as predicted — CONTRACT_FULFILLED **+83,320** @13:04:29 then CONTRACT_ACCEPTED +2,065). Socket HEALTHY
  (**22nd consecutive clean**: s22 hung, s23–s43 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + fresh
  worker contract-work-TORWIND-3-6bfc923f + scout-tour 48adae90). Pending [107] = TORWIND-3 workflow.finished
  success=true = the clean CLOTHING fulfillment, not a failure. **KEY d-37 SIGNAL:** coordinator log shows a far-haul-heavy
  cycle — CLOTHING @distance **630.06** fulfilled +83,320 (~20min), then a new AMMONIA_ICE @distance **761.64** (a NEW max
  far-haul, prior high 714) — both the sole-eligible-hauler case (L48 addendum s37/s44: TORWIND-3 the only LIGHT_HAULER,
  TORWIND-1 COMMAND excluded, "Idle light haulers discovered"), speed-blind selection INERT, NO escalation. Yet **24h delta
  +1,132,320 ≈ +47,180/hr**, UP from s42's +43,667/hr — now **~2.15× the ~21,900 KPI**. So the far-haul cost is real but
  NOT throughput-fatal: a 630/761-unit contract still nets ~50% margin (+83,320 on -41,048 cargo + ~2k refuel). The 2-ship
  pool beats target even through its WORST-CASE selection pattern — strengthens the VALIDATED trend. HELD, no actuation;
  d-37 24h verdict still the decider (due 2026-07-04T14:00Z, ~21.75h out). Guardrail <=50% of 1,307,320 = **~654k cap**.
- **s42 (d-49): CLEAN HEARTBEAT — same cycle as s41 (~10min later), mid-CLOTHING-contract dip.** Treasury
  **1,223,015** (ledger-confirmed: top REFUEL @13:02:31 lands exactly there). Socket HEALTHY (**21st consecutive
  clean**: s22 hung, s23–s42 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + worker
  contract-work-TORWIND-3-8b8f3a39 + scout-tour 48adae90). Pending [106] = TORWIND-1 ship.idle DOCKED at D45 — the
  EXPECTED benched-command-ship state (COMMAND, fallback-only, excluded from the hauler pool; idle costs nothing,
  reason logged). The SAME CLOTHING contract cmr53svaf (TORWIND-3 @distance 630.06) is still mid-execution: its
  PURCHASE_CARGO **-41,048** @12:54:21 + refuel hops drew treasury from the s41 peak 1,265,143 down to 1,223,015 — a
  NORMAL mid-contract dip (L28/L40), rebounds on fulfillment. **24h delta +1,048,015 ≈ +43,667/hr** (dipped from
  s41's +45,422 purely on the not-yet-recovered cargo outlay) — still ~1.99× the ~21,900 KPI. Far-haul is the
  sole-eligible-hauler case (L48 addendum s37/s44), INERT speed-blind selection, NO escalation. HELD, no actuation;
  d-37 24h verdict still the decider (due 2026-07-04T14:00Z, ~21.9h out) — trend strongly toward VALIDATED. Guardrail
  <=50% of 1,223,015 = **~611k cap**.
- **s41 (d-48): CLEAN HEARTBEAT — mixed short/far cycle; rate climbs again to ~45.4k/hr.** Treasury
  **1,265,143** (ledger-confirmed: top REFUEL @12:52 lands exactly there; CONTRACT_FULFILLED +80,327 /
  CONTRACT_ACCEPTED +35,708 confirm a clean cycle). Socket HEALTHY (**20th consecutive clean**: s22 hung,
  s23–s41 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + fresh worker
  contract-work-TORWIND-3-8b8f3a39 + scout-tour 48adae90). Pending [105] = TORWIND-3 workflow.finished
  success=true (clean +80,327 fulfillment, not a failure). Coordinator log shows a MIXED cycle: MEDICINE
  @distance **0.00** (parked-at-market, ~13min, +80,327) then a new CLOTHING @distance **630.06** — the
  sole-eligible-hauler far-haul (L48 addendum s37/s44: TORWIND-3 is the only LIGHT_HAULER, TORWIND-1 is
  COMMAND and excluded, so the far-haul is unavoidable fleet-composition cost, speed-blind selection INERT,
  NO escalation). **24h delta +1,090,143 ≈ +45,422/hr**, up from s40's +40,677/hr — now ~2.07× the ~21,900
  KPI and well above the ~26,655 baseline. HELD, no actuation; d-37 24h verdict still the decider (due
  2026-07-04T14:00Z, ~22.1h out) — trend strongly toward VALIDATED. Guardrail <=50% of 1,265,143 = **~632k cap**.
- **s40 (d-47): CLEAN HEARTBEAT — short-cluster cycling CONTINUES; rate climbs again to ~40.7k/hr.** Treasury
  **1,151,268** (ledger-confirmed; CONTRACT_* anchors match the fleet report exactly). Socket HEALTHY (**19th
  consecutive clean**: s22 hung, s23–s40 clean). Health OK, 3 containers RUNNING (coordinator 35df0a9f + fresh
  worker contract-work-TORWIND-3-8db4589d + scout-tour 48adae90). Pending [104] = TORWIND-3 workflow.finished
  success=true (clean FABRICS fulfillment +134,358, not a failure). **The s39 favorable signal is NOT a one-off:**
  coordinator log shows TORWIND-3 ran FABRICS @distance **106.90** (~4min) then immediately Selected @distance
  **0.00** for MEDICINE (ship already at provider market) — short-cluster cycling, no 714-unit far-hauls (L48
  addendum bounding mechanism). **24h delta +976,268 ≈ +40,677/hr**, up from s39's +38,737/hr — now ~1.86× the
  ~21,900 KPI and well above the ~26,655 baseline. Also CLOSED the stale d-30 (WORKED: contract D fulfilled long
  ago, escalation trigger never fired across s26–s40). HELD, no actuation; d-37 24h verdict still the decider (due
  2026-07-04T14:00Z, ~22.4h out) — trend strongly toward VALIDATED. Guardrail <=50% of 1,151,268 = **~575k cap**.
- **s39 (d-46): MILESTONE — treasury crossed 1M; the d-37 experiment is trending toward VALIDATED.** Treasury
  **1,104,689** (ledger CONTRACT_ACCEPTED anchor @12:27:23 matches the fleet report exactly; pending [99]
  credits.threshold 1,000,000 UP = the first-ever 1M crossing). **24h delta +929,689 ≈ +38,737/hr** — a big jump
  from s38's +29,847/hr, now ~1.77× the ~21,900 KPI and well above the ~26,655 baseline. Socket HEALTHY
  (**18th consecutive clean**: s22 hung, s23–s39 clean). Coordinator 35df0a9f + worker
  contract-work-TORWIND-3-f167eb83 + scout-tour 48adae90 all RUNNING. **The d-37 payoff signal, OPPOSITE of the
  s35–s38 far-haul worry:** the coordinator log shows TORWIND-3 running SHORT-distance contracts back-to-back —
  Selected @distance **88.64** (EQUIPMENT) fulfilled ~3min, then @distance **106.90** (FABRICS) — no 714-unit
  far-hauls this cycle; TORWIND-3 is inside a market cluster churning near-distance contracts fast (the s36
  bounding mechanism, L48 addendum). Two clean fulfillments this window (+103,850, +145,323). Pending
  [100]/[101]/[102] credits.threshold DOWN @ credits=**-40,523** are GARBAGE (L28): the ledger Balance column
  shows -40,523/-40,667 on intermediate mid-contract PURCHASE_CARGO/REFUEL rows while the CONTRACT_* anchors read
  the true 1.07M–1.10M — 3 spurious DOWN thresholds, real treasury never dropped. HELD, no actuation; d-37 24h
  verdict still the decider (due 2026-07-04T14:00Z, ~22.5h out) — trend strongly positive. Guardrail now <=50% of
  1,104,689 = **~552k cap**.
- **s38 (d-45): CLEAN HEARTBEAT — no change from s37; the d-37 verdict is ~22h out.** Treasury **891,331**
  (ledger-confirmed, matches fleet report; 24h delta +716,331 ≈ **+29,847/hr**, beats KPI ~21,900 and holds
  above the ~26,655 baseline). Socket HEALTHY (**17th consecutive clean**: s22 hung, s23–s38 clean). Health OK,
  3 containers RUNNING (coordinator 35df0a9f + worker contract-work-TORWIND-3-41a04d93 + scout-tour 48adae90).
  Pending [97] = TORWIND-1 ship.idle DOCKED at D45 — the EXPECTED benched-command-ship state (fallback-only,
  excluded from the hauler pool now that TORWIND-3 exists; idling costs nothing, recorded reason logged for the
  fleet-utilization KPI). Coordinator selected TORWIND-3 for a new CLOTHING contract cmr52mvdg @distance 714.27
  ("Idle light haulers discovered" → command ship excluded) — the sole-eligible-hauler far-haul (L48 addendum
  s37), NOT a routing bug, NO escalation. CONTRACT_ACCEPTED +56,514 ledgered; TORWIND-3 delivering. HELD, no
  actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z, ~22h out). Guardrail <=50% of 891,331 =
  **~446k cap**.
- **s37 (d-44): CLEAN HEARTBEAT — the far-haul recurred but is NOT a routing bug (command ship is fallback-only).**
  Treasury **892,195** at a new high (ledger-confirmed, matches fleet report; 24h delta +717,195 ≈ **+29,883/hr**,
  beats KPI ~21,900 and edges the ~26,655 baseline). Socket HEALTHY (**16th consecutive clean**: s22 hung,
  s23–s37 clean). Coordinator 35df0a9f + scout-tour 48adae90 + fresh worker contract-work-TORWIND-3-41a04d93 all
  RUNNING. Pending [96] = TORWIND-3 workflow.finished success=true (clean AMMONIA_ICE fulfillment, "Contract
  completed by TORWIND-3" @15:11:30 — not a failure). **KEY REFINEMENT to d-42/d-43:** the coordinator re-selected
  TORWIND-3 for a new CLOTHING contract at **distance 714.27** (the isolated far-haul case), BUT the log reads
  "Idle light haulers discovered" → selection is among LIGHT HAULERS only. TORWIND-1 is a COMMAND ship, used ONLY
  as fallback "when no hauler ships exist" (L43) — now that a real hauler exists it is EXCLUDED from the candidate
  pool. So TORWIND-3 is the SOLE eligible hauler and the 714-unit haul is UNAVOIDABLE given fleet composition,
  NOT the coordinator choosing a slow ship over a faster ELIGIBLE one. The d-42/d-43 escalation criterion presumed
  TORWIND-1 was an eligible candidate the coordinator ignores — it is NOT. So the speed-blind selection is
  currently **INERT** (one eligible hauler = no faster candidate to mis-route around); the far-haul cost is a
  CAPACITY/SPEED question the d-37 experiment measures, not a bug to file. Escalation stays RESERVED for a future
  2+-hauler fleet where a far contract goes to the slow hauler while a faster ELIGIBLE hauler is closer/idle. HELD,
  no actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z). Guardrail now <=50% of 892,195 = **~446k cap**.
- **s36 (d-43): CLEAN HEARTBEAT — the speed-blind leak PARTLY SELF-CORRECTED, did not cost throughput.**
  Treasury **833,337** (24h delta +658,337 ≈ **+27,430/hr** — beats KPI ~21,900 and edges above the ~26,655
  baseline). Socket HEALTHY (**15th consecutive clean**: s22 hung, s23–s36 clean). Coordinator 35df0a9f +
  scout-tour 48adae90 + contract-work-TORWIND-3-92d4285f all RUNNING. Pending [94]/[95] = TORWIND-3
  workflow.finished success=true (clean fulfillments, not failures). **THE s35 WATCH RESOLVED FAVORABLY FOR
  THIS CYCLE:** coordinator log shows TORWIND-3 finished the 714-unit PRECIOUS_STONES haul @14:57:06, then
  landed in a market CLUSTER and immediately ran ALUMINUM @distance **0.00** (fulfilled ~90s, +7,830) and
  AMMONIA_ICE @distance **52.01** (now running) — both because TORWIND-3 was GENUINELY closest, so TORWIND-1
  (COMMAND) idling at D45 is the designed-optimal case, NOT a leak (handing those near-zero contracts to the
  farther ship would ADD travel). MECHANISM: a slow hauler that ends a long haul inside a cluster then churns
  near-zero-distance contracts, which BOUNDS the speed-blind penalty to ISOLATED far contracts, not every
  cycle. The d-42 "idle >2 cycles → escalate" trigger is NOT tripped; REFINED escalation criterion: file a
  coordinator bug report ONLY if a distance->400 contract is routed to speed-15 TORWIND-3 while speed-36
  TORWIND-1 is closer/idle. HELD, no actuation; d-37 24h verdict still the decider (due 2026-07-04T14:00Z).
  Guardrail now <=50% of 833,337 = **~416k cap**.
- **s35 (d-42): CLEAN MONITORING HEARTBEAT — the flagged leak is now OBSERVED, mechanism NAMED.** Treasury
  **814,776** (benign mid-contract dip from the 819,985 peak: PRECIOUS_STONES cargo buy -3,481 + refuel hops;
  rebounds on fulfillment). Socket HEALTHY (**14th consecutive clean**: s22 hung, s23–s35 clean). Coordinator
  35df0a9f + scout-tour 48adae90 RUNNING; TORWIND-3 (LIGHT_HAULER, speed 15) mid-flight on PRECIOUS_STONES
  2a876c3f (59/80 cargo, IN_TRANSIT at I68); TORWIND-1 (COMMAND, speed 36) benched idle-DOCKED 0/40 at D45
  (pending [93] ship.idle — the one-at-a-time bench). **THE d-37 WATCH IS LIVE:** the coordinator's 14:33:31
  "select closest" routed the 714-unit PRECIOUS_STONES haul onto the SLOW hauler because it was
  closest-BY-DISTANCE — selection is **distance-only and SPEED-BLIND**, so a slow-but-closer hauler gets long
  hauls while the 2.4×-faster ship idles. No Captain-side lever fixes this (can't reassign a mid-buy contract,
  can't abort a live worker, selection logic is daemon-side); HELD, no actuation, recorded the idle reason
  (fleet-utilization KPI). The d-37 24h read (due 2026-07-04T14:00Z) settles whether the short-mega positioning
  win (FOOD @88 in ~3min) outweighs this speed-blind loss. If the leak dominates → faster single ship (or bench
  TORWIND-3), and escalate "coordinator selection should weight ETA not raw distance" as a bug report. Guardrail
  now: <=50% of 814,776 = **~407k cap**.
- **s34 (d-41): EXPERIMENT ACTIVATED — d-39/d-40 GRADED WORKED; 2-SHIP POOL IS LIVE AND EARNING.** Treasury
  **819,985** (FOOD-mega CONTRACT_FULFILLED +265,866 @14:33:31Z; 24h delta +644,985 ≈ **+26,874/hr**, beats
  KPI ~21,900 and holds ~26,655). Socket HEALTHY (**13th consecutive clean**: s22 hung, s23–s34 clean).
  **THE FIX TOOK.** Coordinator log: the RUNNING coordinator's 14:22 selection still fell back ("no hauler
  ships exist" — stale in-memory list), but at **14:30:19 the coordinator container RESTARTED**, re-read the
  cache (TORWIND-3 Role=HAULER from the s32 refresh), and at 14:30:24 logged "Idle light haulers discovered"
  with NO fallback → **"Selected TORWIND-3 (distance 88.64)"**. TORWIND-3 fulfilled a FOOD mega
  (accept +98,334 / fulfill +265,866, **net ~+264k after -100,140 cargo) in ~3 MIN** — the d-35/d-48
  cycle-time thesis made real (closer hauler = short buy leg = fast cycle). Then re-selected TORWIND-3 for
  PRECIOUS_STONES (distance 714.27, container 2a876c3f) — the SLOW counter-case now in flight. Fleet:
  TORWIND-1 (COMMAND, idle-benched 0/40 @D45), TORWIND-3 (LIGHT_HAULER, active earner), TORWIND-2 (solar
  scout, running). **HELD capital — no 3rd hauler** (coordinator is one-at-a-time L45-corrected; a 3rd ship
  adds only diminishing positioning; validate the 2-ship $/h first, L16). Guardrail now: <=50% of 820k =
  **~410k cap**. **MEASURE NEXT (d-37, review 2026-07-04T14:00Z):** does 24h $/h hold/beat ~26,655 with the
  2-ship pool running a full day? WATCH the selection mix — if "select closest" keeps routing FAR contracts
  onto the SLOW hauler (speed 15) while faster TORWIND-1 (speed 36) sits idle (as the 714-unit PRECIOUS_STONES
  pick just did), the positioning gain leaks and a faster single ship beats a slow 2nd hauler → d-37 falsified.
  Two mechanism lessons updated: L50 (a role refresh activates on the coordinator's CONTAINER restart, not its
  next in-loop selection) and L49 (fast completion is EVENT-detected in ~3 min, not the 30-53min timeout).
- **s33 (d-40): d-39 FIX CONFIRMED DURABLE; HELD FOR THE NATURAL GRADING FIRE.** Treasury **554,131**
  (ELECTRONICS CONTRACT_FULFILLED +48,363 @14:24:08Z). Socket HEALTHY (**12th consecutive clean**: s22 hung,
  s23–s33 clean). TORWIND-3 still **Role=HAULER** across the session boundary — the s32 refresh did NOT
  re-blank, so the fix is durable. BUT the coordinator's newest selection is still 14:22:19Z (ELECTRONICS, "no
  hauler ships exist"), which ran BEFORE the s32 refresh and so does NOT grade the fix; the first valid test is
  the NEXT selection (~14:52–15:15Z, timeout-gated per L49), not yet fired — 3rd session hitting the same
  timeout wall, but now the fix is confirmed in place so the natural ~14:52 fire is a clean grade (d-39 review
  16:00Z). Cleared TORWIND-1's post-fulfillment phantom (15 ELECTRONICS server=0) via allowlisted
  `ship refresh` → true 0/40 (first CARGO-case exercise; role case was TORWIND-3). Declined to restart the
  coordinator to force an early re-selection: it would preempt d-39's own clean test, add L30 hang risk, and
  give a confounded restart-triggered discovery vs the normal timeout loop — both CLAUDE.md tiebreakers
  (easier-to-reverse, cheaper) favor waiting. Guardrail now: <=50% of 554k = **~277k cap**. **MEASURE NEXT
  (d-40/d-39, review 2026-07-03T16:00Z):** does the ~14:52 selection log "idle light haulers discovered"
  NAMING TORWIND-3 and "select closest" between 2 ships? If it STILL falls back with Role=HAULER + idle-docked
  → escalate to a bug report (filter keys on frame/registration, not cached Role). d-37's 24h $/h verdict
  still due 2026-07-04T14:00Z, conditional on the fix taking.
- **s32 (d-39): THE d-37 EXPERIMENT WAS INERT — ROOT-CAUSED AND FIXED IN-BAND.** Treasury **526,963** (ledger
  CONTRACT_ACCEPTED +20,727 @14:22:25Z on a fresh ELECTRONICS contract). Socket HEALTHY (11th consecutive
  clean: s22 hung / s23-s32 clean). The coordinator's FIRST post-purchase ship-selection (14:22:19Z, 12min
  AFTER the 14:10 buy, TORWIND-3 idle-DOCKED at A2) STILL logged "no hauler ships exist" and fell back to
  TORWIND-1 — so the 308k 2-ship pool was **inert**. ROOT CAUSE: TORWIND-3's daemon-cached **Role was EMPTY**
  while the server held Role=HAULER (an L32/L37 whole-cache desync on a NEW field). FIX: `ship refresh --ship
  TORWIND-3` (**now allowlisted** — the s30 ask landed) reconciled Role→HAULER (persists). This should make
  TORWIND-3 discoverable at the coordinator's NEXT selection (after the ELECTRONICS worker
  contract-work-TORWIND-1-dab6d7f4 times out, ~14:52-15:15Z). **MEASURE NEXT (d-39, review 2026-07-03T16:00Z):**
  does the coordinator now log "idle light haulers discovered" NAMING TORWIND-3 and "select closest" between 2
  ships? If it STILL falls back with Role=HAULER, the filter keys on something other than Role (frame) or the
  role re-blanked → escalate to a bug report. d-37's 24h $/h verdict (2026-07-04T14:00Z) is now CONDITIONAL on
  this fix taking — the effective 2-ship clock starts only once TORWIND-3 is actually discovered. Guardrail now:
  <=50% of 527k = **~263k cap**. L47 gap CLOSED: `ship refresh` is Captain-usable in-band for cargo/position/role
  desyncs (see L50).
- **s30 (d-37): FIRST LIGHT HAULER BOUGHT — the d-35 cycle-time experiment is now RUNNING, not pending.**
  Treasury **506,236** (was 814,733 peak; PURCHASE_SHIP -308,497 @s30). Shipyard A2 priced by a deliberate
  TORWIND-1 visit (the scout structurally can't price shipyards — L49): SHIP_PROBE 21,627 / SHIP_LIGHT_SHUTTLE
  82,905 / SHIP_LIGHT_HAULER 308,497. Bought 1 SHIP_LIGHT_HAULER = **TORWIND-3** (cargo 0/80 = 2x TORWIND-1,
  but speed 15 = ~0.4x, DOCKED idle at A2), giving the coordinator a real 2-ship pool so its "select closest
  ship" balancer stops being inert. FLEET NOW: TORWIND-1 (COMMAND, contract earner), TORWIND-3 (LIGHT_HAULER,
  new — coordinator should discover it as an idle hauler), TORWIND-2 (solar scout). Socket HEALTHY (10th
  consecutive clean session, s22 hung / s23-s31 clean). **s31 (d-38): the first 2-ship coordinator cycle has
  NOT fired yet** — the coordinator's last ship-selection ran 13:52:19, BEFORE the 14:10 hauler purchase, so it
  still logged "command ship as fallback (no hauler ships exist)"; it only re-selects on the current worker's
  ~30-53min timeout (L49), so TORWIND-3's first eligibility lands next session. Held s31: no actuation, and
  deliberately did NOT start a J70->A1 route errand in the idle borrow window, to keep the d-37 24h $/h read
  clean. The CLOTHING mega-contract cmr4zt1e FULFILLED +137,838 @13:59:24Z (ledger shows
  it 3h-offset as 10:59:24). MEASURE NEXT (d-37 review 2026-07-04T14:00Z): does 24h $/h rise above ~21,900
  (hold/beat ~26,655)? does the coordinator log 2-ship "select closest" instead of "command ship fallback"? If
  a full day of a 2-ship pool does NOT lift $/h, the one-at-a-time idle penalty / slow hauler cancels the
  positioning gain and a faster single ship is the better lever. Guardrail now: <=50% of 506k = **~253k cap**.
- **Growth mode: treasury is 699,863 credits** (s28/s29 ledger, CONTRACT_FULFILLED +3,213 -> 699,863
  @10:20:21L; fleet report matched exactly). s29: socket HEALTHY (8th consecutive clean session: s22 hung;
  s23-s29 clean); coordinator cycled cleanly to the next contract (CLOTHING, TORWIND-1 re-assigned 13:52:19Z
  at distance 630). NEW GROWTH LEVER identified (d-35): the fleet owns no light hauler; buying 1 to activate
  the coordinator's position-balancer is the pending experiment, GATED on pricing shipyard A2 (uncached —
  needs a ship visit; do NOT buy blind). s28: the AMMONIA_ICE contract cf9b2a88 FULFILLED
  (+3,213, confirming d-33's prediction), TORWIND-1 went idle at J70 — coordinator should cycle to
  the next contract. s26: contract D fulfilled +5,500 @09:56L,
  next contract accepted +854 @09:59L (cf9b2a88, TORWIND-1 carrying AMMONIA_ICE). s27: cf9b2a88
  still mid multi-trip delivery (0/40 cargo, re-buying), no new CONTRACT_FULFILLED yet — the
  698,972 is a normal mid-contract dip, rebounds on fulfillment.
  The last `CONTRACT_*` row is `CONTRACT_ACCEPTED +2,247 -> 703,627` @09:27:56 local — a fresh
  in-flight contract (contract D) whose cargo buys + a refuel storm drew the balance down from
  the 703,627 accept-leg peak; it will rebound on fulfillment (this is normal mid-contract dip,
  not a loss — s23 read 700,211, s25 reads 696,242 as the same contract keeps outlaying cargo/fuel). The prior four mega-scale fulfillments still stand (+167,097, +184,744, +196,837
  fulfilled; +72,803 accept leg). Guardrail: <=50% of treasury per decision = **~350k cap**.
  The old phantom `PURCHASE_CARGO -2,080` remains a SUNK cosmetic local-ledger desync.
- **POSTURE: CONTINUOUS CONTRACTS via `contract start` — VERIFIED WORKING (s21, d-25/d-26).**
  The contract path is decisively proven net-profitable across 6 fulfillments (+1,547,
  +8,806, +167,097, +184,744, +196,837). **RESOLVED (was UNVERIFIED in s20):** the `contract
  start` fleet coordinator (`contract_fleet_coordinator-player-1-35df0a9f`) DID pick up the
  COMMAND-role TORWIND-1 as an eligible light hauler and executed 4 contracts through it —
  so `contract start` is the confirmed TRUE-continuous primitive (`batch-contract
  --iterations N` does NOT loop, self-completes after one contract, L43). KEY: the
  coordinator's work commits to the DB even during a socket hang (L44), so treasury keeps
  climbing regardless — the s21 in-flight +72,803 contract FULFILLED for +196,837 straight
  through the s22 socket hang. NEXT SESSION: once the socket recovers, confirm the coordinator
  is still RUNNING and earning (new CONTRACT_FULFILLED rows past 12:21Z, treasury > 701,380);
  if it has EXITED (no new rows + TORWIND-1 still idle at F58), relaunch `contract start`.
  TORWIND-1 may carry leftover FOOD/CLOTHING surplus (real, unsellable — ship-sell DEGRADED);
  doesn't block contracts.
- **Socket: DEGRADED (recurring L30/L43 hang from `contract start`).** Hangs spontaneously
  during the coordinator's heavy discovery/negotiation iteration: `health` + `ship list` +
  `container list` return `context deadline exceeded` while the DB path (ledger) answers
  instantly (L19/L30). Recurred at s21 AND s22 start (3rd genuine single-launch recurrence:
  s20 launch / s21 activity / s22 boundary — appended s22 to the report). **s23 UPDATE: socket
  was HEALTHY at start — every verb (health/container/ship/ledger) responded, NO hang, with the
  coordinator running the whole time.** So the hang is INTERMITTENT, not every-session or
  deterministic on `contract start`; keep the DEGRADED label but note it does not recur every
  session. **s24-s27 UPDATE: socket HEALTHY at start again — 6 consecutive clean sessions now
  (s22 hung, s23/s24/s25/s26/s27 clean), confirming the hang is intermittent, not a recurring tax.** The whole point of a socket-health/restart verb still stands (when it DOES hang, a
  session is lost), but the hang is not a guaranteed tax on every coordinator run.
  **This costs OBSERVABILITY, not money (L44)** — contracts keep completing to the DB and the
  daemon self-recovers between sessions. No Captain-side restart exists. The socket-hang
  report `2026-07-02-daemon-socket-hang.md` is `gate_failed` (pipeline attempted a fix, did
  not land). Only treat as a MONEY blocker if a session shows the socket hung AND no new
  CONTRACT_* ledger rows since the last known contract.
- **Scout RUNNING: TORWIND-2** (`scout-tour-...-48adae90`, solar/free, IN_TRANSIT at I67),
  infinite tour of all 26 X1-PZ28 markets — self-healing coverage at zero cost. IRON_ORE
  buyable at B7 (~52); A1 imports CLOTHING/MEDICINE/QUANTUM_DRIVES at a premium (sells
  11,192 / 10,270 / 141,736). If it 4204-crashes on a hop, recover via the third-waypoint
  navigate (Degraded → Scout position desync).
- **Next:** (0) TOP PRIORITY — MEASURE THE CYCLE-TIME EXPERIMENT (s30, d-37 EXECUTED d-35): the hauler is
  BOUGHT (TORWIND-3, SHIP_LIGHT_HAULER, cargo 80/speed 15, 308,497). Do NOT re-buy. Next session(s) READ the
  result: (a) `container logs contract_fleet_coordinator-...` — does it now log "idle light haulers discovered"
  with TORWIND-3 and "select closest ship" picking between 2 ships, instead of "Using command ship as fallback
  (no hauler ships exist)"? (b) 24h $/h from the ledger — does it hold/beat ~26,655 and stay above the ~21,900
  KPI? (c) do distance-heavy contracts now run on the closer ship with fewer 630–714-unit buy legs? FALSIFY
  by d-37 review (2026-07-04T14:00Z): if a full day of a real 2-ship pool does NOT lift $/h, the one-at-a-time
  idle penalty and/or the hauler's slow speed (15 vs 36) cancels the positioning gain → pivot to a faster
  single ship or reserve TORWIND-3 for the J70→A1 route (item 2). Also watch L46c: if the coordinator
  auto-claims TORWIND-3 for contracts, it can't be reserved for the parallel route while the coordinator runs. (1) Derive a NET credits/hour baseline from
  >=3 CONTRACT_FULFILLED rows minus paired cargo/fuel (the ~21.9k/hr aggregate is mega-dependent, L41).
  (2) PARALLEL ROUTE — QUANTIFIED CANDIDATE (s26, d-32/L46):
  **J70 -> A1 manufactured goods.** J70 buys MEDICINE @4,491 / CLOTHING @4,748; A1 sells MEDICINE
  @10,270 / CLOTHING @11,170 -> paper spread +5,779 / +6,422 per unit (~+240k per 40-unit round
  trip est). BLOCKED at TWO NON-capital layers now (was three; s28 d-34 LIFTED the actuator):
  (a) ~~`ship sell` DEGRADED~~ **LIFTED s28 — `ship sell` is crash-safe (d-34)**, though a real
  end-to-end sale is still unproven (the s28 test hit phantom cargo); (b) scout gives ONE
  snapshot/market so the spread can't be validated stable-vs-mirage, and J70 source supply is
  LIMITED; (c) `contract start` auto-claims any idle hauler, so a route-dedicated ship gets
  grabbed for contracts. EXIT: with the actuator crash-safe, the next step is a real J70->A1
  round-trip (<=20u each, ~185k, under the 350k guardrail) to (i) confirm the sell path realizes
  credits AND (ii) compare realized NET to the paper est BEFORE buying a 2nd hauler — but that
  still needs a hauler reservable OUT of the coordinator's auto-claim (layer c) and a stable-spread
  read (layer b). (3)
  With ~700k treasury a 2nd hauler is justifiable IF a validated route needs capacity (guardrail
  ~350k) — but validate first (L16), and (c) means it can't be reserved for a route while the
  coordinator runs. The binding constraint on diversification is TOOLING (ship-sell fix), not capital.
  RE-CORRECTED (s29, d-35/L45): the s24 claim that "extra ships add only position flexibility, not
  throughput" was itself WRONG. The coordinator IS one-at-a-time, but because cycle time is 67% travel,
  position flexibility (shorter buy legs) RAISES contracts/hour = throughput, even one-at-a-time. So a 2nd
  hauler DOES help the CONTRACT earner (via cycle compression) — that is now the TOP experiment (item 0),
  distinct from the parallel-route case. It does NOT add *parallel* contracts (only one runs at a time), but
  it does add cycles/hour. Capital (~700k) is available; the gate is pricing A2, not treasury or validation.

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
  REOPENED `2026-07-02-ship-sell-nil-panic.md` (merged -> new, d-24). s26 UPDATE: report reads
  `merged` AGAIN, but UNVERIFIED — couldn't safely re-test (TORWIND-1 was mid-contract with live
  cargo; testing sell would sabotage the contract; TORWIND-2 is cargo-less). Treat as DEGRADED
  until exercised on a cargo-bearing NON-contract ship. Low impact on the earner (contracts never
  touch this path) BUT it is now the ACTUATOR BLOCKER on the parallel trade route (L46/d-32): a
  manual arbitrage can't offload without a working `ship sell`. Confirming the rebuilt binary is
  crash-safe is the gate that unblocks diversification — next chance to test is any idle
  cargo-bearing ship that is NOT mid-contract.
  **s28 UPDATE — CRASH-SAFE, VERIFIED (d-34).** Exercised `ship sell` on TORWIND-1 (idle,
  non-contract, 9 AMMONIA_ICE leftover) at J70: it did NOT segfault — returned a graceful
  API 4219, i.e. the L33/L42 nil-panic is FIXED in the DEPLOYED binary, not just merged.
  The ACTUATOR gate on diversification is LIFTED. TWO caveats: (1) the 9 units were PHANTOM
  (server=0), so this proved crash-safety but NOT a real end-to-end sale — a real J70->A1
  round-trip still needs to realize credits to fully close the sell path; (2) `ship refresh`
  (the phantom-reconcile verb) is NOT allowlisted, so phantom cargo is still not
  Captain-clearable in-band (L47). Re-label: `ship sell` = crash-safe, real-sale-path
  unverified end-to-end.
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

