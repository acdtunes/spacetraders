# RULINGS.md — Standing Admiral Rulings

**Every shipwright dispatch brief references this file. Read it BEFORE making design decisions.**
These are human-issued standing orders. They override code convenience, test convenience, and
"obvious" optimizations. If your task appears to conflict with a ruling, STOP, flag the bead
`bd human`, and mail the captain — do not resolve the conflict yourself. (Origin: sp-snmb shipped a contract
value-floor that violated ruling #1 because the agent never saw it. A spec-miss, not a code bug.
This file exists so that class of miss cannot recur.)

## The rulings

1. **NEVER skip contracts.** The engine must never decline, skip, or value-floor a contract.
   Sequencing/deferral within deadlines is allowed; refusal is not. (Origin: sp-snmb incident.)

2. **Daemon restarts are ALWAYS resilient.** We patch the engine in real time — every piece of
   operational state (fleet pins, claims, reservations, in-flight worker progress, cooldown
   clocks) must survive a restart. If you add state, you persist it and reload it on boot.
   (Origin: Admiral hard requirement 2026-07-09; sp-w870/sp-bi75/sp-tgp5/sp-o8wi all exist
   because this was violated.)

3. **Single-writer ship state.** The daemon is the ONLY writer of ship state. Never reintroduce
   an in-process CLI mediator/writer — that is the sp-zewt TOCTOU/orphan bug family. New
   operations become daemon containers or daemon RPCs, not CLI-side orchestration.

4. **Money guards fail CLOSED and are never weakened.** The guard stack — bp6f trade floor,
   sp-9aoc factory floor, sp-2dv4 chain-margin + absorption bounds, sp-w3he cross-container
   spend cap, per-run min-margin gates — is layered defense. Cannot read the live balance or
   price → do not spend. No fix may relax a guard as a side effect.

5. **Parametrize, don't hardcode.** Operational values (standby stations, systems, thresholds,
   lane targets) are flags/config, not constants — EXCEPT where the Admiral has ruled a hard
   floor. Two such floors are deliberately non-tunable per-run (see the #5 amendment for the split):
   the **immutable anti-stall bound (50k)** and the **contract working-capital cushion (150k)**.

6. **Fleet purchases follow measured demand + the 25% rule.** Never buy hulls speculatively:
   a purchase requires measured lane/contract demand AND price ≤ ~25% of treasury.
   (Origin: the over-build that starved contracts; captain ruling st-wisp-93mg.)

7. **The ownership model is law.** Pinned/dedicated hulls are never poached (l7h2 P1-P2.5,
   atomic ClaimShip); the command frigate hauls only as last resort (sp-4a4e). Do not code
   around fleet pins, exclusivity (wq7r), or the claim tx.

8. **Every live change notifies the captain — mail AND nudge, every time.** (Admiral order
   2026-07-09.) Un-nudged mail sits unread; a deploy the captain doesn't know about is a
   deploy that didn't happen.

9. **Crew model policy.** Captain session = claude-opus-4-8. Standing crew sessions =
   sonnet-5. The shipwright coordinates and delegates only — every build runs through an
   ephemeral subagent: sonnet (mechanical/fully-spec'd) or opus (anything needing
   root-causing or design judgment), chosen by the shipwright per dispatch
   (Admiral 2026-07-10).

10. **No merge caps.** Fixes/features per day are uncapped by policy (config 1000000).
    Do no cap accounting; throughput is limited by verification, not quotas.

11. **bd is the tracker.** All work is beads (`bd` from the repo root for sp-*); persistent
    knowledge is `bd remember`. No markdown TODOs, no TaskCreate lists, no MEMORY.md.

12. **Merges are COMMITS — verify before trusting.** (Protocol v2, Admiral-ordered 2026-07-09.)
    Commit in the worktree before gating (never stage issues.jsonl; `--no-verify` if the hook
    interferes); after the gate, verify the merged SHA's diffstat lists your files and report
    it. The orchestrator independently verifies before any deploy, close, or notification.
    (Origin: the empty-merge incident — three fixes silently lost to message-only commits.)

13. **Only captain-gate merges agent work to main.** (2026-07-09, origin: sp-4xn4 landed via
    raw `git merge --ff-only` — harmless for a docs/deps change, but a Go change merged that
    way would skip the build/test gate and the empty-merge/stray-sweep protections.) Agents
    NEVER merge to main directly; the sanctioned path is
    `gobot/bin/captain-gate --repo <root> --worktree <wt> --branch <br> --message <m>
    --provision --merge`, followed by numstat verification. Direct main commits are reserved
    for the Admiral (docs, operational state). Dispatch briefs must quote the
    full gate invocation.

14. **Contract operations are single-system.** (2026-07-10, origin: sp-9hu8 — the sourcing
    optimizer selected cross-gate sources the serial contract pipeline can't afford to chase:
    a ~30-min round trip on the one-active-contract clock needs ~200k+ savings to break even,
    and the flat 25k penalty underpriced that ~8×.) Contract sourcing/delivery legs never
    leave the worker's current system. Cross-system logistics belongs to the parallel trade
    engine (tours pre-position goods home); the Admiral ruled the economically-gated
    exception not worth the complexity.

15. **Captain-set wake triggers are ONE-SHOT.** Every captain-declared wake trigger — credit thresholds (credits_above/credits_below), price tripwires (RegimeTripwire), scheduled wakes (next_wake_at) — fires at most once, then is consumed (removed from the persisted policy); the captain re-declares to re-arm. Wake watches (sp-oyer) already follow this. (Origin: Admiral 2026-07-12; sp-wfut credits, sp-a6e0 tripwires.)

16. **`gc` source code is off-limits, full stop.** (2026-07-07; consolidated from memory
    2026-07-18.) The city gateway is out-of-repo shared runtime infrastructure every live
    agent depends on — USE it, never MODIFY it, even for a real bug, even if a bead asks;
    surface its defects for its owner.

17. **Protected paths and prod isolation.** (2026-07-06/07-11 origins; consolidated
    2026-07-18.) Build agents never touch `gobot/internal/captain/**`, `cmd/captain-gate/**`,
    or `city/agents/**` — stop and report if a change seems to need them. Test
    infrastructure never targets the production socket or database: force-inject test
    endpoints so a stray run cannot reach prod.

18. **Three build lanes TOTAL.** (Admiral 2026-07-11; no exempt classes.) Past the cap,
    suite contention and stale-rebase cascades go negative-sum. Exceeding it requires
    NAMING, in the dispatch decision itself, which running lane yields — no named yield,
    no dispatch.

19. **Closed is not armed.** (Admiral 2026-07-17, the arming process failure.) A bead that
    ships a default-off/armable knob is not closed until the knob is ARMED — or consciously
    disabled with the reason recorded — in the arming ledger. Dormant-knob audit at every
    deploy; uncommitted runtime overrides are live fleet state; re-verify arms after every
    restart.

20. **Never block on the Admiral.** (2026-07-06, all templates; canonicalized 2026-07-18.)
    The Admiral is always away: no choice-prompts, no waiting for sign-off — take the
    option you would recommend, record choice + rationale on the bead, and PROCEED. SOLE
    exception: Tier-3 rails (templates, watchkeeper, gate) require sign-off before code
    moves. Approved work is executed, not re-litigated.

## Amendments (Admiral consolidation, 2026-07-18)

- **#1 scope clarified:** the ruling binds the ENGINE — code may never refuse, skip, or
  value-filter a contract while contracts run. Weighting the contract/trade portfolio
  (including freezing contract operations) is an operational Admiral/captain decision made
  through config, never through code that declines work.
- **#5 extended:** spend guards are treasury-RELATIVE above the immutable 50k
  working-capital floor — an absolute cap tuned for a poor treasury must not throttle a
  flush one.
- **#5 split (2026-07-18):** the contract operation's working-capital cushion and the immutable
  anti-stall bound are now DISTINCT hard floors. The **contract working-capital cushion = 150k**
  (contract op's operating capital): bootstrap's hauler + gate-worker/construction spend is affordable
  only when treasury−price ≥ 150k. The **immutable anti-stall floor = 50k** (unchanged): the outer-max
  backstop that keeps mature tour/factory trade able to trade its way out of a low-treasury crunch, and
  the line the fleet autosizer clamps to + the capacity reconciler's DefaultReserveFloorCredits equal
  (their compile-time lockstep guard stays at 50k). The cushion is RAISED above the bound (stricter),
  never below it; both are documented hard constants, not live-tunable. (Origin: sp-7r7w / epic sp-ktio;
  the cushion previously equaled the 50k bound under sp-bpdf and was un-pinned here.)
- **#9 restated as intent:** strongest available model for command/judgment work; standing
  crew on the mid-tier; the shipwright picks the model per dispatch by task complexity;
  cross-model review panels for high-blast-radius work; review-class models are never
  handed bulk code generation.

## Maintenance

New Admiral rulings are appended here (with date + origin) by the shipwright as
they are issued. The captain's standing operational doctrine lives in beads (decision beads,
`bd remember` memories, and the captain template); this file carries only rulings that bind
ENGINEERING decisions.
