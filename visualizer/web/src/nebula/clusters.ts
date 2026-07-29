export interface Cluster { id: string; members: string[]; cx: number; cy: number; isHome: boolean }
const MAX_CLUSTER = 8;

interface TopoLike {
  systems: { symbol: string; x: number; y: number }[];
  edges: { from: string; to: string }[];
  homeSystem: string | null;
}

// Deterministic greedy BFS over the gate graph: iterate symbols sorted, seed a
// cluster from each unassigned symbol, absorb sorted-neighbor frontier up to
// MAX_CLUSTER. Sorted iteration everywhere ⇒ same topology → same clusters.
export function clustersFor(topo: TopoLike): Cluster[] {
  const pos = new Map(topo.systems.map(s => [s.symbol, s]));
  const adj = new Map<string, string[]>();
  for (const s of topo.systems) adj.set(s.symbol, []);
  for (const e of topo.edges) {
    if (e.from === e.to || !adj.has(e.from) || !adj.has(e.to)) continue;
    adj.get(e.from)!.push(e.to); adj.get(e.to)!.push(e.from);
  }
  for (const [, ns] of adj) ns.sort();

  const assigned = new Set<string>();
  const clusters: Cluster[] = [];
  for (const seed of [...pos.keys()].sort()) {
    if (assigned.has(seed)) continue;
    const members: string[] = [];
    const queue = [seed];
    while (queue.length && members.length < MAX_CLUSTER) {
      const cur = queue.shift()!;
      if (assigned.has(cur)) continue;
      assigned.add(cur); members.push(cur);
      for (const n of adj.get(cur) ?? []) if (!assigned.has(n)) queue.push(n);
    }
    members.sort();
    const cx = members.reduce((s, m) => s + pos.get(m)!.x, 0) / members.length;
    const cy = members.reduce((s, m) => s + pos.get(m)!.y, 0) / members.length;
    clusters.push({ id: members[0], members, cx, cy, isHome: topo.homeSystem != null && members.includes(topo.homeSystem) });
  }
  return clusters;
}
