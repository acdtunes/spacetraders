import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { twin } from '../helpers/twin-admin';
import { coldStart } from '../helpers/fixtures';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, advanceTicks } from '../helpers/drive';
import { HARNESS_ROOT } from '../helpers/config';

const DISABLED_CONFIG = path.join(HARNESS_ROOT, 'tests', 'fixtures', 'test-config.disabled.yaml');

describe('bootstrap DATA — disabled escape', () => {
  it('no-ops every tick when bootstrap_disabled=true', async () => {
    await twin.reset(coldStart({ probePrice: 40000 }));
    await resetDaemonDb();
    const prev = process.env.SPACETRADERS_CONFIG;
    process.env.SPACETRADERS_CONFIG = DISABLED_CONFIG;
    const daemon = await startTestDaemon();
    try {
      launchBootstrap();
      await advanceTicks(8, 1000);
      const s = await twin.state();
      expect(s.mutationLog).toEqual([]); // disabled → no acting
    } finally {
      await daemon.stop();
      if (prev === undefined) delete process.env.SPACETRADERS_CONFIG; else process.env.SPACETRADERS_CONFIG = prev;
    }
  }, 120_000);
});
