// sp-3fcdx — the freshness marks actually reach the scene graph, at both the
// GALAXY (per-cluster) and REGION (per-system) bands. Real pixi containers with a
// stub renderer (jsdom has no WebGL), mirroring latticeTiers.test.ts.
//
// These pin STRUCTURE — that the right number of marks of the right kind exist,
// and that they vanish when there is no freshness data. They are deliberately not
// offered as evidence that the aura is legible on screen; that was checked by
// rendering the page (see the bead's render evidence), because this dashboard has
// produced three defects that passed their unit tests and only a browser caught.
import { describe, it, expect, vi } from 'vitest';
import { Container, Sprite, type Renderer } from 'pixi.js';
import { buildSceneData, type SceneData } from '../sceneData';
import { buildOrbs, FRESHNESS_BOX, PRICED_RING_PREFIX } from '../layers/orbs';
import { buildGalaxyBand, CLUSTER_FRESHNESS_BOX } from '../layers/galaxyBand';
import { LAYER_ORDER, type Layers } from '../layers/registry';
import { mockTopology, mockFreshness } from '../../mocks/mockFlows';
import { FRESHNESS_RAMP, DARK_COLOR, rampColor, rampStep } from '../freshness';

vi.mock('../glowTexture', () => ({ makeGlowTexture: () => ({ width: 1, height: 1 }) }));

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

const NOW = Date.now();
const withFreshness = (): SceneData =>
  buildSceneData(mockTopology, null, null, NOW, mockFreshness());
const withoutFreshness = (): SceneData => buildSceneData(mockTopology, null, null, NOW);

const boxIn = (layer: Container, label: string) =>
  layer.children.find((c) => c.label === label) as Container | undefined;

describe('REGION band freshness marks', () => {
  it('draws one aura per priced system plus a batched dark-ring object', () => {
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    expect(box).toBeDefined();

    // mockTopology draws NK36, KA42, ZC66 (priced) and UU57 (omitted ⇒ dark).
    // 3 aura sprites + 3 priced-contour batches (their t's land on 3 distinct
    // ramp steps) + 1 batched dark-ring Graphics + 1 batched scout Graphics.
    const tints = box.children.filter((c) => c instanceof Sprite).map((c) => (c as { tint: number }).tint);
    expect(tints).toHaveLength(3);
    expect(box.children).toHaveLength(8);
  });

  it('tints each aura by its own age — the ramp is continuous, not two buckets', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    const tints = box.children.filter((c) => c instanceof Sprite).map((c) => (c as { tint: number }).tint);

    // Every aura tint is a distinct point on the ramp, and matches the exact
    // colour its system's own t resolves to.
    expect(new Set(tints).size).toBe(3);
    const priced = data.systems.filter((s) => s.freshness.priced);
    expect(tints).toEqual(priced.map((s) => rampColor(s.freshness.t!)));
    // Ordered along the ramp: the freshest system is the brightest. (It is NOT
    // exactly FRESHNESS_RAMP[0] — a 4-minute-old scan sits just off the head,
    // which is what "continuous" means and what a two-bucket encoding could not
    // produce.)
    const lum = (c: number) => 0.2126 * ((c >> 16) & 0xff) + 0.7152 * ((c >> 8) & 0xff) + 0.0722 * (c & 0xff);
    expect(lum(tints[0])).toBeGreaterThan(lum(tints[1]));
    expect(lum(tints[1])).toBeGreaterThan(lum(tints[2]));
    expect(lum(tints[0])).toBeLessThan(lum(FRESHNESS_RAMP[0]));
    // No aura ever wears the dark state's colour.
    expect(tints).not.toContain(DARK_COLOR);
  });

  it('gives a dark system a ring and NO aura — absence of data, absence of glow', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    // One aura short of the system count: UU57 is dark.
    const auras = box.children.filter((c) => c instanceof Sprite);
    expect(data.systems).toHaveLength(4);
    expect(auras).toHaveLength(3);
    expect(data.systems.find((s) => s.symbol === 'X1-UU57')!.freshness.t).toBeNull();
  });

  // sp-voyz7 — the priced state must be a MARK, not a glow. Measured on the live
  // map at REGION, peak luminance in the annulus outside each orb was 79 for a
  // dark system's dashed ring against 29 for a priced system's glow: absence was
  // drawn 2.8× louder than presence and the whole field read dark. Worse, ramp
  // step 0 IS the orb's own CYAN, so on a TRADED (active) system the glow was a
  // #22d3ee smudge behind a brighter #22d3ee halo — invisible by construction.
  // The contour is what survives both, so these pin it existing, being per-step,
  // and never appearing for a dark system.
  const ringBatches = (box: Container) =>
    box.children.filter((c) => typeof c.label === 'string' && c.label.startsWith(PRICED_RING_PREFIX));

  it('gives every priced system a contour, batched by ramp step', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;

    const priced = data.systems.filter((s) => s.freshness.priced);
    expect(priced).toHaveLength(3);
    // One batch per DISTINCT step the priced systems occupy — not one per system
    // (265 strokes on the live map) and not one for all (a Graphics carries one
    // stroke colour, so a single batch could only ever draw one ramp step).
    const steps = [...new Set(priced.map((s) => rampStep(s.freshness.t!)))].sort();
    expect(steps.length).toBeGreaterThan(1); // the fixture must span the ramp
    expect(ringBatches(box).map((c) => c.label).sort()).toEqual(
      steps.map((i) => `${PRICED_RING_PREFIX}${i}`).sort(),
    );
    // No batch is mounted for a step nothing occupies.
    expect(ringBatches(box)).toHaveLength(steps.length);
  });

  it('draws the contour in its ramp step colour, not the dark colour', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    for (const batch of ringBatches(box)) {
      const step = Number((batch.label as string).slice(PRICED_RING_PREFIX.length));
      const style = (batch as unknown as { strokeStyle: { color: number; alpha: number; width: number } }).strokeStyle;
      expect(style.color).toBe(FRESHNESS_RAMP[step]);
      expect(style.color).not.toBe(DARK_COLOR);
      expect(style.alpha).toBeGreaterThan(0);
      expect(style.width).toBeGreaterThan(0);
    }
  });

  it('never contours a dark system — absence of data still gets absence of glow AND of edge', () => {
    const layers = makeLayers();
    // Every system dark: the freshness response carries no priced record that
    // topology can place, so nothing may draw a priced contour.
    const fresh = mockFreshness();
    fresh.systems = fresh.systems.map((s) => ({ ...s, totalListings: 0, freshestAt: null }));
    const data = buildSceneData(mockTopology, null, null, NOW, fresh);
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    expect(data.systems.every((s) => !s.freshness.priced)).toBe(true);
    expect(ringBatches(box)).toHaveLength(0);
    expect(box.children.filter((c) => c instanceof Sprite)).toHaveLength(0);
    // ...but the dark rings themselves are still there — this is the "all dark"
    // state, not the "no data" state.
    expect(box.children.length).toBeGreaterThan(0);
  });

  it('draws nothing at all when no freshness payload has landed', () => {
    // The distinction that matters: a failed poll must not ring every system
    // dark, because "we do not know" is not "these markets are unsensed".
    const layers = makeLayers();
    buildOrbs(layers, withoutFreshness(), stubRenderer);
    const box = boxIn(layers.orbs, FRESHNESS_BOX)!;
    expect(box).toBeDefined();
    expect(box.children).toHaveLength(0);
  });

  it('sits behind the orbs and above the lattice', () => {
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    const labels = layers.orbs.children.map((c) => c.label);
    const fresh = labels.indexOf(FRESHNESS_BOX);
    expect(fresh).toBeGreaterThan(labels.indexOf('threads-near'));
    // Orb sprites are appended after, so every later child outranks the box.
    expect(fresh).toBeLessThan(labels.length - 1);
  });
});

describe('GALAXY band cluster freshness', () => {
  it('rings each cluster and marks its dark share', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildGalaxyBand(layers, data, stubRenderer);
    const box = boxIn(layers.auras, CLUSTER_FRESHNESS_BOX)!;
    expect(box).toBeDefined();
    // The single mock cluster has priced members AND a dark one (UU57), so both
    // the worst-case ring and the dark arc are present.
    expect(box.children).toHaveLength(2);
    const cf = data.clusterFreshness.get(data.clusters[0].id)!;
    expect(cf.worstT).not.toBeNull();
    expect(cf.darkCount).toBe(1);
  });

  it('is lifted above the cluster auras — a crisp stroke under a glow washes out', () => {
    const layers = makeLayers();
    buildGalaxyBand(layers, withFreshness(), stubRenderer);
    const labels = layers.auras.children.map((c) => c.label);
    expect(labels[labels.length - 1]).toBe(CLUSTER_FRESHNESS_BOX);
  });

  it('draws nothing when no freshness payload has landed', () => {
    const layers = makeLayers();
    buildGalaxyBand(layers, withoutFreshness(), stubRenderer);
    const box = boxIn(layers.auras, CLUSTER_FRESHNESS_BOX)!;
    expect(box).toBeDefined();
    expect(box.children).toHaveLength(0);
  });
});
