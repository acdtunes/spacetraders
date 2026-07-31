import { describe, it, expect } from 'vitest';
import { mockTopology, mockLanes, mockLiveFlows, mockFreshness } from '../../mocks/mockFlows';
import { buildSceneData } from '../sceneData';

// Exactly on a 10-minute wall-clock boundary: mockLiveFlows anchors its in-transit
// nav legs to epoch = floor(nowMs/600k)*600k, so epoch === NOW and every glide
// phase below is a pinned constant, not a function of the test run's clock.
//   A (TORWIND-3): outbound gate half, t = 90s/600s = 0.15 → edge phase 0.075 on NK36→KA42
//   B (TORWIND-7): dwelling in X1-KA42 (orbit radius 9 at scale 1)
//   C (TORWIND-54): true warp ZC66→UU57, t = 150s/600s = 0.25
//   D (TORWIND-11): parked pre-departure on ZC66→UU57 (phase 0.04), no projection
const NOW = 600_000_000;

const scene = () => buildSceneData(mockTopology, mockLanes('6h'), mockLiveFlows(NOW), NOW);

describe('buildSceneData systems', () => {
  it('maps every topology system in order with coords, home flag, construction flag', () => {
    const s = scene().systems;
    expect(s.map((x) => x.symbol)).toEqual(['X1-NK36', 'X1-KA42', 'X1-ZC66', 'X1-UU57']);
    expect(s[0]).toMatchObject({ x: -400, y: -120, isHome: true });
    expect(s[1]).toMatchObject({ x: 260, y: 40, isHome: false });
    // The one under-construction edge (UU57→NK36) names NK36's own gate, so
    // NK36 — and only NK36 — is flagged.
    expect(s.map((x) => x.underConstruction)).toEqual([true, false, false, false]);
  });

  it('sums activity from realized lane profit touching each system', () => {
    const byName = new Map(scene().systems.map((x) => [x.symbol, x.activity]));
    expect(byName.get('X1-NK36')).toBe(312000); // NK36→KA42
    expect(byName.get('X1-KA42')).toBe(453000); // 312000 + 141000
    expect(byName.get('X1-ZC66')).toBe(133000); // 141000 + (-8000)
    expect(byName.get('X1-UU57')).toBe(-8000); // the loss lane
  });
});

describe('buildSceneData lanes', () => {
  it('maps the directed system lanes 1:1 with realized/volume/profitPerHr', () => {
    const lanes = scene().lanes;
    expect(lanes).toHaveLength(3);
    expect(lanes[0]).toMatchObject({ from: 'X1-NK36', to: 'X1-KA42', realized: 312000, volume: 480, profitPerHr: 52000 });
    expect(lanes[1]).toMatchObject({ from: 'X1-KA42', to: 'X1-ZC66', realized: 141000, volume: 300, profitPerHr: 23500 });
    expect(lanes[2]).toMatchObject({ from: 'X1-ZC66', to: 'X1-UU57', realized: -8000, volume: 120 });
    expect(lanes[2].profitPerHr).toBeCloseTo(-8000 / 6, 6);
  });

  it('divides profitPerHr by the lane window hours', () => {
    // '1h' scales the fixture by 0.25; '24h' by 3.5 — both divide by their own hours.
    const oneHr = buildSceneData(mockTopology, mockLanes('1h'), undefined, NOW).lanes[0];
    expect(oneHr).toMatchObject({ realized: 78000, profitPerHr: 78000, volume: 120 });
    const dayLong = buildSceneData(mockTopology, mockLanes('24h'), undefined, NOW).lanes[0];
    expect(dayLong).toMatchObject({ realized: 1092000, profitPerHr: 45500, volume: 1680 });
  });

  it('attributes gliding flows projected profit onto their rendered lane', () => {
    const lanes = scene().lanes;
    expect(lanes[0].projected).toBe(250000); // A glides NK36→KA42
    expect(lanes[1].projected).toBe(0); // nobody flying KA42→ZC66
    expect(lanes[2].projected).toBe(60000); // C warps ZC66→UU57; D flies it too but has no projection
  });
});

describe('buildSceneData ships', () => {
  it('projects every mock flow to a finite position via flowMotion', () => {
    const ships = scene().ships;
    expect(ships.map((s) => s.id)).toEqual(['TORWIND-3', 'TORWIND-7', 'TORWIND-54', 'TORWIND-11']);
    expect(ships.map((s) => s.flowId)).toEqual([
      'tour-run-TORWIND-3-galaxyA',
      'tour-run-TORWIND-7-loopB',
      'arb-run-TORWIND-54-beba64e7',
      'tour-run-TORWIND-11-relocD',
    ]);
    for (const s of ships) {
      expect(Number.isFinite(s.x)).toBe(true);
      expect(Number.isFinite(s.y)).toBe(true);
      expect(Number.isFinite(s.headingRad)).toBe(true);
    }
  });

  it('pins glide positions to the fixture anchor window', () => {
    const ships = scene().ships;
    // A: NK36 (-400,-120) → KA42 (260,40) at edge phase 0.5*0.15 = 0.075.
    expect(ships[0].x).toBeCloseTo(-350.5, 9);
    expect(ships[0].y).toBeCloseTo(-108, 9);
    expect(ships[0].headingRad).toBeCloseTo(Math.atan2(160, 660), 9);
    expect(ships[0].system).toBe('X1-NK36');
    // C: true warp ZC66 (120,380) → UU57 (-260,280) at t = 0.25.
    expect(ships[2].x).toBeCloseTo(25, 9);
    expect(ships[2].y).toBeCloseTo(355, 9);
    expect(ships[2].system).toBe('X1-ZC66');
    // D: parked pre-departure at phase 0.04 on the same edge.
    expect(ships[3].x).toBeCloseTo(104.8, 9);
    expect(ships[3].y).toBeCloseTo(376, 9);
    expect(ships[3].system).toBe('X1-ZC66');
  });

  it('keeps dwelling ships on their orbit ring (radius 9 at scale 1)', () => {
    const b = scene().ships[1];
    expect(b.system).toBe('X1-KA42');
    expect(Math.hypot(b.x - 260, b.y - 40)).toBeCloseTo(9, 6);
  });
});

describe('buildSceneData edges', () => {
  it('maps the raw directed topology edges 1:1 with the per-edge construction flag', () => {
    // gateWaypoint is dropped; endpoints + underConstruction survive verbatim,
    // plus the relevance tier. Every mock system trades, so the whole fixture
    // lattice is relevant — the tiering itself is pinned in latticeTiers.test.ts.
    expect(scene().edges).toEqual([
      { from: 'X1-NK36', to: 'X1-KA42', underConstruction: false, relevant: true },
      { from: 'X1-KA42', to: 'X1-ZC66', underConstruction: false, relevant: true },
      { from: 'X1-ZC66', to: 'X1-UU57', underConstruction: false, relevant: true },
      { from: 'X1-UU57', to: 'X1-NK36', underConstruction: true, relevant: true },
    ]);
  });

  it('keeps the edge lattice even when nothing traded (topology-only scene)', () => {
    const s = buildSceneData(mockTopology, undefined, undefined, NOW);
    expect(s.edges).toHaveLength(4);
    expect(s.lanes).toEqual([]);
  });
});

describe('buildSceneData clusters, home, fit', () => {
  it('clusters the topology via clustersFor with the home system mapped through', () => {
    const { clusters, homeSystem } = scene();
    expect(homeSystem).toBe('X1-NK36');
    expect(clusters).toHaveLength(1); // 4 connected systems < MAX_CLUSTER
    expect(clusters[0].id).toBe('X1-KA42'); // sorted seed
    expect(clusters[0].members).toEqual(['X1-KA42', 'X1-NK36', 'X1-UU57', 'X1-ZC66']);
    expect(clusters[0].cx).toBeCloseTo(-70, 9);
    expect(clusters[0].cy).toBeCloseTo(145, 9);
    expect(clusters[0].isHome).toBe(true);
  });

  it('exposes every system position as a fit point', () => {
    expect(scene().fitPoints).toEqual([
      { x: -400, y: -120 },
      { x: 260, y: 40 },
      { x: 120, y: 380 },
      { x: -260, y: 280 },
    ]);
  });
});

describe('buildSceneData market freshness', () => {
  // mockFreshness anchors freshestAt to the CALLER's clock (so the drilldown's
  // "Nm ago" line reads plausibly), so these use wall-clock now rather than the
  // pinned NOW the ship-motion fixtures need.
  const REAL_NOW = Date.now();
  const fresh = () => buildSceneData(mockTopology, mockLanes('6h'), mockLiveFlows(NOW), REAL_NOW, mockFreshness());

  it('resolves dark from the difference against topology, not from the response', () => {
    // mockFreshness omits X1-UU57 entirely and carries X1-QA88 (a zero-listing
    // scout-post system) that topology does not place. The drawn set is
    // topology's, so UU57 is dark and QA88 simply is not drawn.
    const byName = new Map(fresh().systems.map((s) => [s.symbol, s.freshness]));
    expect([...byName.keys()]).toEqual(['X1-NK36', 'X1-KA42', 'X1-ZC66', 'X1-UU57']);
    expect(byName.get('X1-UU57')).toMatchObject({ priced: false, t: null, ageMinutes: null });
    expect(byName.get('X1-NK36')?.priced).toBe(true);
  });

  it('ramps priced systems continuously by age', () => {
    const byName = new Map(fresh().systems.map((s) => [s.symbol, s.freshness]));
    const ts = ['X1-NK36', 'X1-KA42', 'X1-ZC66'].map((s) => byName.get(s)!.t!);
    expect(ts[0]).toBeLessThan(ts[1]);
    expect(ts[1]).toBeLessThan(ts[2]);
    expect(ts.every((t) => t > 0 && t < 1)).toBe(true);
  });

  it('carries the live bound and its basis through for the legend', () => {
    const s = fresh();
    expect(s.rotationBoundMinutes).toBe(400);
    expect(s.rotationBoundBasis).toBe('observed');
    expect(s.marketsKnown).toBe(13_525);
  });

  it('falls back to staleAfterMinutes when an older payload omits the bound', () => {
    const legacy = { ...mockFreshness(), rotationBoundMinutes: undefined, staleAfterMinutes: 222 };
    expect(buildSceneData(mockTopology, null, null, REAL_NOW, legacy).rotationBoundMinutes).toBe(222);
  });

  it('leaves the bound at 0 with no freshness payload — nothing drawn, nothing claimed', () => {
    // The distinction the bands depend on: "we have no freshness data" must not
    // render as "every one of these markets is unsensed".
    const s = buildSceneData(mockTopology, null, null, REAL_NOW);
    expect(s.rotationBoundMinutes).toBe(0);
    expect(s.rotationBoundBasis).toBe('unknown');
    expect(s.systems.every((x) => x.freshness.priced === false)).toBe(true);
  });

  it('aggregates clusters on worst-case age plus a dark count, never a mean', () => {
    const s = fresh();
    expect(s.clusterFreshness.size).toBe(s.clusters.length);
    for (const c of s.clusters) {
      const cf = s.clusterFreshness.get(c.id)!;
      const members = c.members.map((m) => s.systems.find((x) => x.symbol === m)!.freshness);
      const pricedTs = members.filter((f) => f.t != null).map((f) => f.t!);
      expect(cf.darkCount).toBe(members.filter((f) => !f.priced).length);
      if (pricedTs.length > 0) expect(cf.worstT).toBe(Math.max(...pricedTs));
      else expect(cf.worstT).toBeNull();
    }
  });
});

describe('buildSceneData degraded inputs', () => {
  it('returns an empty scene for missing inputs without throwing', () => {
    for (const empty of [
      buildSceneData(undefined, undefined, undefined, 0),
      buildSceneData(null, null, null, NOW),
    ]) {
      expect(empty).toEqual({
        systems: [], lanes: [], ships: [], edges: [], clusters: [], homeSystem: null, fitPoints: [],
        // An empty scene knows NOTHING about freshness — bound 0 and basis
        // 'unknown', which is what makes the bands draw no freshness marks at
        // all rather than ringing every system dark.
        clusterFreshness: new Map(), rotationBoundMinutes: 0, rotationBoundBasis: 'unknown', marketsKnown: 0,
      });
    }
  });

  it('builds the static scene when only topology is available', () => {
    const s = buildSceneData(mockTopology, undefined, undefined, NOW);
    expect(s.systems).toHaveLength(4);
    expect(s.systems.every((x) => x.activity === 0)).toBe(true);
    expect(s.lanes).toEqual([]);
    expect(s.ships).toEqual([]);
    expect(s.clusters).toHaveLength(1);
    expect(s.fitPoints).toHaveLength(4);
  });
});
