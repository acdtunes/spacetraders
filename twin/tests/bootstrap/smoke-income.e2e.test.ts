import { describe, expect, it } from 'vitest';
import { twinIncome } from './helpers/twin-admin-income';
import { incomeEntry } from './helpers/fixtures-income';

describe('INCOME admin client (live stack)', () => {
  it('seedIncome → incomeState round-trips a post-DATA world', async () => {
    await twinIncome.seedIncome(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2'] }));
    const s = await twinIncome.incomeState();
    expect(s.frigateContractTagged).toBe(true);
    expect(s.hubs.length).toBeGreaterThanOrEqual(2);
    expect(s.creditsPerHour).toBe(0);
    expect(s.batchContractRunning).toBe(false);
  });
  it('setIncome moves realized $/hr', async () => {
    await twinIncome.setIncome(42000);
    expect((await twinIncome.incomeState()).creditsPerHour).toBeGreaterThanOrEqual(42000);
  });
});
