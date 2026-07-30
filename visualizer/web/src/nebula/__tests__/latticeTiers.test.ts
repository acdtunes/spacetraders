// sp-fw6a2 — the dormant-thread lattice outgrew the view: parked sensing took
// the era-5 gate graph from ~1.2k edges to 5,173 in a day, and drawn
// undifferentiated it renders as an opaque grey mat that buries the ~240 real
// trade lanes it exists to frame. Two halves are pinned here:
//   1. buildSceneData tags each edge `relevant` — does it touch the
//      neighbourhood we actually trade in? (pure, no pixi)
//   2. buildOrbs splits the lattice into a hoverable NEAR tier and a batched,
//      inert FAR tier (real pixi scene graph, stub renderer — jsdom has no WebGL)
// The band gate that holds the FAR tier back to SYSTEM lives in latticeBand.test.tsx.
import { describe, it, expect, vi } from 'vitest';
import { Container, Graphics, type Renderer } from 'pixi.js';
import { buildSceneData, type SceneData, type SceneEdge } from '../sceneData';
import { buildOrbs, THREADS_NEAR, THREADS_FAR } from '../layers/orbs';
import { LAYER_ORDER, type Layers, type PointerHooks } from '../layers/registry';
import { mockTopology, mockLiveFlows } from '../../mocks/mockFlows';
import type { LanesResponse } from '../../types/flows';

const stubRenderer = {} as unknown as Renderer;

function makeLayers(): Layers {
  const world = new Container();
  const named = {} as Record<(typeof LAYER_ORDER)[number], Container>;
  for (const name of LAYER_ORDER) {
    const layer = new Container();
    layer.label = name;
    world.addChild(layer);
    named[name] = layer;
  }
  return { ...named, world };
}

// ---- Part 1: the relevance rule ---------------------------------------------
// mockTopology is a 4-system ring: NK36→KA42→ZC66→UU57→NK36 (the last edge is
// under construction). mockLiveFlows puts hulls in NK36, KA42 and ZC66 — so
// which systems the lane lists name, and whether ships count, is observable
// edge by edge.
const NOW = 600_000_000;

/** A LanesResponse naming exactly the given system lanes / activity systems. */
const lanesNaming = (systemLanes: [string, string][], activity: string[] = []): LanesResponse => ({
  lanes: [],
  systemLanes: systemLanes.map(([from, to]) => ({
    from, to, realizedUnits: 10, realizedProfit: 1000, legCount: 1, goods: {}, topGoods: [],
  })),
  systemActivity: activity.map((system) => ({ system, realizedProfit: 500, legCount: 1 })),
  window: '6h',
  generatedAt: new Date(0).toISOString(),
});

/** `from→to` ⇒ relevant, for the whole lattice. */
const relevanceOf = (edges: SceneEdge[]) =>
  Object.fromEntries(edges.map((e) => [`${e.from}→${e.to}`, e.relevant]));

describe('buildSceneData lattice relevance', () => {
  it('tags an edge relevant when EITHER endpoint is a traded system, and untraded pairs false', () => {
    // Only NK36↔KA42 traded, and no ships — so KA42→ZC66 has exactly ONE
    // endpoint in the seed and must still be relevant (an edge reaching from a
    // traded system out to a quiet neighbour is the context that frames the
    // lane). ZC66→UU57 touches neither and is the one edge that culls.
    const s = buildSceneData(mockTopology, lanesNaming([['X1-NK36', 'X1-KA42']]), undefined, NOW);
    expect(relevanceOf(s.edges)).toEqual({
      'X1-NK36→X1-KA42': true,  // both endpoints traded
      'X1-KA42→X1-ZC66': true,  // ONE endpoint traded — falsifies an ANY→BOTH change
      'X1-ZC66→X1-UU57': false, // neither endpoint traded — the cull
      'X1-UU57→X1-NK36': true,  // ONE endpoint traded (and under construction)
    });
  });

  it('seeds relevance from systemActivity, not just the directed lane endpoints', () => {
    // Same lanes, but UU57 now books realized profit with no directed lane of
    // its own. Dropping the systemActivity term flips this edge back to false.
    const s = buildSceneData(
      mockTopology,
      lanesNaming([['X1-NK36', 'X1-KA42']], ['X1-UU57']),
      undefined,
      NOW,
    );
    expect(relevanceOf(s.edges)['X1-ZC66→X1-UU57']).toBe(true);
  });

  it('seeds relevance from live ship systems, so no hull is left gliding over a culled thread', () => {
    // Lanes name only NK36/KA42; ZC66 is reached by nothing but the two hulls
    // parked/warping there. ZC66→UU57 is therefore relevant PURELY via the ship
    // term — drop it and this edge culls out from under TORWIND-54/11.
    const live = mockLiveFlows(NOW);
    const s = buildSceneData(mockTopology, lanesNaming([['X1-NK36', 'X1-KA42']]), live, NOW);
    expect(s.ships.map((x) => x.system)).toContain('X1-ZC66');
    expect(relevanceOf(s.edges)['X1-ZC66→X1-UU57']).toBe(true);

    // Same inputs minus the live feed: the only thing that changed is the ships.
    const noShips = buildSceneData(mockTopology, lanesNaming([['X1-NK36', 'X1-KA42']]), undefined, NOW);
    expect(relevanceOf(noShips.edges)['X1-ZC66→X1-UU57']).toBe(false);
  });

  it('keeps every edge in the lattice — relevance tiers the draw, it never drops data', () => {
    // The cull is a render tier, not a filter: a topology-only scene still
    // carries all 4 edges (all far), so the Lattice toggle can reveal them.
    const s = buildSceneData(mockTopology, undefined, undefined, NOW);
    expect(s.edges).toHaveLength(4);
    expect(s.edges.every((e) => e.relevant === false)).toBe(true);
  });

  it('ignores blank endpoints and activity rows with no system symbol', () => {
    // An empty `from` and a systemActivity row missing `system` must seed
    // nothing — otherwise '' / undefined lands in the seed and the `has()`
    // lookups start matching on junk. (Wholly null ROWS are out of scope: the
    // pre-existing systemLanes mapping above dereferences them and would throw
    // before relevance is ever computed — unchanged by this fix.)
    const ragged = {
      lanes: [],
      systemLanes: [
        { from: '', to: 'X1-KA42', realizedUnits: 0, realizedProfit: 0, legCount: 0, topGoods: [] },
      ],
      systemActivity: [{ realizedProfit: 0, legCount: 0 }],
      window: '6h',
      generatedAt: '',
    } as unknown as LanesResponse;
    const s = buildSceneData(mockTopology, ragged, undefined, NOW);
    // Only the one well-formed endpoint (KA42) seeded anything.
    expect(relevanceOf(s.edges)).toEqual({
      'X1-NK36→X1-KA42': true,
      'X1-KA42→X1-ZC66': true,
      'X1-ZC66→X1-UU57': false,
      'X1-UU57→X1-NK36': false,
    });
  });
});

// ---- Part 2: the rendered tiers ---------------------------------------------
// A traded core (A,B,C) plus an untraded periphery (D,E,F,G). Six edges: three
// touch the core, three do not — one of those under construction, so the far
// tier's dash split is observable.
const sys = (symbol: string, x: number, y: number, activity = 0) =>
  ({ symbol, x, y, activity, isHome: false, underConstruction: false });
const edge = (from: string, to: string, relevant: boolean, underConstruction = false): SceneEdge =>
  ({ from, to, underConstruction, relevant });

const tieredScene = (): SceneData => ({
  systems: [
    sys('X1-A', 0, 0, 900), sys('X1-B', 200, 0, 400), sys('X1-C', 400, 100, 100),
    sys('X1-D', 600, 200), sys('X1-E', 800, 300), sys('X1-F', 1000, 400), sys('X1-G', 1200, 500),
  ],
  lanes: [{ from: 'X1-A', to: 'X1-B', profitPerHr: 100, volume: 10, realized: 600, projected: 0 }],
  ships: [],
  edges: [
    edge('X1-A', 'X1-B', true),
    edge('X1-B', 'X1-C', true),
    edge('X1-C', 'X1-D', true),           // reaches out of the core — still near
    edge('X1-D', 'X1-E', false),
    edge('X1-E', 'X1-F', false, true),    // far AND under construction → dashed batch
    edge('X1-F', 'X1-G', false),
  ],
  clusters: [],
  homeSystem: null,
  fitPoints: [{ x: 0, y: 0 }, { x: 1200, y: 500 }],
});

const tier = (layers: Layers, label: string) =>
  layers.orbs.children.find((c) => c.label === label) as Container;
const threadLabels = (box: Container) => box.children.map((c) => c.label).sort();

describe('buildOrbs lattice tiers', () => {
  it('puts ONLY the relevant edges in the near tier, one hoverable Graphics each', () => {
    const layers = makeLayers();
    buildOrbs(layers, tieredScene(), stubRenderer);
    // Three relevant edges → three individually-keyed threads, and nothing else.
    expect(threadLabels(tier(layers, THREADS_NEAR))).toEqual([
      'thread:X1-A→X1-B', 'thread:X1-B→X1-C', 'thread:X1-C→X1-D',
    ]);
  });

  it('batches the irrelevant edges into the far tier instead of one object each', () => {
    const layers = makeLayers();
    buildOrbs(layers, tieredScene(), stubRenderer);
    const far = tier(layers, THREADS_FAR);
    // Three far edges, but only TWO Graphics — one per dash style. Drawing them
    // per-edge (the old shape) would put three here.
    expect(far.children).toHaveLength(2);
    expect(far.children.every((c) => c instanceof Graphics)).toBe(true);
    // Batched context texture, never a hover target.
    expect(far.children.every((c) => c.eventMode !== 'static')).toBe(true);
    // ...and no far edge leaked a per-edge thread key into the scene.
    for (const key of ['thread:X1-D→X1-E', 'thread:X1-E→X1-F', 'thread:X1-F→X1-G']) {
      expect(threadLabels(tier(layers, THREADS_NEAR))).not.toContain(key);
    }
  });

  it('scales the far tier by dash style, not by edge count', () => {
    const layers = makeLayers();
    const scene = tieredScene();
    // 200 more untraded edges among the periphery: the far tier must not grow.
    for (let i = 0; i < 200; i++) {
      scene.systems.push(sys(`X1-P${i}`, 2000 + i * 10, 1000 + i * 7));
      scene.edges.push(edge('X1-G', `X1-P${i}`, false));
    }
    buildOrbs(layers, scene, stubRenderer);
    expect(tier(layers, THREADS_FAR).children).toHaveLength(2);
    expect(tier(layers, THREADS_NEAR).children).toHaveLength(3);
  });

  it('drops the dashed far batch entirely when no far edge is under construction', () => {
    const layers = makeLayers();
    const scene = tieredScene();
    scene.edges = scene.edges.map((e) => ({ ...e, underConstruction: false }));
    buildOrbs(layers, scene, stubRenderer);
    expect(tier(layers, THREADS_FAR).children).toHaveLength(1);
  });

  it('keeps both tier containers present and ordered far-under-near-under-orbs', () => {
    const layers = makeLayers();
    // Even an all-relevant scene keeps an (empty) far box, so the band gate in
    // NebulaScene never has to null-check it.
    const scene = tieredScene();
    scene.edges = scene.edges.map((e) => ({ ...e, relevant: true }));
    buildOrbs(layers, scene, stubRenderer);
    const labels = layers.orbs.children.map((c) => c.label);
    expect(labels.indexOf(THREADS_FAR)).toBe(0);
    expect(labels.indexOf(THREADS_NEAR)).toBe(1);
    expect(tier(layers, THREADS_FAR).children).toHaveLength(0);
    expect(tier(layers, THREADS_NEAR).children).toHaveLength(6);
  });

  it('arms hover on near threads with the directed lane key', () => {
    const layers = makeLayers();
    const events: PointerHooks = { hover: vi.fn(), hoverOut: vi.fn(), tapSystem: vi.fn() };
    buildOrbs(layers, tieredScene(), stubRenderer, events);
    const thread = tier(layers, THREADS_NEAR).children.find((c) => c.label === 'thread:X1-A→X1-B')!;
    expect(thread.eventMode).toBe('static');
    thread.emit('pointerover', { clientX: 4, clientY: 5 } as never);
    expect(events.hover).toHaveBeenCalledWith('lane', 'X1-A→X1-B', { clientX: 4, clientY: 5 });
  });

  it('is idempotent — a rebuild replaces the tiers instead of stacking them', () => {
    const layers = makeLayers();
    buildOrbs(layers, tieredScene(), stubRenderer);
    buildOrbs(layers, tieredScene(), stubRenderer);
    expect(layers.orbs.children.filter((c) => c.label === THREADS_NEAR)).toHaveLength(1);
    expect(layers.orbs.children.filter((c) => c.label === THREADS_FAR)).toHaveLength(1);
    expect(tier(layers, THREADS_NEAR).children).toHaveLength(3);
    expect(tier(layers, THREADS_FAR).children).toHaveLength(2);
  });

  it('exposes both tiers on the handle for the band gate', () => {
    const layers = makeLayers();
    const handle = buildOrbs(layers, tieredScene(), stubRenderer);
    expect(handle.nearThreads).toBe(tier(layers, THREADS_NEAR));
    expect(handle.farThreads).toBe(tier(layers, THREADS_FAR));
  });

  it('still hands back usable tier containers for an empty scene', () => {
    const layers = makeLayers();
    const handle = buildOrbs(layers, { ...tieredScene(), systems: [] }, stubRenderer);
    expect(handle.nearThreads.label).toBe(THREADS_NEAR);
    expect(handle.farThreads.label).toBe(THREADS_FAR);
    expect(handle.farThreads.children).toHaveLength(0);
  });
});
