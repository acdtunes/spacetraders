export interface LayoutNode {
  symbol: string;
  x: number;
  y: number;
}

export interface LayoutEdge {
  from: string;
  to: string;
}

// FNV-1a — a stable, dependency-free hash so initial placement is seeded by the
// symbol (deterministic across processes; no Math.random anywhere in here).
function hashSymbol(symbol: string): number {
  let h = 2166136261;
  for (let i = 0; i < symbol.length; i++) {
    h ^= symbol.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// Deterministic Fruchterman-Reingold-style layout of the gate graph. Nodes start
// on a hash-jittered ring and relax under edge attraction + all-pairs repulsion.
export function computeGalaxyLayout(
  systems: string[],
  edges: LayoutEdge[],
  opts: { radius?: number; iterations?: number } = {},
): LayoutNode[] {
  const radius = opts.radius ?? 1000;
  const iterations = opts.iterations ?? 200;
  const sorted = [...new Set(systems)].sort();
  const n = sorted.length;
  if (n === 0) return [];

  const pos = new Map<string, { x: number; y: number }>();
  sorted.forEach((sym, i) => {
    const base = (i / n) * Math.PI * 2;
    const h = hashSymbol(sym);
    const jitter = ((h % 1000) / 1000 - 0.5) * (Math.PI / n);
    const r = radius * (0.6 + ((h >>> 10) % 1000) / 1000 * 0.4);
    pos.set(sym, { x: Math.cos(base + jitter) * r, y: Math.sin(base + jitter) * r });
  });

  const known = new Set(sorted);
  const realEdges = edges.filter((e) => e.from !== e.to && known.has(e.from) && known.has(e.to));
  const k = radius / Math.sqrt(n); // ideal spacing

  for (let iter = 0; iter < iterations; iter++) {
    const disp = new Map<string, { x: number; y: number }>();
    sorted.forEach((s) => disp.set(s, { x: 0, y: 0 }));

    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const a = pos.get(sorted[i])!;
        const b = pos.get(sorted[j])!;
        const dx = a.x - b.x;
        const dy = a.y - b.y;
        const dist = Math.hypot(dx, dy) || 0.01;
        const force = (k * k) / dist;
        const fx = (dx / dist) * force;
        const fy = (dy / dist) * force;
        const da = disp.get(sorted[i])!;
        const db = disp.get(sorted[j])!;
        da.x += fx; da.y += fy;
        db.x -= fx; db.y -= fy;
      }
    }

    for (const e of realEdges) {
      const a = pos.get(e.from)!;
      const b = pos.get(e.to)!;
      const dx = a.x - b.x;
      const dy = a.y - b.y;
      const dist = Math.hypot(dx, dy) || 0.01;
      const force = (dist * dist) / k;
      const fx = (dx / dist) * force;
      const fy = (dy / dist) * force;
      disp.get(e.from)!.x -= fx; disp.get(e.from)!.y -= fy;
      disp.get(e.to)!.x += fx; disp.get(e.to)!.y += fy;
    }

    const temp = radius * (1 - iter / iterations) * 0.1;
    for (const s of sorted) {
      const d = disp.get(s)!;
      const p = pos.get(s)!;
      const dl = Math.hypot(d.x, d.y) || 0.01;
      p.x += (d.x / dl) * Math.min(dl, temp);
      p.y += (d.y / dl) * Math.min(dl, temp);
    }
  }

  return sorted.map((s) => ({ symbol: s, x: Math.round(pos.get(s)!.x), y: Math.round(pos.get(s)!.y) }));
}

export type AnchoredNode = LayoutNode & { layout: 'real' | 'force' };

// Place systems with real coordinates verbatim; anchor each unknown at the
// centroid of its gate neighbours' REAL positions (deterministic hash jitter
// so siblings sharing a neighbour set don't stack), or on a hash ring outside
// the real spread when no neighbour is known. All-unknown degenerates to the
// classic force layout.
export function layoutWithAnchors(
  real: Map<string, { x: number; y: number }>,
  systems: string[],
  edges: LayoutEdge[],
): AnchoredNode[] {
  const sorted = [...new Set(systems)].sort();
  if (real.size === 0) {
    return computeGalaxyLayout(sorted, edges).map((n) => ({ ...n, layout: 'force' as const }));
  }

  const neighbours = new Map<string, string[]>();
  const push = (k: string, v: string) => {
    const arr = neighbours.get(k);
    if (arr) arr.push(v);
    else neighbours.set(k, [v]);
  };
  for (const e of edges) {
    if (e.from === e.to) continue;
    push(e.from, e.to);
    push(e.to, e.from);
  }

  const reals = [...real.values()];
  const cx = reals.reduce((s, p) => s + p.x, 0) / reals.length;
  const cy = reals.reduce((s, p) => s + p.y, 0) / reals.length;
  let spread = 0;
  for (const p of reals) spread = Math.max(spread, Math.hypot(p.x - cx, p.y - cy));
  if (spread === 0) spread = 1000;

  return sorted.map((sym) => {
    const r = real.get(sym);
    if (r) return { symbol: sym, x: Math.round(r.x), y: Math.round(r.y), layout: 'real' as const };
    const h = hashSymbol(sym);
    const angle = ((h % 360) / 360) * Math.PI * 2;
    const anchored = (neighbours.get(sym) ?? [])
      .map((n) => real.get(n))
      .filter((p): p is { x: number; y: number } => Boolean(p));
    if (anchored.length > 0) {
      const ax = anchored.reduce((s, p) => s + p.x, 0) / anchored.length;
      const ay = anchored.reduce((s, p) => s + p.y, 0) / anchored.length;
      const jr = spread * 0.06 + ((h >>> 9) % 100);
      return { symbol: sym, x: Math.round(ax + Math.cos(angle) * jr), y: Math.round(ay + Math.sin(angle) * jr), layout: 'force' as const };
    }
    const ring = spread * 1.15 + ((h >>> 9) % 200);
    return { symbol: sym, x: Math.round(cx + Math.cos(angle) * ring), y: Math.round(cy + Math.sin(angle) * ring), layout: 'force' as const };
  });
}
