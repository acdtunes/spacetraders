// Elapsed mm:ss since an ISO timestamp; "—" when unknown.
export function formatElapsed(fromIso: string | null, nowMs: number): string {
  if (!fromIso) return '—';
  const then = Date.parse(fromIso);
  if (Number.isNaN(then)) return '—';
  const secs = Math.max(0, Math.floor((nowMs - then) / 1000));
  const mm = String(Math.floor(secs / 60)).padStart(2, '0');
  const ss = String(secs % 60).padStart(2, '0');
  return `${mm}:${ss}`;
}
