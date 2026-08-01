import { describe, expect, it } from 'vitest';
import { twinGate } from './helpers/twin-admin-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('GATE admin client (live stack)', () => {
  it('seedGate → gateState round-trips a GATE-entry world', async () => {
    await twinGate.seedGate(gateEntry({ constructionPercent: 0 }));
    const s = await twinGate.gateState();
    expect(s.construction.site).toBeTruthy();
    expect(s.construction.percent).toBe(0);
    expect(s.construction.started).toBe(false);
    expect(s.done).toBe(false);
  });
  it('setConstruction advances the percent', async () => {
    await twinGate.setConstruction(100);
    expect((await twinGate.gateState()).construction.percent).toBeGreaterThanOrEqual(100);
  });
});
