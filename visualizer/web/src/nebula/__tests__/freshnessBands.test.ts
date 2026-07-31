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
import { Container, Graphics, Sprite, type Renderer } from 'pixi.js';
import { buildSceneData, type SceneData } from '../sceneData';
import { buildOrbs, FRESHNESS_BOX, AURA_ALPHA, ORB_MIN_PX, markRadiusPx, orbRadius } from '../layers/orbs';
import { buildGalaxyBand, CLUSTER_FRESHNESS_BOX } from '../layers/galaxyBand';
import { LAYER_ORDER, type Layers } from '../layers/registry';
import { mockTopology, mockLanes, mockFreshness } from '../../mocks/mockFlows';
import { FRESHNESS_RAMP, DARK_COLOR, rampStep } from '../freshness';

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
/** ...with lane volume, so orb radii — and therefore mark radii — actually vary.
 * Several claims below are vacuous on a scene where every orb is at the floor. */
const withActivity = (): SceneData =>
  buildSceneData(mockTopology, mockLanes('1h'), null, NOW, mockFreshness());

const boxIn = (layer: Container, label: string) =>
  layer.children.find((c) => c.label === label) as Container | undefined;

describe('REGION band freshness marks', () => {
  it('draws one aura per priced system plus a batched dark-ring object', () => {
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    expect(box).toBeDefined();

    // mockTopology draws NK36, KA42, ZC66 (priced) and UU57 (omitted ⇒ dark).
    // 3 aura sprites + 1 batched dark-ring Graphics + 1 batched scout Graphics.
    // ONE MARK PER SYSTEM (sp-9m0bd): the priced state used to be a glow AND a
    // contour stacked on it, two near-identical hues per system, which is how
    // adjacent ramp steps came off the framebuffer 2.2 ΔE apart when the palette
    // promised 9.3.
    const tints = box.children.filter((c) => c instanceof Sprite).map((c) => (c as { tint: number }).tint);
    expect(tints).toHaveLength(3);
    expect(box.children).toHaveLength(5);
  });

  it('tints each aura at its own ramp STEP — a mark wears a legend colour exactly', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    const auras = box.children.filter((c) => c instanceof Sprite) as unknown as { tint: number; alpha: number }[];

    // Quantised, not interpolated: adjacent steps are only ~9 ΔE apart (the
    // ceiling this scene's constraints allow), so a tint landing BETWEEN two
    // legend stops is by construction less separable than either one — and it
    // is unmatchable against the legend, which draws the five stops flat.
    const priced = data.systems.filter((s) => s.freshness.priced);
    expect(auras.map((a) => a.tint)).toEqual(priced.map((s) => FRESHNESS_RAMP[rampStep(s.freshness.t!)]));
    for (const a of auras) expect(FRESHNESS_RAMP as readonly number[]).toContain(a.tint);
    // The fixture must actually span the ramp, or this proves nothing.
    expect(new Set(auras.map((a) => a.tint)).size).toBeGreaterThan(1);
    // Ordered along the ramp: the freshest system is the brightest.
    const lum = (c: number) => 0.2126 * ((c >> 16) & 0xff) + 0.7152 * ((c >> 8) & 0xff) + 0.0722 * (c & 0xff);
    expect(lum(auras[0].tint)).toBeGreaterThan(lum(auras[2].tint));
    // No aura ever wears the dark state's colour.
    expect(auras.map((a) => a.tint)).not.toContain(DARK_COLOR);
  });

  it('gives every aura ONE flat alpha — the ramp carries the ordering by itself', () => {
    // A second luminance channel on top of a ramp that is already monotone in
    // L* and C* would only make the map disagree with its own legend, which
    // draws the five stops at full opacity.
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    const alphas = box.children.filter((c) => c instanceof Sprite).map((c) => (c as { alpha: number }).alpha);
    expect(new Set(alphas)).toEqual(new Set([AURA_ALPHA]));
  });

  it('gives a dark system a ring and NO aura — absence of data, absence of glow', () => {
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    // One aura short of the system count: UU57 is dark.
    const auras = box.children.filter((c) => c instanceof Sprite);
    expect(data.systems).toHaveLength(4);
    expect(auras).toHaveLength(3);
    expect(data.systems.find((s) => s.symbol === 'X1-UU57')!.freshness.t).toBeNull();
  });

  it('never marks a dark system as priced — absence of data still gets absence of glow', () => {
    const layers = makeLayers();
    // Every system dark: the freshness response carries no priced record that
    // topology can place, so nothing may draw a priced mark.
    const fresh = mockFreshness();
    fresh.systems = fresh.systems.map((s) => ({ ...s, totalListings: 0, freshestAt: null }));
    const data = buildSceneData(mockTopology, null, null, NOW, fresh);
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    expect(data.systems.every((s) => !s.freshness.priced)).toBe(true);
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
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    expect(box).toBeDefined();
    expect(box.children).toHaveLength(0);
  });

  // sp-9m0bd — the marks must sit UNDER the lanes and the labels. Presence is
  // context; the trade structure and the system names are the subject, and at
  // REGION density carrying freshness above them buried both.
  it('draws beneath the lanes, the orbs and the labels', () => {
    expect(LAYER_ORDER.indexOf('freshness')).toBeLessThan(LAYER_ORDER.indexOf('lanes'));
    expect(LAYER_ORDER.indexOf('freshness')).toBeLessThan(LAYER_ORDER.indexOf('orbs'));
    expect(LAYER_ORDER.indexOf('freshness')).toBeLessThan(LAYER_ORDER.indexOf('labels'));
    // ...and it is the freshness LAYER the marks land in, not the orb layer.
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    expect(boxIn(layers.freshness, FRESHNESS_BOX)).toBeDefined();
    expect(boxIn(layers.orbs, FRESHNESS_BOX)).toBeUndefined();
  });

  it('clears its own layer on rebuild — a re-poll must not stack a second set', () => {
    // The marks moved out of layers.orbs, so buildOrbs clearing layers.orbs no
    // longer reaches them. Left unswept, every freshness poll would add another
    // full set of auras on top of the last one.
    const layers = makeLayers();
    buildOrbs(layers, withFreshness(), stubRenderer);
    const first = boxIn(layers.freshness, FRESHNESS_BOX)!.children.length;
    buildOrbs(layers, withFreshness(), stubRenderer);
    expect(layers.freshness.children).toHaveLength(1);
    expect(boxIn(layers.freshness, FRESHNESS_BOX)!.children).toHaveLength(first);
  });

  it('sizes priced and dark marks to ONE radius, so form is the only difference', () => {
    // The two states share an annulus by construction. It is what makes "absence
    // must not be drawn louder than presence" a comparison over the same pixels,
    // and it is why priced-vs-dark survives CVD and grayscale.
    const layers = makeLayers();
    const data = withFreshness();
    buildOrbs(layers, data, stubRenderer);
    const box = boxIn(layers.freshness, FRESHNESS_BOX)!;
    const auras = box.children.filter((c) => c instanceof Sprite) as unknown as { width: number; x: number }[];
    const darkRing = box.children.find((c) => c instanceof Graphics && c.bounds.width > 0) as Graphics;
    expect(darkRing).toBeDefined();

    // The dark system (UU57) and every priced system sit at the orb floor in
    // this fixture, so their marks must come out the same size.
    const maxActivity = data.systems.reduce((m, s) => Math.max(m, Math.abs(s.activity)), 0);
    const floorOrbs = data.systems.filter((s) => orbRadius(s.activity, maxActivity) === ORB_MIN_PX);
    expect(floorOrbs.length).toBeGreaterThan(1);
    expect(floorOrbs.some((s) => s.freshness.priced)).toBe(true);
    expect(floorOrbs.some((s) => !s.freshness.priced)).toBe(true);
    const auraDiameter = auras[0].width;
    // The batched dark ring's bounds are its path diameter PLUS its stroke
    // width (the stroke straddles the path), so back the stroke out before
    // comparing — otherwise this passes for any radius within a stroke of the
    // aura's, which is most of them.
    const stroke = (darkRing as unknown as { strokeStyle: { width: number } }).strokeStyle;
    expect(stroke.width).toBeGreaterThan(0);
    // Relative, because the dashed ring is a tessellated polyline: its bounds
    // sit a hair inside the true circle by an error that scales with radius.
    const darkDiameter = darkRing.bounds.width - stroke.width;
    expect(Math.abs(darkDiameter - auraDiameter) / auraDiameter).toBeLessThan(0.001);
  });

  it('scales every mark by markRadiusPx, so the shared radius is a rule not a coincidence', () => {
    // Ratios, so this needs no worldPerPx: whatever the scale, the drawn marks
    // must be in the proportion the exported rule gives. The fixture has to span
    // more than the orb floor or every ratio is 1 and this proves nothing.
    const layers = makeLayers();
    const data = withActivity();
    const maxActivity = data.systems.reduce((m, s) => Math.max(m, Math.abs(s.activity)), 0);
    const priced = data.systems.filter((s) => s.freshness.priced && s.freshness.t != null);
    const orbs = priced.map((s) => orbRadius(s.activity, maxActivity));
    expect(new Set(orbs).size).toBeGreaterThan(1);

    buildOrbs(layers, data, stubRenderer);
    const auras = boxIn(layers.freshness, FRESHNESS_BOX)!
      .children.filter((c) => c instanceof Sprite) as unknown as { width: number }[];
    expect(auras).toHaveLength(priced.length);
    const expected = orbs.map((o) => markRadiusPx(o) / markRadiusPx(orbs[0]));
    // The expectation must itself vary, or a markRadiusPx that ignored its
    // argument would make both sides 1 and this would agree with anything.
    expect(new Set(expected).size).toBeGreaterThan(1);
    for (let i = 1; i < auras.length; i++) {
      expect(auras[i].width / auras[0].width).toBeCloseTo(expected[i], 6);
    }
  });

  it('offsets each label past its own freshness mark — a glyph must not sit on a band', () => {
    // The label used to be offset from the ORB, so on any orb above the floor it
    // landed inside its own system's mark: measured on the live map, a glyph
    // against the brightest ground inside its own box came to 1.05:1, which is
    // no contrast at all. Compared against the mark AS DRAWN (the aura's own
    // width), so it cannot drift from what the renderer actually put there.
    const layers = makeLayers();
    const data = withActivity();
    const maxActivity = data.systems.reduce((m, s) => Math.max(m, Math.abs(s.activity)), 0);
    // Above the floor, or the orb and mark radii are too close to tell apart.
    expect(data.systems.some((s) => orbRadius(s.activity, maxActivity) > ORB_MIN_PX)).toBe(true);

    buildOrbs(layers, data, stubRenderer);
    const priced = data.systems.filter((s) => s.freshness.priced && s.freshness.t != null);
    const auras = boxIn(layers.freshness, FRESHNESS_BOX)!
      .children.filter((c) => c instanceof Sprite) as unknown as { width: number }[];
    const labelBox = layers.labels.children.find((c) => c.label === 'region-labels') as Container;
    expect(labelBox.children.length).toBeGreaterThan(0);

    let checked = 0;
    for (const label of labelBox.children) {
      const symbol = (label as unknown as { text: string }).text;
      const i = priced.findIndex((s) => s.symbol === symbol);
      if (i < 0) continue; // dark systems carry a ring, not an aura
      // Top of the glyph box sits at or beyond the mark's outer edge.
      expect(label.y - priced[i].y).toBeGreaterThanOrEqual(auras[i].width / 2);
      checked += 1;
    }
    expect(checked).toBeGreaterThan(0);
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
