// ─── /_twin/fault in-memory queue ─────────────────────────────────────────────────────
// POST /_twin/fault arms the next `count` requests matching "METHOD /path" (path relative
// to /v2, no query string) to return HTTP `code`; the Nth matching request self-clears the
// arm. Checked by the preHandler registered on the /v2 plugin (server.ts) — /_twin itself
// is a separate top-level registration and is never faulted. Module-level singleton (mirrors
// clock.ts / world/store.ts): one twin process, one queue, independent of the World object
// (so it survives a POST /_twin/reset unless explicitly cleared — see resetFaults below).

interface ArmedFault {
  code: number;
  remaining: number;
}

const armed = new Map<string, ArmedFault>();

/** "get", " /my/ships/ " -> "GET /my/ships": upper-case method, force a leading slash, drop
 *  a trailing slash (except root) so arm-time and request-time keys always agree. */
function normalizeKey(method: string, path: string): string {
  const m = method.trim().toUpperCase();
  let p = path.trim();
  if (!p.startsWith('/')) p = `/${p}`;
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1);
  return `${m} ${p}`;
}

/** POST /_twin/fault: arm `count` (>=1) upcoming requests to `method`+`path` to return
 *  `code`. Re-arming the same endpoint overwrites any still-armed count (last write wins). */
export function armFault(method: string, path: string, code: number, count: number): void {
  armed.set(normalizeKey(method, path), { code, remaining: Math.trunc(count) });
}

/** Checked by the /v2 preHandler for every request. Decrements the remaining count and
 *  deletes the arm once exhausted (self-clear). Returns the HTTP code to send, or null when
 *  nothing is armed for this method+path. */
export function consumeFault(method: string, path: string): number | null {
  const key = normalizeKey(method, path);
  const entry = armed.get(key);
  if (!entry) return null;
  entry.remaining -= 1;
  if (entry.remaining <= 0) armed.delete(key);
  return entry.code;
}

/** Clear every armed fault. Called by POST /_twin/reset so a fresh scenario never inherits a
 *  stale arm left over from a previous one within the same long-lived twin process. */
export function resetFaults(): void {
  armed.clear();
}
