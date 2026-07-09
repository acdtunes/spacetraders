# RULINGS.md — Standing Admiral Rulings

**Every shipwright dispatch brief references this file. Read it BEFORE making design decisions.**
These are human-issued standing orders. They override code convenience, test convenience, and
"obvious" optimizations. If your task appears to conflict with a ruling, STOP and escalate to
the harbormaster — do not resolve the conflict yourself. (Origin: sp-snmb shipped a contract
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
   floor (e.g. the 50k working-capital reserve is deliberately non-tunable per-run).

6. **Fleet purchases follow measured demand + the 25% rule.** Never buy hulls speculatively:
   a purchase requires measured lane/contract demand AND price ≤ ~25% of treasury.
   (Origin: the over-build that starved contracts; captain ruling st-wisp-93mg.)

7. **The ownership model is law.** Pinned/dedicated hulls are never poached (l7h2 P1-P2.5,
   atomic ClaimShip); the command frigate hauls only as last resort (sp-4a4e). Do not code
   around fleet pins, exclusivity (wq7r), or the claim tx.

8. **Every live change notifies the captain — mail AND nudge, every time.** (Admiral order
   2026-07-09.) Un-nudged mail sits unread; a deploy the captain doesn't know about is a
   deploy that didn't happen.

9. **Crew model policy.** Captain session = fable-5. Standing crew sessions = sonnet-5.
   Ephemeral build subagents tier by complexity: sonnet (mechanical/spec'd), opus (normal
   root-caused builds), fable (architecture/concurrency/economics design only).

10. **No merge caps.** Fixes/features per day are uncapped by policy (config 1000000).
    Do no cap accounting; throughput is limited by verification, not quotas.

11. **bd is the tracker.** All work is beads (`bd` from the repo root for sp-*); persistent
    knowledge is `bd remember`. No markdown TODOs, no TaskCreate lists, no MEMORY.md.

12. **Merges are COMMITS — verify before trusting.** (Protocol v2, Admiral-ordered 2026-07-09.)
    Commit in the worktree before gating (never stage issues.jsonl; `--no-verify` if the hook
    interferes); after the gate, verify the merged SHA's diffstat lists your files and report
    it. The orchestrator independently verifies before any deploy, close, or notification.
    (Origin: the empty-merge incident — three fixes silently lost to message-only commits.)

## Maintenance

New Admiral rulings are appended here (with date + origin) by the harbormaster/shipwright as
they are issued. The captain's standing operational doctrine lives in captain/state/; this file
carries only rulings that bind ENGINEERING decisions.
