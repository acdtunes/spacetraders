// Build-path smoke for the SYSTEM band: real pixi containers (scene graph only,
// no WebGL — jsdom), a stub renderer, and a fixture detail. Verifies the layer
// contract (fx + 'system-labels' ownership, idempotent clears) and that the
// gate badge chip exists exactly when the detail carries construction data —
// the one branch live data cannot currently exercise end-to-end.
import { describe, it, expect } from 'vitest';
import { Container, type Renderer } from 'pixi.js';
import { buildSystemBand, clearSystemBand } from '../layers/systemBand';
import { LAYER_ORDER, type Layers } from '../layers/registry';
import type { SystemDetail } from '../useSystemDetail';
import type { SceneData } from '../sceneData';
import type { Waypoint } from '../../types/spacetraders';

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

const wp = (symbol: string, type: string, x: number, y: number, extra: Partial<Waypoint> = {}): Waypoint =>
  ({
    symbol,
    type,
    systemSymbol: 'X1-TEST',
    x,
    y,
    orbitals: [],
    traits: [],
    isUnderConstruction: false,
    ...extra,
  }) as Waypoint;

const waypoints: Waypoint[] = [
  wp('X1-TEST-A1', 'PLANET', 30, 10, { hasMarketplace: true }),
  wp('X1-TEST-B2', 'ASTEROID', -80, 40),
  wp('X1-TEST-I3', 'JUMP_GATE', 10, -90, { isUnderConstruction: true }),
];

const detailWith = (gate: SystemDetail['gate']): SystemDetail => ({
  symbol: 'X1-TEST',
  waypoints,
  freshness: { system: 'X1-TEST', totalListings: 10, freshListings: 8, freshnessPct: 80, freshestAt: null, scoutPost: null },
  gate,
});

const scene: SceneData = {
  systems: [{ symbol: 'X1-TEST', x: 500, y: 500, activity: 1, isHome: false, underConstruction: true }],
  lanes: [],
  ships: [{ id: 's1', flowId: 'f1', x: 500, y: 500, headingRad: 0, system: 'X1-TEST' }],
  edges: [],
  clusters: [],
  homeSystem: null,
  fitPoints: [
    { x: 0, y: 0 },
    { x: 1000, y: 1000 },
  ],
};

const findBadge = (labels: Container) =>
  (labels.children.find((c) => c.label === 'system-labels') as Container | undefined)?.children.find(
    (c) => c.label === 'gate-badge',
  );

describe('buildSystemBand', () => {
  it('draws into fx + its own system-labels box, with a gate badge when construction data exists', () => {
    const layers = makeLayers();
    const handle = buildSystemBand(
      layers,
      detailWith({ progress: 45, materials: [{ tradeSymbol: 'FAB_MATS', required: 100, fulfilled: 45 }] }),
      'X1-TEST',
      stubRenderer,
      scene,
    );
    expect(layers.fx.children.length).toBeGreaterThan(0);
    expect(handle.labels.label).toBe('system-labels');
    expect(handle.labels.children.length).toBeGreaterThan(0);
    expect(findBadge(layers.labels)).toBeDefined();
  });

  it('omits the badge for an unstarted bill and never touches other label boxes', () => {
    const layers = makeLayers();
    const foreign = new Container();
    foreign.label = 'region-labels';
    const marker = new Container();
    foreign.addChild(marker);
    layers.labels.addChild(foreign);

    buildSystemBand(layers, detailWith(null), 'X1-TEST', stubRenderer, scene);
    expect(findBadge(layers.labels)).toBeUndefined();
    expect(foreign.children).toContain(marker);

    clearSystemBand(layers);
    expect(layers.fx.children).toHaveLength(0);
    expect(foreign.children).toContain(marker); // the shared layer's other boxes survive
  });

  it('degrades to an empty band when the focused system is missing from the scene', () => {
    const layers = makeLayers();
    buildSystemBand(layers, detailWith(null), 'X1-ELSEWHERE', stubRenderer, scene);
    expect(layers.fx.children).toHaveLength(0);
  });
});
