import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));
vi.mock('../../src/client.js', () => ({
  SpaceTradersClient: class { get() { return Promise.resolve({ data: {} }); } },
}));

const realFetch = global.fetch;

async function makeApp() {
  const { default: flowsRouter } = await import('../flows.js');
  const app = express();
  app.use('/api/flows', flowsRouter);
  return app;
}

beforeEach(() => { connect.mockReset(); vi.resetModules(); });
afterEach(() => { global.fetch = realFetch; });

function stubDaemon(flows: any[]) {
  global.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ flows }) }) as any;
}

const baseFlow = (containerId: string, ship: string) => ({
  containerId, program: 'tour', ship, tourId: containerId, closed: false,
  currentLeg: null, cargo: [], remainingHops: [], projected: { profit: 100000, ratePerHour: 40000 },
  plannedAt: '2026-07-17T09:00:00Z',
});

const shipRow = (ship_symbol: string) => ({
  ship_symbol, nav_status: 'IN_TRANSIT', system_symbol: 'X1-NK36', location_symbol: 'X1-NK36-I52',
  location_x: '12', location_y: '-7', arrival_time: '2026-07-17T10:05:00Z',
  origin_symbol: 'X1-NK36-A1', origin_x: '3', origin_y: '4', departure_time: '2026-07-17T10:00:00Z',
  cargo_capacity: '120',
});

describe('GET /api/flows/live — transit columns + realized', () => {
  it('joins origin/departure transit columns into shipNav', async () => {
    stubDaemon([baseFlow('tour-1', 'SHIP-1')]);
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [shipRow('SHIP-1')] }) // ships join
      .mockResolvedValueOnce({ rows: [] });                  // realized sums
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    expect(res.status).toBe(200);
    const nav = res.body.flows[0].shipNav;
    expect(nav).toMatchObject({
      originSymbol: 'X1-NK36-A1', originX: 3, originY: 4,
      departureTime: '2026-07-17T10:00:00.000Z',
      cargoCapacity: 120,
    });
    const shipSql = query.mock.calls[0][0] as string;
    expect(shipSql).toMatch(/origin_symbol/);
    expect(shipSql).toMatch(/departure_time/);
    expect(shipSql).toMatch(/cargo_capacity/);
  });

  it('folds signed transaction sums into realized per flow (0/null when no rows)', async () => {
    stubDaemon([baseFlow('tour-1', 'SHIP-1'), baseFlow('tour-2', 'SHIP-2')]);
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [shipRow('SHIP-1'), shipRow('SHIP-2')] })
      .mockResolvedValueOnce({ rows: [{ cid: 'tour-1', net: '-42000', last_event_at: '2026-07-17T10:00:00Z' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    const f1 = res.body.flows.find((f: any) => f.containerId === 'tour-1');
    expect(f1.realized).toEqual({ net: -42000, lastEventAt: '2026-07-17T10:00:00.000Z' });
    const f2 = res.body.flows.find((f: any) => f.containerId === 'tour-2');
    expect(f2.realized).toEqual({ net: 0, lastEventAt: null });
    const realizedSql = query.mock.calls[1][0] as string;
    expect(realizedSql).toMatch(/related_entity_type\s*=\s*'container'/);
    expect(realizedSql).toMatch(/SUM\(amount\)/i);
  });

  it('feed lost: no PG round-trips at all', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error('daemon down')) as any;
    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');
    expect(res.status).toBe(200);
    expect(res.body.feedLost).toBe(true);
    expect(connect).not.toHaveBeenCalled();
  });
});
