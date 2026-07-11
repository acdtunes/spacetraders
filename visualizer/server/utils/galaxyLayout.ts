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
