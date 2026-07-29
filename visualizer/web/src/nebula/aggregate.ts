// Pure aggregation of system→system lanes into cluster→cluster money currents
// for the GALAXY band. No pixi, no side effects.
import type { Cluster } from './clusters';
import type { SceneLane } from './sceneData';

export interface AggregateCurrent {
  fromCluster: string;
  toCluster: string;
  profitPerHr: number;
  volume: number;
}

/**
 * Sum lanes whose endpoints land in DIFFERENT clusters. Intra-cluster lanes
 * (and lanes touching a system outside every cluster) are excluded. Symmetric
 * pairs (A→B and B→A) merge under the canonical `min|max` key — from/to are the
 * sorted cluster ids, profit/volume simply sum, so a loss-making direction
 * keeps its sign in the net. Output order is deterministic: sorted by key.
 */
export function aggregateCurrents(clusters: Cluster[], lanes: SceneLane[]): AggregateCurrent[] {
  const clusterOf = new Map<string, string>();
  for (const c of clusters) for (const m of c.members) clusterOf.set(m, c.id);

  const merged = new Map<string, AggregateCurrent>();
  for (const lane of lanes) {
    const a = clusterOf.get(lane.from);
    const b = clusterOf.get(lane.to);
    if (a == null || b == null || a === b) continue;
    const [lo, hi] = a < b ? [a, b] : [b, a];
    const key = `${lo}|${hi}`;
    let cur = merged.get(key);
    if (cur == null) {
      cur = { fromCluster: lo, toCluster: hi, profitPerHr: 0, volume: 0 };
      merged.set(key, cur);
    }
    cur.profitPerHr += lane.profitPerHr;
    cur.volume += lane.volume;
  }

  return [...merged.keys()].sort().map((k) => merged.get(k)!);
}
