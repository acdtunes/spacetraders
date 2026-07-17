export interface PgClientLike {
  query: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }>;
}

export type FetchSystemXY = (symbol: string) => Promise<{ x: number; y: number } | null>;

// Latest era id, or 0 when the eras table is empty/malformed.
export async function currentEraId(client: PgClientLike): Promise<number> {
  const r = await client.query(`SELECT era_id FROM eras ORDER BY era_id DESC LIMIT 1`);
  return Number(r.rows[0]?.era_id) || 0;
}

// Rate-limit hardening for the lazy live-API fill. The first build after a
// deploy or era reset misses the ENTIRE gate graph (~130+ systems); serially
// awaiting one GET per system under the API's ~2 req/s limit blocked
// /api/flows/topology for minutes while holding a pg pool client. Bounds:
// - MAX_FETCHES_PER_CALL: at most this many live GETs per invocation. The
//   overflow stays absent this cycle — the caller force-places it — and each
//   subsequent topology cache rebuild (~5 min) anchors another batch.
// - FETCH_CONCURRENCY: worker-pool width over the budgeted batch.
// - inFlightFetches: module-level (eraId:symbol)-keyed promise dedup so a
//   concurrent second tab / reload shares fetches instead of doubling 429
//   pressure. Entries clear on settle, so a failed fetch can retry on the
//   next cache miss.
export const FETCH_CONCURRENCY = 4;
export const MAX_FETCHES_PER_CALL = 24;

const inFlightFetches = new Map<string, Promise<{ x: number; y: number } | null>>();

// Real galaxy coordinates for `systems`: read the era-scoped snapshot, then
// lazily fetch + upsert missing systems from the live API (bounded per call,
// see above; this only runs on topology cache misses, ~once per 5 minutes).
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

  const budget = systems.filter((s) => !real.has(s)).slice(0, MAX_FETCHES_PER_CALL);
  let next = 0;
  const worker = async () => {
    while (next < budget.length) {
      const symbol = budget[next++];
      try {
        const key = `${eraId}:${symbol}`;
        let pending = inFlightFetches.get(key);
        if (!pending) {
          pending = Promise.resolve()
            .then(() => fetchSystemXY(symbol))
            .finally(() => inFlightFetches.delete(key));
          inFlightFetches.set(key, pending);
        }
        const xy = await pending;
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
  };
  await Promise.all(
    Array.from({ length: Math.min(FETCH_CONCURRENCY, budget.length) }, () => worker()),
  );
  return real;
}
