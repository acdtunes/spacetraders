export interface PgClientLike {
  query: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }>;
}

export type FetchSystemXY = (symbol: string) => Promise<{ x: number; y: number } | null>;

// Latest era id, or 0 when the eras table is empty/malformed.
export async function currentEraId(client: PgClientLike): Promise<number> {
  const r = await client.query(`SELECT era_id FROM eras ORDER BY era_id DESC LIMIT 1`);
  return Number(r.rows[0]?.era_id) || 0;
}

// Real galaxy coordinates for `systems`: read the era-scoped snapshot, then
// lazily fetch + upsert any missing system from the live API (one GET per
// unknown; this only runs on topology cache misses, ~once per 5 minutes).
// A system the API cannot supply stays absent — the caller force-places it.
// Fetch/upsert failures are per-system and non-fatal.
export async function resolveSystemCoords(
  client: PgClientLike,
  fetchSystemXY: FetchSystemXY,
  systems: string[],
  eraId: number,
): Promise<Map<string, { x: number; y: number }>> {
  const real = new Map<string, { x: number; y: number }>();
  if (systems.length === 0) return real;

  const known = await client.query(
    `SELECT symbol, x, y FROM system_coords WHERE era_id = $1 AND symbol = ANY($2)`,
    [eraId, systems],
  );
  for (const row of known.rows) {
    const x = Number(row?.x);
    const y = Number(row?.y);
    if (typeof row?.symbol === 'string' && Number.isFinite(x) && Number.isFinite(y)) {
      real.set(row.symbol, { x, y });
    }
  }

  for (const symbol of systems.filter((s) => !real.has(s))) {
    try {
      const xy = await fetchSystemXY(symbol);
      if (!xy) continue;
      await client.query(
        `INSERT INTO system_coords (era_id, symbol, x, y, fetched_at)
         VALUES ($1, $2, $3, $4, $5)
         ON CONFLICT (era_id, symbol)
         DO UPDATE SET x = EXCLUDED.x, y = EXCLUDED.y, fetched_at = EXCLUDED.fetched_at`,
        [eraId, symbol, xy.x, xy.y, new Date().toISOString()],
      );
      real.set(symbol, xy);
    } catch {
      // best effort per system; anchored force placement covers it this cycle
    }
  }
  return real;
}
