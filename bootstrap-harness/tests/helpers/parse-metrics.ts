// Minimal Prometheus text-exposition reader for test assertions (one metric at a time).
export function parseMetric(
  text: string,
  name: string,
  labels?: Record<string, string>,
): number | null {
  const wantLabels = labels ?? {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) continue;
    const brace = line.indexOf('{');
    const metricName = brace === -1 ? line.split(/\s+/)[0] : line.slice(0, brace);
    if (metricName !== name) continue;

    const lineLabels: Record<string, string> = {};
    if (brace !== -1) {
      const end = line.indexOf('}');
      const body = line.slice(brace + 1, end);
      for (const pair of body.split(',')) {
        if (!pair) continue;
        const eq = pair.indexOf('=');
        const k = pair.slice(0, eq).trim();
        const v = pair.slice(eq + 1).trim().replace(/^"|"$/g, '');
        lineLabels[k] = v;
      }
    }
    const match = Object.entries(wantLabels).every(([k, v]) => lineLabels[k] === v);
    if (!match) continue;
    const value = line.slice(line.lastIndexOf(' ') + 1);
    const n = Number(value);
    return Number.isNaN(n) ? null : n;
  }
  return null;
}
