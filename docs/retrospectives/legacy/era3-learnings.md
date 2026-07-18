# Era 3 (torwind) — Learnings

Operational learnings from the era-3 campaign. Append new entries; keep each concrete and actionable.

---

## Production model: feed the source factory, then buy its output

**The model (Admiral, 2026-07-13).** Gate/construction materials such as `FAB_MATS` and `ADVANCED_CIRCUITRY` are the **EXPORT of a source factory** (e.g. `FAB_MATS` @ `X1-VB74-F48`, `ADVANCED_CIRCUITRY` @ `X1-VB74-D42`).

- We **always BUY the output** from that factory and haul it to the gate. We do **not** manufacture the final good ourselves.
- Buying *alone* depletes the factory's export supply → the bid climbs with every purchase → the buy-ceiling guard eventually trips → the fill stalls. **This is why a gate cannot be filled by pure market-buying at scale** (e.g. ~1520 FAB_MATS + ~360 ADV_CIRC in one era).
- **"Producing" = feeding the source factory its raw-material INPUTS.** Buy the goods the factory *imports* (elsewhere), deliver/sell them to the factory, so it keeps producing its export and its supply/price stay healthy — **which keeps our buying of the output affordable.**

**So sustaining a gate fill is a *feed-the-factory-inputs* loop running in parallel with buying the output** — NOT "produce instead of buy." The correct lever is keeping the EXPORT factory's imports stocked, then continuing to buy its export.

**Anti-pattern to avoid:** framing the fix as "fabricate the final good ourselves instead of buying it." We buy the output either way; the win is sustaining the source factory's export supply so the buy price doesn't explode.

**Related work:** sp-hoc6 (make the construction supply op feed F48/D42 their imports while buying the outputs). Depends on the correct code mechanics (whether the fabricate path feeds-inputs-then-buys vs harvests) — under validation.

---

## Construction gate-fill regression (sp-jav2) — capability was deleted, not missing

Last-era construction ran on a legacy parallel manufacturing coordinator that had: multiple concurrent hulls, production/fabrication drive, and continuous worklist refill. **sp-jav2 (`ef2281b8`) deleted that entire subpackage (41 files) as collateral** when retiring the "second coordinator"; the sp-382j thin-drain rebuilt only a single-hull, buy-only, one-shot subset. The gate-fill fixes are therefore **restores of deleted code** (recoverable at `ef2281b8^`), re-integrated into the thin drain (no second coordinator — Admiral veto):

- **Continuous refill** — sp-utjr (merged `dd40d725`, live): drain re-stages `DELIVER_TO_CONSTRUCTION` until each material's full bill is met.
- **Fabrication execution** — sp-qmp8 (merged `69fe694e`, live): drain can run `ProduceGood(Fabricate)`; but the *planner still prefers BUY* for buyable goods, so it isn't triggered for FAB_MATS/ADV_CIRC (→ sp-hoc6 / the production model above).
- **Parallel hulls** — sp-01eh (in progress): the drain was single-worker *by code* (serial loop + vestigial `max_workers`); the factory op, by contrast, was already parallel.
- **Full-load per trip** — sp-2me2 (queued): drain buys ~1 trade-volume (~20u)/trip, not toward hull capacity.

**Caveat:** the repo shows no evidence a gate was ever driven to 100% last era (one recorded era stuck at 0%). The *capability* regression is certain; a proven completed-gate outcome is not.

---

## Delegation / worktree hygiene

- Harness worktree isolation is **not a filesystem write-sandbox** — agents can still write to the main checkout via absolute paths. Keep the main-checkout path **out of the agent's brief entirely** (relative paths only) and have the **orchestrator run captain-gate**, so the agent never has a reason to touch main.
- Never run two lanes on the **same file** concurrently (only true corruption path); serialize same-file lanes, allow concurrency across different files. Verify the main working tree is pristine after every lane.
- **Validate capabilities against the code before filing "build X" beads** — a live symptom ("single-hull", "no factory") may be an existing-but-buggy capability or a deleted one (regression), not a missing feature.
