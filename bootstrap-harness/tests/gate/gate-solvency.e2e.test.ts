import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateLowCredits } from '../helpers/fixtures-gate';
import { countCall } from '../helpers/mutation-log';

// ─── GWT ────────────────────────────────────────────────────────────────────────────
// GIVEN a GATE-entry world whose treasury (400k) sits where a gate-delivery buy would drop it BELOW the
//       SHARED working-capital floor (max(50k, 40%×treasury) = 160k; 400k − 300k = 100k < 160k),
// WHEN  the coordinator wants to buy the gate-delivery fleet (Option B — the WHOLE fleet is bought from
//       contract income),
// THEN  every fleet buy is PAUSED while it would breach the floor — the contract fleet stays intact and
//       earning (nothing repurposed) — and the buys RESUME to the full manifest-derived complement (D=3)
//       once the treasury refills, exactly as contract income would refill it.
//
// TEETH: this is a REAL /v2 solvency test, not a flag. The twin DEBITS credits on every PurchaseShip
//   (routes/ships.ts) — so a fleet buy really drains the treasury — and the daemon's solvency gate
//   (blocker gate_worker_capital_gate) refuses the buy BEFORE it fires whenever treasury − price would
//   fall under the shared floor. The floor is the SAME primitive the material engine enforces at a factory
//   input buy (common.EffectiveReserveFloor), so fleet and materials can't starve each other.
//
// SCOPE: FLEET buys only. Materials are MANUFACTURED and handed to the site via construction-supply, which
//   the twin serves WITHOUT any credit debit (routes/construction.ts) — so material funding has no solvency
//   teeth to assert here; that path is out of scope by construction.
describe('bootstrap GATE — fleet-buy solvency floor', () => {
  it('pauses gate-fleet buys at the shared reserve floor, keeps the contract fleet intact, resumes when funded', async () => {
    await withGateScenario(gateLowCredits(), async (ctx) => {
      ctx.launchBootstrap();

      // The sizer must be LIVE and WANTING to buy: construction started + executor adopted, so desired=D>0
      // and the only thing standing between the coordinator and a buy is the solvency gate.
      await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted,
        { steps: 80, advanceMs: 1000 },
      );

      // ── PHASE 1 — PAUSED at the floor ──────────────────────────────────────────────
      // At 400k treasury a 300k buy would leave 100k < the 160k floor, so NO fleet hull is bought however
      // long we run. Falsifiable: with the solvency gate removed the daemon would buy here (PurchaseShip>0)
      // and the twin's debit would drop credits — so PurchaseShip===0 AND credits untouched is real teeth.
      await ctx.advanceTicks(20, 1000);
      let s = await ctx.twin.gateState();
      expect(countCall(s.mutationLog, 'PurchaseShip'), 'no fleet hull bought while a buy would breach the floor').toBe(0);
      expect(s.gateWorkers.length).toBe(0);
      expect(s.agent.credits).toBe(400_000); // treasury untouched — the paused buy never drained it
      // Contract fleet INTACT: Option B never converts an earner into a worker.
      expect(s.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0);

      // ── PHASE 2 — RESUME once funded ───────────────────────────────────────────────
      // Refill the treasury (as accumulating contract income would). Now treasury − price clears the floor,
      // so the paused buys proceed to the full manifest-derived complement D = min(2 chains + 1 delivery, 6) = 3.
      await ctx.twin.setCredits(3_000_000);
      s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.gateWorkers.length >= 3,
        { steps: 120, advanceMs: 1000 },
      );
      expect(s.gateWorkers.length).toBe(3);                                   // whole D=3 fleet now sized
      expect(s.gateWorkers.every((w) => w.source === 'bought')).toBe(true);   // ALL bought from income
      expect(s.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0); // still nothing repurposed
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(3);               // /v2 truth: exactly D real buys
    });
  }, 360_000);
});
