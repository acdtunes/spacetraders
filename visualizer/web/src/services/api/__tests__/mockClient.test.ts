import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { mockRequest } from '../mockClient';
import {
  DEMO_SIGNAL_LOSS_DURATION_MS,
  DEMO_SIGNAL_LOSS_PERIOD_MS,
  demoLatestEventId,
} from '../../../mocks/demoEvents';
import type { FleetEvent, GateProgress } from '../../../types/spacetraders';

// A clock exactly on a signal-loss period boundary (phase 0 -> healthy):
// 1_800_000_000_000 = 10_000_000 * 180_000, and also 8s-bucket aligned.
const HEALTHY_T = 1_800_000_000_000;
// The drill occupies the trailing DURATION of every PERIOD.
const DARK_T = HEALTHY_T + DEMO_SIGNAL_LOSS_PERIOD_MS - DEMO_SIGNAL_LOSS_DURATION_MS;
const RECOVERED_T = HEALTHY_T + DEMO_SIGNAL_LOSS_PERIOD_MS;

describe('mockRequest /bot/* demo handlers', () => {
  beforeEach(() => {
    // Fake timers give the test the Date.now() steering wheel AND keep the mock
    // scenario's behavior timeouts (if any start) from ever actually firing.
    vi.useFakeTimers();
    vi.setSystemTime(HEALTHY_T);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('GET /bot/events returns { events } newest-first and honors the after cursor', async () => {
    const { events } = await mockRequest<{ events: FleetEvent[] }>('/bot/events?limit=5');
    expect(events).toHaveLength(5);
    expect(events[0].id).toBe(demoLatestEventId(HEALTHY_T));
    for (let i = 1; i < events.length; i++) {
      expect(events[i].id).toBeLessThan(events[i - 1].id);
    }

    // Caught-up delta fetch: empty page, not an error.
    const caughtUp = await mockRequest<{ events: FleetEvent[] }>(
      `/bot/events?after=${events[0].id}&limit=50`
    );
    expect(caughtUp.events).toEqual([]);
  });

  it('GET /bot/construction/:wp returns the demo gate bill', async () => {
    const gate = await mockRequest<GateProgress>('/bot/construction/X1-PZ28-I67');
    expect(typeof gate.progress).toBe('number');
    expect(gate.progress).toBeGreaterThan(0);
    expect(gate.progress).toBeLessThanOrEqual(100);
    expect(gate.materials.length).toBeGreaterThan(0);
    for (const material of gate.materials) {
      expect(material.fulfilled).toBeGreaterThanOrEqual(0);
      expect(material.fulfilled).toBeLessThanOrEqual(material.required);
    }
  });

  it('resolves every other /bot/* dataset with an empty-but-valid envelope', async () => {
    const cases: Array<[string, unknown]> = [
      ['/bot/assignments', { assignments: [] }],
      ['/bot/daemons', { daemons: [] }],
      ['/bot/players', { players: [] }],
      ['/bot/operations/summary', { summary: [] }],
      ['/bot/markets/X1-DEMO', { markets: [] }],
      ['/bot/markets/X1-DEMO/freshness', { freshness: [] }],
      ['/bot/tours/X1-DEMO?player_id=1', { tours: [] }],
      ['/bot/trade-opportunities/X1-DEMO?limit=200', { opportunities: [] }],
      ['/bot/transactions/X1-DEMO?limit=100', { transactions: [] }],
    ];
    for (const [endpoint, expected] of cases) {
      await expect(mockRequest(endpoint)).resolves.toEqual(expected);
    }

    const { graph } = await mockRequest<{ graph: { system_symbol: string; graph_data: object } }>(
      '/bot/graph/X1-DEMO'
    );
    expect(graph.system_symbol).toBe('X1-DEMO');
    expect(graph.graph_data).toEqual({ nodes: {}, edges: {} });

    const ledger = await mockRequest<{ transactions: unknown[]; total: number }>(
      '/bot/ledger/transactions?player_id=1&limit=25'
    );
    expect(ledger).toMatchObject({ transactions: [], total: 0, page: 1, limit: 25 });

    const cashFlow = await mockRequest<{ summary: { net_cash_flow: number }; categories: unknown[] }>(
      '/bot/ledger/cash-flow?player_id=1'
    );
    expect(cashFlow.summary.net_cash_flow).toBe(0);
    expect(cashFlow.categories).toEqual([]);

    const pl = await mockRequest<{ net_profit: number }>('/bot/ledger/profit-loss?player_id=1');
    expect(pl.net_profit).toBe(0);

    const opPl = await mockRequest<{ operations: unknown[] }>(
      '/bot/ledger/profit-loss-by-operation?player_id=1'
    );
    expect(opPl.operations).toEqual([]);

    const balance = await mockRequest<{ dataPoints: unknown[] }>(
      '/bot/ledger/balance-history?player_id=1'
    );
    expect(balance.dataPoints).toEqual([]);

    // Unknown future bot endpoints succeed benignly instead of faking an outage.
    await expect(mockRequest('/bot/some-future-endpoint')).resolves.toEqual({});
  });

  it('goes dark for /bot/* only during the signal-loss window, then auto-recovers', async () => {
    // Healthy before the window.
    await expect(mockRequest('/bot/events?limit=1')).resolves.toBeTruthy();

    // Inside the drill: the whole bot namespace throws...
    vi.setSystemTime(DARK_T);
    await expect(mockRequest('/bot/events?limit=1')).rejects.toThrow(/signal-loss/i);
    await expect(mockRequest('/bot/assignments')).rejects.toThrow(/signal-loss/i);
    await expect(mockRequest('/bot/construction/X1-PZ28-I67')).rejects.toThrow(/signal-loss/i);
    // ...while non-bot mock endpoints stay healthy.
    await expect(mockRequest('/agents')).resolves.toBeTruthy();

    // Still dark at the last millisecond of the window.
    vi.setSystemTime(HEALTHY_T + DEMO_SIGNAL_LOSS_PERIOD_MS - 1);
    await expect(mockRequest('/bot/events?limit=1')).rejects.toThrow(/signal-loss/i);

    // The instant the window passes, the namespace recovers.
    vi.setSystemTime(RECOVERED_T);
    await expect(mockRequest('/bot/events?limit=1')).resolves.toBeTruthy();
  });
});
