# Captain Bootstrap Coordinator — Design

**Date:** 2026-07-11
**Status:** Approved (Admiral, via brainstorm) — written-spec review pending
**Scope:** `gobot/` — a new long-running coordinator + a `workflow bootstrap` CLI verb. Reuses existing capabilities (`shipyard purchase`, `scout-all-markets`, `batch-contract`, `construction start`, the manufacturing coordinator). Builds **no** new fabrication, delivery, navigation, or trade logic.

---

## Purpose

At the start of a new era a fresh agent has ~150k credits, one command frigate + one probe, no market data, and none of the standing coordinators running. Today the captain (an autonomous LLM) must re-derive the correct cold-start sequence from lessons every era — slow and error-prone.

**The objective of Phase 1 is building the system's jump gate.** This coordinator encodes the known-good cold-start playbook and drives a cold agent all the way to gate construction — autonomously, idempotently, and recoverably — so the captain launches it once and monitors, never babysits.

## The playbook it encodes (Admiral-directed)

Data first → a contract-income ramp → the gate:

1. **Probes → 3, scouting**, so market data flows ASAP. Contracts are *blocked until data exists* — the contract hubs are chosen from that data.
2. **Contract-hauler ramp**: retire the frigate from contract work (poor fuel/cargo), buy **4–5 light haulers** placed on data-chosen **contract hubs**, run `batch-contract` to drive down cycle-time and drive up $/hr.
3. **Gate**: once $/hr clears a bar, drive `construction start` on the jump-gate site — which auto-discovers materials and produces/acquires/delivers them via the manufacturing coordinator — and monitor until the gate completes.

Trade tours, siting, autosizer, worker-rebalancer, and frontier are **not** Phase 1; they are the mature economy that runs after (or alongside) the gate.

## Constraints from the captain's operating model (`captain/CLAUDE.md`)

- The captain's only actuator is the `spacetraders` CLI ⇒ bootstrap is a CLI-launchable **workflow** verb.
- **Spending guardrails:** `shipyard purchase` always with `--budget`; never > 50% of treasury on one decision; price-check before the first buy of a type; a decision line before capital. Bootstrap encodes these on every buy.
- **Bold posture:** take the obvious cold-start actions autonomously; don't wait to be told.

---

## Architecture

A **long-running reconciler coordinator**, run as a daemon container like `siting-coordinator` / `fleet-autosizer`. Launched once via `spacetraders workflow bootstrap --agent <A>`; it drives the arc to the gate, then exits `COMPLETE`.

It is a **reconciler, not a stored-cursor script** — this is what makes it idempotent and self-healing. Each tick:

1. **Observe** the live world through the daemon (the game is the source of truth): fleet (counts by role, assignments, nav status), market coverage/freshness, active contracts + their containers, factory chains, construction %.
2. **Derive** the current phase from that observation — never read back a persisted phase enum that can desync from reality.
3. **Act on the delta, each action guarded by "already done / in-flight?"** — so re-evaluation, including the first tick after a restart, never double-acts.

### Idempotency & self-healing

- **Auto-reinstantiation** on daemon restart via the coordinator recovery path (sp-7yej lifecycle) — it re-observes and resumes at real state.
- **In-flight detection** — navigating ships (nav status), running containers (list-by-type), pending purchases are all observable, so a restart mid-"buy probe" sees the count already incremented *or* not, and retries/skips — never duplicates.
- **Phantom-cache guard (captain L47)** — ship cargo/position/role cache desyncs and recurs; force `ship refresh` on the pool before any role/assignment decision so a phantom-idle hull isn't misread as busy.
- **No silent stalls (captain L61)** — every tick emits a progress line (phase · delta done · next · blockers) so a wedged reconciler is visible, not a frozen ledger.
- **Terminal idempotency** — once construction = 100%, re-instantiation observes "gate complete" → exits `COMPLETE`.
- **Minimal persisted state** — an operation record (id, player, status, config, started_at) for observability + the disable flag. *Progress is always re-derived*, never trusted from storage. That makes a mid-flight crash a non-event.

### Capital-staged acquisitions

Bootstrap holds an **ordered acquisition priority** — probes (→3) → light haulers (→ **one per viable contract hub, capped at `hauler_target`** 4–5) → gate materials (via `construction start`) — and each tick fires **the highest-priority acquisition that clears BOTH gates**:

- **Readiness gate** — needed and unblocked? (haulers need hub data; the gate needs the $/hr bar.)
- **Capital gate** — affordable *within the captain's guardrail*? Price-checked, spend ≤ 50% of current treasury (which by construction leaves the other half as working buffer), one decision line emitted.

Staging falls out for free: a ~40k probe clears immediately; a ~300k hauler does **not** until contracts have grown treasury to ~600k, so the coordinator keeps earning and re-checks next tick — buys hauler #1 the moment it's affordable, then #2 later, etc. **Never a blind buy-all, never an over-commit**; a lean tick simply earns and waits. The 50% cap doubles as the pacer *and* the reserve; `bootstrap_reserve_margin` tunes the pace.

### Phases (derived from observation, not stored)

- **DATA** — buy probes → 3 (staged, guarded), assign every probe to `scout-all-markets`. Exit when market coverage ≥ `coverage_bar`.
- **INCOME** — retire the frigate from contract work; select contract hubs from the market data; buy light haulers — **one per viable hub, capped at `hauler_target` (4–5)** — staged, placed on hubs; run `batch-contract`. Exit when realized $/hr ≥ `income_bar`.
- **GATE** — discover the jump-gate construction site; ensure the manufacturing coordinator is running (the executor); `construction start <site>` **+ the L57 adoption bounce** (a freshly-created pipeline is inert until the manufacturing coordinator adopts it at startup); monitor `construction status` until complete → `COMPLETE`.

### Fleet scaling & hand-off

Bootstrap is a **deterministic cold-start ramp to fixed Phase-1 targets** — probes → 3; contract haulers → one per viable hub (capped at `hauler_target`) — staged to capital. It is **not** a demand-driven autoscaler.

The demand-driven autoscaler — the standing `fleet-autosizer` (sp-1txd) — **stays off for the entire bootstrap run**: both buying hulls at once would issue conflicting purchases against the same treasury. At `COMPLETE`, bootstrap launches the autosizer and the other standing coordinators as its hand-off; from there the autosizer owns all fleet scaling to demand.

**Gate-construction workers.** Producing and delivering the gate materials needs worker hulls, run by the manufacturing coordinator (the construction executor). Bootstrap sizes them deterministically (autosizer still off): (1) **repurpose first** — when GATE begins and contracts wind down, idle INCOME haulers are claimed by the manufacturing coordinator for produce/deliver tasks, so the income fleet becomes the seed construction workforce; (2) **top-up to the pipeline's shape** — once `construction start` reveals the producing chains, target ~one worker per active gate-material chain + 1–2 delivery haulers, capped at `gate_worker_target`, buying the delta (staged) only if the pool is short; (3) **keep a cash earner** — if the pipeline market-acquires any materials, keep `min_contract_earners` haulers on contracts through GATE and move the rest to construction. Acquisition priority extends: probes → contract haulers → gate workers (mostly repurposed) → gate materials. *(Admiral to confirm this rule.)*

---

## Slices (delivery)

Strict dependency line (each phase's exit is the next's entry). Each merge is a working coordinator driving one phase further.

- **Slice 1 (v1) — coordinator skeleton + DATA phase.** The whole reconciler framework: daemon container, `workflow bootstrap` verb, reconcile loop, observation snapshot, phase derivation, state recovery, config knobs, `bootstrap_disabled`, heartbeat events, `--dry-run`. Its one live phase: **DATA** (probes → 3, scout, coverage bar). Ships a thing that visibly works — launch it on a cold agent, come back to 3 probes scouting. Everything after is "add a phase."
- **Slice 2 — INCOME phase.** Frigate retirement, the **contract-hub selector** (this slice's real sub-design), staged hauler buys onto hubs, `batch-contract`, the $/hr exit bar.
- **Slice 3 — GATE phase.** Gate-site discovery, manufacturing-coordinator ensure + L57 adoption, `construction start`, monitor to completion → `COMPLETE`.

---

## Config (RULINGS #5 — live-by-default, disable escape)

`[bootstrap]`, each documented in `config.yaml.example` with its meaning:

- `bootstrap_disabled` (false) — the emergency escape.
- `probe_target` (3) — DATA target.
- `coverage_bar` — DATA→INCOME exit (fresh markets in the home system(s)).
- `hauler_target` (4–5) — INCOME hull cap (actual = one per viable contract hub, up to this).
- `income_bar` — INCOME→GATE exit ($/hr threshold).
- `reserve_margin` (0.5) — the ≤50%-per-decision guardrail; also the pacer.
- `tick_seconds` — reconcile cadence.
- `gate_worker_target` — cap on gate-construction workers (actual = ~one per active gate-material chain + delivery, up to this).
- `min_contract_earners` — haulers kept on contracts through GATE to fund material acquisition.

## Reuse (build nothing that already exists — captain verification gate)

- **Ships:** `shipyard purchase` (+ `shipyard list` price-check).
- **Scouting:** `workflow scout-all-markets`.
- **Earning:** `workflow batch-contract`.
- **Gate:** `construction start` / `status` / `stop` + the manufacturing coordinator (the executor).
- **Fleet dedication:** `fleet` assign (retire the frigate, dedicate haulers).

Bootstrap owns **sequencing, gating, staging, recovery** — not fabrication, navigation, or trade tactics.

## Observability

- Per-tick **heartbeat** event: phase · delta done · next action · blockers.
- A **decision/event line per purchase** (the guardrail arithmetic — price, treasury, ≤50% check, what would have blocked).
- Metrics: `spacetraders_bootstrap_phase{phase}`, `_probes_total`, `_haulers_total`, `_construction_pct`.

## Testing

- **Reconciler unit tests:** phase derivation from observed state; each action's guard (done / in-flight / ready); staging (buy only when affordable within the guardrail); recovery (re-derive phase after a simulated restart — no double-act, no double-buy).
- **DATA-phase acceptance (Slice 1):** from a cold-agent fixture, reaches 3 probes scouting; idempotent across a simulated restart mid-purchase.

## Open questions (deferred to the slice that needs them)

- **Slice 2:** contract-hub selection heuristic — which waypoints qualify as hubs (by contract-good sourcing cost / market clustering from the scouted data); `income_bar` calibration.
- **Slice 3:** gate-site discovery — how bootstrap finds the jump-gate-under-construction waypoint; whether INCOME haulers repurpose as manufacturing workers once contracts wind down.
