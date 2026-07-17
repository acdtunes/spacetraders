import { describe, it, expect, vi } from 'vitest';
import { currentEraId, resolveSystemCoords } from '../../utils/systemCoords.js';

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
