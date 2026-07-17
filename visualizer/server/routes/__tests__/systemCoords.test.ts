import { describe, it, expect, vi } from 'vitest';
import {
  currentEraId,
  resolveSystemCoords,
  FETCH_CONCURRENCY,
  MAX_FETCHES_PER_CALL,
} from '../../utils/systemCoords.js';

const client = (impl: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }>) => ({ query: vi.fn(impl) });

describe('currentEraId', () => {
  it('returns the latest era id', async () => {
    const c = client(async () => ({ rows: [{ era_id: 7 }] }));
    expect(await currentEraId(c)).toBe(7);
  });
  it('returns 0 when eras is empty or malformed', async () => {
    expect(await currentEraId(client(async () => ({ rows: [] })))).toBe(0);
    expect(await currentEraId(client(async () => ({ rows: [{ nope: 1 }] })))).toBe(0);
  });
});

describe('resolveSystemCoords', () => {
  it('returns known rows and skips the live API for them', async () => {
    const c = client(async (sql) => {
      if (/FROM system_coords/.test(sql)) return { rows: [{ symbol: 'X1-AA', x: '10', y: '-20' }] };
      return { rows: [] };
    });
    const fetcher = vi.fn();
    const real = await resolveSystemCoords(c, fetcher, ['X1-AA'], 7);
    expect(real.get('X1-AA')).toEqual({ x: 10, y: -20 });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('lazily fetches + upserts missing systems', async () => {
    const calls: { sql: string; params?: unknown[] }[] = [];
    const c = {
      query: vi.fn(async (sql: string, params?: unknown[]) => {
        calls.push({ sql, params });
        if (/FROM system_coords/.test(sql)) return { rows: [] };
        return { rows: [] }; // INSERT
      }),
    };
    const fetcher = vi.fn(async () => ({ x: 33, y: 44 }));
    const real = await resolveSystemCoords(c, fetcher, ['X1-BB'], 7);
    expect(real.get('X1-BB')).toEqual({ x: 33, y: 44 });
    const insert = calls.find((c2) => /INSERT INTO system_coords/.test(c2.sql));
    expect(insert).toBeTruthy();
    expect(insert!.params!.slice(0, 4)).toEqual([7, 'X1-BB', 33, 44]);
    expect(insert!.sql).toMatch(/ON CONFLICT \(era_id, symbol\)/);
  });

  it('leaves a system absent when the live API returns null or throws', async () => {
    const c = client(async (sql) => (/FROM system_coords/.test(sql) ? { rows: [] } : { rows: [] }));
    const fetcher = vi.fn(async (sym: string) => {
      if (sym === 'X1-CC') return null;
      throw new Error('api down');
    });
    const real = await resolveSystemCoords(c, fetcher, ['X1-CC', 'X1-DD'], 7);
    expect(real.size).toBe(0);
  });

  it('ignores malformed snapshot rows (garbage never poisons the map)', async () => {
    const c = client(async (sql) =>
      /FROM system_coords/.test(sql) ? { rows: [{ symbol: undefined, x: 'nope', y: null }] } : { rows: [] },
    );
    const real = await resolveSystemCoords(c, vi.fn(async () => null), ['X1-EE'], 7);
    expect(real.size).toBe(0);
  });
});

// Regression: the first build after deploy/era reset misses the whole gate
// graph (~130+ systems). Serially awaiting one live GET per system under the
// API's ~2 req/s limit blocked /api/flows/topology for minutes (holding a pg
// client), and a second tab started a duplicate fetch storm.
describe('resolveSystemCoords rate-limit hardening', () => {
  const emptySnapshotClient = () =>
    client(async (sql) => (/FROM system_coords/.test(sql) ? { rows: [] } : { rows: [] }));

  it('caps live fetches per call; overflow stays absent for force placement', async () => {
    const fetcher = vi.fn(async (sym: string) => ({ x: 1, y: 2 }));
    const symbols = Array.from({ length: MAX_FETCHES_PER_CALL + 10 }, (_, i) => `X1-B${i}`);
    const real = await resolveSystemCoords(emptySnapshotClient(), fetcher, symbols, 7);
    expect(fetcher).toHaveBeenCalledTimes(MAX_FETCHES_PER_CALL);
    expect(real.size).toBe(MAX_FETCHES_PER_CALL);
  });

  it('runs fetches through a bounded worker pool (concurrent, capped, not serial)', async () => {
    let active = 0;
    let maxActive = 0;
    const fetcher = vi.fn(async () => {
      active++;
      maxActive = Math.max(maxActive, active);
      await new Promise((r) => setTimeout(r, 5));
      active--;
      return { x: 1, y: 2 };
    });
    const symbols = Array.from({ length: 12 }, (_, i) => `X1-P${i}`);
    const real = await resolveSystemCoords(emptySnapshotClient(), fetcher, symbols, 7);
    expect(real.size).toBe(12);
    expect(maxActive).toBeGreaterThan(1); // not serial
    expect(maxActive).toBeLessThanOrEqual(FETCH_CONCURRENCY); // capped
  });

  it('dedupes concurrent in-flight fetches for the same system (second-tab storm)', async () => {
    let resolveFetch!: (v: { x: number; y: number } | null) => void;
    const fetcher = vi.fn(
      () => new Promise<{ x: number; y: number } | null>((r) => { resolveFetch = r; }),
    );
    const a = resolveSystemCoords(emptySnapshotClient(), fetcher, ['X1-DUP'], 7);
    const b = resolveSystemCoords(emptySnapshotClient(), fetcher, ['X1-DUP'], 7);
    await new Promise((r) => setTimeout(r, 0)); // both calls reach the fetch stage
    resolveFetch({ x: 9, y: 8 });
    const [ra, rb] = await Promise.all([a, b]);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(ra.get('X1-DUP')).toEqual({ x: 9, y: 8 });
    expect(rb.get('X1-DUP')).toEqual({ x: 9, y: 8 });
  });

  it('clears the in-flight entry after settle so a later cache miss retries', async () => {
    const fetcher = vi.fn(async () => null);
    await resolveSystemCoords(emptySnapshotClient(), fetcher, ['X1-RE'], 7);
    await resolveSystemCoords(emptySnapshotClient(), fetcher, ['X1-RE'], 7);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('a throwing fetch inside the pool stays per-system (others still resolve)', async () => {
    const fetcher = vi.fn(async (sym: string) => {
      if (sym === 'X1-BAD') throw new Error('429 storm');
      return { x: 3, y: 4 };
    });
    const real = await resolveSystemCoords(emptySnapshotClient(), fetcher, ['X1-BAD', 'X1-OK'], 7);
    expect(real.has('X1-BAD')).toBe(false);
    expect(real.get('X1-OK')).toEqual({ x: 3, y: 4 });
  });
});
