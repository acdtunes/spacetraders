import { describe, expect, it } from 'vitest';
import { parseMetric } from '../helpers/parse-metrics';

const SAMPLE = `
# HELP spacetraders_daemon_bootstrap_probes_total Probes bought
# TYPE spacetraders_daemon_bootstrap_probes_total counter
spacetraders_daemon_bootstrap_probes_total 3
# TYPE spacetraders_daemon_bootstrap_phase gauge
spacetraders_daemon_bootstrap_phase{phase="DATA"} 1
spacetraders_daemon_bootstrap_phase{phase="INCOME"} 0
`;

describe('parseMetric', () => {
  it('reads an unlabeled counter', () => {
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_probes_total')).toBe(3);
  });
  it('reads a labeled gauge series', () => {
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'DATA' })).toBe(1);
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
  });
  it('returns null when absent', () => {
    expect(parseMetric(SAMPLE, 'nope')).toBeNull();
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBeNull();
  });
});
