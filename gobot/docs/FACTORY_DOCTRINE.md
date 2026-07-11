# FACTORY DOCTRINE — era-2 validated (2026-07-11)

**Status:** AUTHORITATIVE. This document consolidates the Admiral-directed full factory
review (trade-analyst, sp-hzz5) and the era-2 incident evidence into the standing factory
doctrine. Every other factory/manufacturing doc in this directory predates era 2 and is
banner-marked stale — they remain as design history only.

**Primary sources (beads, near-verbatim below):** sp-hzz5 (ranked redesign brief +
validation addendum + C1 de-risk), sp-wedx (supply-first sourcing ruling), sp-a5j7
(restoration build), sp-iv65 (ceiling + its live failure), sp-64je (era-3 C1 build),
sp-rh2z (chain P&L), sp-i0hl (vertical P&L attribution).

---

## The doctrine (one paragraph, portable — era-3 boot inherits this verbatim)

> A factory is a stocker with a local acquisition method. Its output is planner-visible
> stock at cost basis, never passive market supply. Chains are few, tour-aligned, and
> rotation-sized to the worker pool. Sourcing is supply-first with cross-source price
> backstops. Every chain carries its own P&L and dies automatically when it stops paying.
> Input recursion beyond depth-1 is forbidden — raw inputs are bought, not made.

---

## The verdict on "too complex or too simple": BOTH — in opposite places

**The ACQUISITION side is over-built; the REALIZATION side is under-built.** The design
invested its complexity in recursive input trees and dual coordinators — where the era's
ledger says almost no money lives (raw inputs = 0.29% of spend; market-buy ruled
permanently correct, sp-naw6). Meanwhile the side where ALL the money lives — output
realization, ~6.6M/hr gross flowing through tours — had NO stock model, NO chain-level
P&L, NO kill-switch, and was discovered by tours only via market-cache side-effects
(workers showed ~72k/hr while their chains' goods grossed 6.6M/hr through tours). The
redesign moves complexity from input trees to output realization.

## KEEP (proven, cheap, load-bearing)

- **K1 — In-system chain rule + make-led operation.** Simple, correct, guard-verifiable.
- **K2 — Shared worker pool + rotation.** THE anti-erosion mechanism (chains sustain ~1
  visit/1.5h recovery; a camper grinds its chain and its own P&L to zero — measured,
  sp-8w40).
- **K3 — Demand-gated harvest.** Only lift what has a sink; prevents inventory rot.
- **K4 — Supply-first sourcing** (sp-wedx ruling, sp-a5j7 build). The leading-indicator
  fix for every blowup this era.
- **K5 — Launch-time chain-margin guard (sp-2dv4).** Refused candidates cost zero.

## KILL (complexity without money)

- **X1 — Recursive fabricate ladders beyond depth-1.** Raw inputs are 0.29% of buys;
  depth-1 (buy inputs at market, lift output) captures everything. The furnace class
  lived in the recursion. (Build bead: sp-jav2, building this era per Admiral no-deferral order.)
- **X2 — One of the two coordinator designs.** One job, one design; the shipwright
  designates the survivor. (sp-jav2.)
- **X3 — Standing tail chains under ~50k/hr feed value.** Five goods carry 80%+ of
  factory-good revenue; tail goods are opportunistic arb legs, not staffed chains.
- **X4 — The trailing-SELF-median price ceiling as primary protection.** It failed live
  (see Incident record): a ladder drags its own trailing median up behind it. Demoted to
  secondary; the parking decision uses the eligible-source cross-market median (C5).

## CHANGE (ranked by expected $/hr × build cost)

- **C1 — PLANNER-VISIBLE STOCK (the headline; BUILT 2026-07-11 this era, Admiral-pulled-forward; sp-64je).**
  Unify factory output with the warehouse/pre-positioning model: a factory IS a stocker
  whose acquisition method is "lift cheap local production." Output deposits into
  warehouse stock with cost basis recorded; the tour solver sees stock as a reservable
  zero-ask source at basis (deposit→zero-ask withdrawal proven end-to-end, dchv);
  contracts already withdraw. Closes the export-ask-subsidy inversion structurally —
  tours stop buying our own output at laddered asks. Net architecture REDUCTION (two
  models become one). **+300–600k/hr derived** (paid-ask vs rested-lift deltas ×
  measured volumes — plan on the low end).
  **De-risked from history (analyst addendum):** 115 untouched export series held supply
  with SOFTENING asks — production needs no buyer; feeding alone softens asks (−4.1%
  fed-only) while our LIFTING is what tightens (+4.8% fed+lifted). Tours withdrawing at
  basis cannot stall production. Doctrine inherits C1 conditional only on **T2** (burn-in
  acceptance: tours' average acquisition cost for factory goods must track the RESTED ask
  series, not the ladder — single telemetry series, pass/fail).
- **C2 — Chain P&L ledger + kill-switch (sp-rh2z, +100–200k/hr, small).** Per-chain
  realized = output value realized − input costs − lift costs; auto-pause under 30k/hr
  over a 6h window. The portfolio becomes self-pruning.
- **C3 — Chain count re-size: 14–16 → 8–10, tour-book-aligned, rotation-sized.**
  Correct sizing = workers × 3–4 rotation slots, concentrated on the tour book's top
  goods (clothing / adv_circuitry / equipment / medicine / lab_instruments). Fewer and
  aligned — breadth is the tours' job.
- **C4 — Mechanize the REST signal (sp-xdk6, small).** Own-market lift ask >
  cross-source eligible median ⇒ the chain rests one recovery window. Humans stop having
  to notice the 8w40 export-ask-subsidy inversion.
- **C5 — Poison-proof ceiling baseline (folded into sp-a5j7 phase 2).** Ceiling = 1.5×
  the median ask of ELIGIBLE (healthy-supply) sources cross-market — a laddered source
  drops out of eligibility and therefore out of its own baseline.

## Sourcing rules (sp-wedx ruling, authoritative)

1. **Supply eligibility + ranking PRIMARY** — the original SupplyChainResolver design:
   prefer ABUNDANT/HIGH supply, activity next (WEAK is cheapest for buying), price as
   tiebreak. SCARCE parks (fail closed); LIMITED warns (park-level operator-tunable).
2. **Tranche caps SECONDARY** — no single input tranche exceeds the market's
   trade_volume (buys above it are what physically push a market down supply states).
3. **Price ceiling BACKSTOP** — per C5; the lagging indicator, never primary.
4. **Re-source trigger** — a chain mid-run whose source degrades below eligibility
   re-ranks and switches instead of riding it down.
5. **Price-first selection is permitted ONLY in:** era-end mode, micro-buys, EXCHANGE
   markets (per the sp-wedx definitions).

## Incident record (why each rule exists — evidence on the named beads)

- **The ADV_CIRC ladder (sp-iv65):** no input price ceiling existed; the chain bought
  19k/u inputs (4× market) for a 7k/u output — −6.6M in 3h. The launch-time chain guard
  projected once; the ladder climbed DURING the buy round.
- **The ceiling's live failure (sp-iv65 acceptance):** the waypoint-scoped 24h median was
  poisoned by the fleet's own previous-day ladder at that waypoint — 1.5× an inflated
  median never fires. Root of X4/C5.
- **The selector drift (sp-a5j7 archaeology):** the original supply-first selection
  survived, fully implemented, at market_locator.go (FindExportMarketBySupplyPriority —
  doc comment cites 43cr WEAK+ABUNDANT vs 6,863cr RESTRICTED+ABUNDANT), while buyGood
  called the price-first sibling and LOGGED the supply data it ignored. The class was
  fixed once (b469484, 2025-12-02) for RAW materials only; fabricated inputs kept
  price-first for 7 months. **Drift-control lesson: a fix scoped to the symptom's
  surface leaves the dangerous function alive under the innocent name — and new call
  sites will find it.**
- **Every input blowup this era started at a SCARCE/LIMITED source; zero from healthy
  ones** (analyst evidence spine). Supply-first is restoration, not invention.

## Epistemic status (validation addendum, sp-hzz5)

- MEASURED: feeding lowers asks/grows supply; double-draw ladders asks (+21%/11
  tranches); tours paid premiums at our own markets; rest recovers (fitted per-tier
  half-lives).
- PROVEN n=1: stock→zero-ask withdrawal (dchv).
- DERIVED: the C1 +300–600k/hr range.
- The formerly-ASSUMED link (lifting sustains production) is now answered from history —
  see the C1 de-risk above. T2 remains the burn-in gate.
