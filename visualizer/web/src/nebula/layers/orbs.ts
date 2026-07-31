// REGION band content: one bloom orb per system (white-hot core + tinted halo,
// gold ring on the home system), the dormant gate topology (raw edges) as faint
// 1px threads UNDER the orbs (dashed where the edge is under construction), and
// monospace labels on the top-20 systems by activity. Owns the `orbs` layer
// outright (this band is its only writer) AND the `freshness` layer beneath the
// lanes, plus a private 'region-labels' sub-container inside the shared `labels`
// layer — the galaxyBand pattern — so other bands' labels are never faded or
// cleared by this band.
//
// buildOrbs is idempotent per snapshot: it clears everything it owns before
// drawing. Visibility (250ms cross-fade on band ∈ {REGION, SYSTEM}) is driven
// by the NebulaScene ticker via the returned handle; nothing here animates.
import { Circle, Container, Graphics, Sprite, Text, type FederatedPointerEvent, type Renderer } from 'pixi.js';
import type { SceneData } from '../sceneData';
import type { Layers, PointerHooks } from './registry';
import { worldBounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';
import { DARK_COLOR, FRESHNESS_RAMP, SCOUT_COLOR, rampStep } from '../freshness';

export interface OrbsHandle {
  /** Region-band label sub-container (child of layers.labels) — fade this, not the shared layer. */
  labels: Container;
  /** Market-freshness auras + dark rings + scout markers. Lives in
   * `layers.freshness` (below the lanes), not in `layers.orbs` — so NebulaScene
   * drives that layer's band fade, and the Freshness toggle gates it. */
  freshness: Container;
  /** Threads touching the traded neighbourhood: hoverable, one Graphics each.
   * Rides `layers.orbs` visibility, so it shows across REGION and SYSTEM. */
  nearThreads: Container;
  /** The rest of the lattice: batched and inert. NebulaScene holds this back
   * until the SYSTEM band (or the Lattice toggle) asks for the full web. */
  farThreads: Container;
}

// Palette (exact — see revamp spec).
/** The ACTIVE orb's halo tint. Exported because it is the thing a freshness mark
 * must never be mistaken for — the ramp's head used to be this exact constant
 * (sp-voyz7), so the encoding tests assert separation against it by reading it
 * from here rather than restating it. */
export const CYAN = 0x22d3ee;
const GOLD = 0xe8d9a0;
const SLATE = 0x475569; // dim ember for dormant orbs + the thread lattice
const LABEL = 0x8b9cc0;

/** Px calibration (galaxyBand's trick, shifted one band deeper): sizes below
 * are meant as screen px at the REGION reference zoom (z≈3×fit — the dev
 * `?band=region` camera), assuming the fitted span maps to ~1000 viewport px;
 * worldPerPx = span/(1000·3) scales them to any galaxy size. */
const PX_SPAN = 1000;
const MIN_SPAN = 200;
const REGION_PX_ZOOM = 3;

export const ORB_MIN_PX = 4;
export const ORB_MAX_PX = 18;

/**
 * Pure orb sizing: 4px floor, 18px cap, sqrt scaling on the activity share
 * (magnitude — loss-heavy systems bloom as large as gain-heavy ones), rounded
 * to 1 decimal. Degenerate maxActivity (≤0 or non-finite) → floor.
 */
export function orbRadius(activity: number, maxActivity: number): number {
  if (!Number.isFinite(maxActivity) || maxActivity <= 0) return ORB_MIN_PX;
  const share = Math.min(1, Math.abs(activity) / maxActivity);
  return Math.round((ORB_MIN_PX + (ORB_MAX_PX - ORB_MIN_PX) * Math.sqrt(share)) * 10) / 10;
}

const ORB_TEXTURE_RADIUS = 64;
/** Soft halo profile — tinted per orb (cyan when active, slate ember when not). */
const HALO_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0.9)'],
  [0.35, 'rgba(255,255,255,0.4)'],
  [1, 'rgba(255,255,255,0)'],
];
/** Tight white-hot center over the halo. */
const CORE_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.5, 'rgba(255,255,255,0.9)'],
  [1, 'rgba(255,255,255,0)'],
];
/** Hollow profile → a thin luminous ring at ~80% of the sprite radius. */
const RING_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0)'],
  [0.74, 'rgba(255,255,255,0)'],
  [0.8, 'rgba(255,255,255,0.95)'],
  [0.86, 'rgba(255,255,255,0.2)'],
  [1, 'rgba(255,255,255,0)'],
];
/** Core diameter as a fraction of the orb's; ring reaches past the halo. */
const CORE_FRAC = 0.45;
const RING_SCALE = 1.8;

const THREAD_WIDTH_PX = 1;
const THREAD_ALPHA = 0.3;
/** The far tier draws at roughly half the near tier's alpha, so when SYSTEM (or
 * the Lattice toggle) reveals the whole web the traded subgraph still reads as
 * the foreground rather than dissolving into it. */
const THREAD_FAR_ALPHA = 0.16;
const DASH_ON_PX = 4;
const DASH_OFF_PX = 4;

/** Thread-tier sub-containers inside `layers.orbs` — NebulaScene gates the far
 * tier by band, and the build tests assert per-tier membership by these labels. */
export const THREADS_NEAR = 'threads-near';
export const THREADS_FAR = 'threads-far';

/** Market-freshness sub-container. It is mounted in `layers.freshness` — BELOW
 * the lanes, the orbs and the labels — because presence is context and they are
 * the subject; see the registry's LAYER_ORDER note. Gated by the Freshness
 * toggle, and faded with the REGION band by NebulaScene like the orbs it
 * annotates. The box is still built here because this band owns the per-system
 * geometry (orb radius) the marks are sized from. */
export const FRESHNESS_BOX = 'system-freshness-auras';

/** THE ANNULUS BOTH FRESHNESS STATES DRAW IN, and one radius for both is the
 * point. Priced and dark occupy the SAME ring of space just outside the orb, so
 * the two states differ in FORM alone (a filled soft band vs a dashed hairline)
 * — which is what keeps them apart under CVD, under the focus dimmer and in a
 * grayscale screenshot — and so the luminance comparison sp-voyz7 introduced
 * ("absence must not be drawn louder than presence") is measured over the same
 * pixels for both rather than over two different annuli.
 *
 * 2.6 → 2.0 IS THE DENSITY FIX (sp-9m0bd, Admiral-reported). Measured on the
 * live REGION viewport over the reported neighbourhood, freshness marks crossed
 * each other 380 times in one frame; the eye read the crossings, not the
 * systems. The multiplier is the whole lever here and the px floor is inert:
 * ORB_MIN_PX is 4, so 4 × 2.0 = 8 = MARK_MIN_PX exactly, and 0% of the marks in
 * that frame were pinned by the floor. It is kept as the guard it is — if the
 * orb floor ever drops, a zero-activity system must still show a legible mark.
 *
 * SHRINKING ALONE COULD NEVER HAVE FIXED IT, which is why the priced state is a
 * soft band and not a thinner ring. In that frame the 5th-percentile
 * nearest-neighbour separation is 7.1 design px against a 4px orb floor: ANY
 * hard contour drawn outside the orb crosses its neighbours (measured: 341
 * crossing pairs at the old 10.4px radius, 184 at 8px, and you have to get down
 * to ~3.5px — inside the orb — for zero). A curve with no edge has no locus to
 * cross; two overlapping soft bands blend. The geometry forbids the contour, so
 * the contour had to go.
 *
 * Nothing here ramps with age. Mark size is NOT a freshness channel, or a big
 * stale system would outrank a small fresh one — age is carried by colour alone,
 * and orb radius/tint keep activity to themselves. */
const MARK_SCALE = 2.0;
const MARK_MIN_PX = 8;

/**
 * A system's freshness-mark radius in design px, for an orb of `orbPx`. Pure and
 * exported so both states, the label offset and the tests all read ONE rule —
 * the priced and dark radii used to be two constant pairs that happened to
 * agree, which is not the same thing as sharing an annulus.
 */
export function markRadiusPx(orbPx: number): number {
  return Math.max(MARK_MIN_PX, orbPx * MARK_SCALE);
}

/** Priced: a soft filled band in that annulus, quantised to its ramp step.
 *
 * ENERGY IN THE ANNULUS, NOT UNDER THE ORB. The old aura reused HALO_STOPS,
 * whose energy sits inside 35% of its radius — i.e. underneath the orb, the one
 * place a freshness mark cannot be read, which is why sp-voyz7 measured it at
 * 45.1 peak against the dark ring's 80.9 and concluded the glow had to be
 * replaced. It did not: it had to be moved. This profile is zero out to 0.5
 * (exactly where the orb ends, since MARK_SCALE is 2.0), peaks across the outer
 * half, and returns to zero at the rim so the mark has no edge to moiré with.
 *
 * THE OUTER FALLOFF IS THE LONG ONE, and deliberately so: the inner edge is
 * masked by the orb's own halo, so the only edge a neighbour can cross is the
 * outer one. It runs 0.68 → 1.0, about twice the width of the rise. Measured,
 * the first cut of this profile still put as much strong-gradient pixel on
 * screen as the contour it replaced — at the REGION reference zoom a floor-orb
 * mark is only ~9px in radius, so a profile written in fractions of r gives
 * transitions of 1.6px, which is a ring edge by another name. This softens the
 * mark; it does not remove its edge, and the crossing count is what does the
 * real work here.
 *
 * ONE COLOUR PER SYSTEM, AND IT IS A LEGEND COLOUR. The band replaced a
 * continuously-tinted glow WITH a quantised contour stacked on top of it; two
 * marks per system in near-identical hues is why the framebuffer read adjacent
 * ramp steps 2.2 ΔE apart when the palette promised 9.3. See freshness.ts
 * rampStep for why quantised beats interpolated at this separation.
 *
 * 'normal', NOT 'screen'. Screen is right for the orb halos — neighbours soften
 * instead of summing — but it makes DENSITY read as BRIGHTNESS, and density is
 * the defect. Under normal blend eight overlapping marks look like one mark
 * instead of a hotspot, and the composited colour is exactly tint×alpha over the
 * ground, which is what makes the legend's flat swatches honest. */
export const AURA_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0)'],
  [0.5, 'rgba(255,255,255,0)'],
  [0.62, 'rgba(255,255,255,1)'],
  [0.68, 'rgba(255,255,255,1)'],
  [1, 'rgba(255,255,255,0)'],
];
/** One alpha for every step — the ramp carries the ordering on its own (monotone
 * L* AND C*, see FRESHNESS_RAMP), and a second luminance channel on top of it
 * would only make the map disagree with its own legend, which draws the five
 * stops flat. 0.8 → 0.65 is also the alpha drop this needed: as the PEAK of a
 * profile that is zero at both ends, the mark's mean energy is well under half
 * what the old hard 0.8 stroke put on screen. */
export const AURA_ALPHA = 0.7;

/** Orb halo alpha, active / dormant. Exported alongside CYAN for the same
 * reason: "the mark is not the halo" is a claim about the composited pixel, and
 * a composite needs the alpha. */
export const ORB_HALO_ALPHA_ACTIVE = 0.85;
export const ORB_HALO_ALPHA_DORMANT = 0.45;

/** Dark (unsensed) ring: hollow, dashed, off-ramp slate — UNCHANGED (it reads
 * well and was never what anybody complained about). Hollow-and-dashed is the
 * load-bearing part: priced vs dark must be legible as a difference in FORM, not
 * only in hue. It now shares the priced mark's radius by construction rather
 * than by coincidence — see MARK_SCALE — which is byte-identical to the 2.0/8 it
 * already used. */
const DARK_RING_WIDTH_PX = 1.1;
const DARK_RING_ALPHA = 0.5;
const DARK_DASH_COUNT = 10;

/** Scout-post actuator: a small diamond on the system. Drawn for priced AND dark
 * systems — a post on a dark system (an actuator placed, first scan not yet
 * landed) is the case the freshness endpoint bends its own omission rule for, so
 * dropping it here would discard exactly the record it took trouble to emit. */
const SCOUT_PX = 3.4;
const SCOUT_ALPHA = 0.95;

/** Screen-px pad around an orb's visual radius for its pointer hit circle. */
const ORB_HIT_PAD_PX = 4;

export const REGION_LABEL_COUNT = 20;
const LABEL_FONT_PX = 11;
const LABEL_GAP_PX = 5;
const REGION_LABELS = 'region-labels';

/** Find-or-create the region label sub-container (find + clear own box only —
 * never removeChild on the shared layer — so other bands' labels are untouched). */
function ensureRegionLabels(labelsLayer: Container): Container {
  const existing = labelsLayer.children.find((c) => c.label === REGION_LABELS);
  if (existing != null) return existing as Container;
  const box = new Container();
  box.label = REGION_LABELS;
  labelsLayer.addChild(box);
  return box;
}

function clearLayer(layer: Container): void {
  for (const child of layer.removeChildren()) child.destroy({ children: true });
}

/**
 * (Re)build the REGION band into its layers for one SceneData snapshot. Clears
 * everything this band owns first — call on every snapshot identity change.
 * `events` (Task 12) arms pointer interactivity: orb hover/tap targets and
 * per-thread lane hover; absent, the band is inert (build tests, screenshots).
 */
export function buildOrbs(layers: Layers, data: SceneData, renderer: Renderer, events?: PointerHooks): OrbsHandle {
  clearLayer(layers.orbs);
  // The freshness layer is this band's too, and it is a SEPARATE layer now, so
  // it needs its own clear — left out, every rebuild would stack another full
  // set of auras on the last one.
  clearLayer(layers.freshness);
  const labelBox = ensureRegionLabels(layers.labels);
  clearLayer(labelBox);
  // Tier boxes go in FIRST (far under near under every orb) and always exist, so
  // NebulaScene's per-tick gate never has to null-check an empty scene.
  const farBox = new Container();
  farBox.label = THREADS_FAR;
  layers.orbs.addChild(farBox);
  const nearBox = new Container();
  nearBox.label = THREADS_NEAR;
  layers.orbs.addChild(nearBox);
  // Freshness marks go in their own layer, under the lanes — LAYER_ORDER.
  const freshBox = new Container();
  freshBox.label = FRESHNESS_BOX;
  layers.freshness.addChild(freshBox);
  const handle: OrbsHandle = { labels: labelBox, nearThreads: nearBox, farThreads: farBox, freshness: freshBox };
  if (data.systems.length === 0) return handle;

  const fit = worldBounds(data.fitPoints);
  const span = Math.max(MIN_SPAN, fit.maxX - fit.minX, fit.maxY - fit.minY);
  const worldPerPx = span / PX_SPAN / REGION_PX_ZOOM;

  const bySymbol = new Map(data.systems.map((s) => [s.symbol, s]));
  const maxActivity = data.systems.reduce((m, s) => Math.max(m, Math.abs(s.activity)), 0);

  // ---- Dormant threads: the RAW gate topology (data.edges — spec L1) as faint
  // slate lines, added before the orbs so every orb renders on top. TIERED by
  // sceneData's `relevant` flag, because an undifferentiated lattice stopped
  // working when the gate graph hit ~5.2k edges — it renders as an opaque mat
  // that buries the trade lanes (sp-fw6a2):
  //   NEAR — edges touching the traded neighbourhood. One Graphics PER directed
  //     edge (`thread:<from→to>`, the FlowTooltip lane-key shape) so each thread
  //     is its own hover target with the stroke's bounds as the hit area —
  //     exactly what the whole lattice used to be, now ~5% of it.
  //   FAR  — everything else, BATCHED into one Graphics per dash style (two
  //     objects instead of thousands) and inert: at this density the far lattice
  //     is context texture, not something you aim at. Held back to the SYSTEM
  //     band by NebulaScene.
  // Dashes are manual 4/4 segments — pixi Graphics has no native dash.
  const dashOn = DASH_ON_PX * worldPerPx;
  const dashOff = DASH_OFF_PX * worldPerPx;
  /** Trace one edge's path into `g` (solid, or 4/4 dashes when under construction). */
  const trace = (g: Graphics, ax: number, ay: number, bx: number, by: number, len: number, dashed: boolean) => {
    if (!dashed) {
      g.moveTo(ax, ay);
      g.lineTo(bx, by);
      return;
    }
    const ux = (bx - ax) / len;
    const uy = (by - ay) / len;
    for (let t = 0; t < len; t += dashOn + dashOff) {
      const e = Math.min(t + dashOn, len);
      g.moveTo(ax + ux * t, ay + uy * t);
      g.lineTo(ax + ux * e, ay + uy * e);
    }
  };
  const nearStyle = { width: THREAD_WIDTH_PX * worldPerPx, color: SLATE, alpha: THREAD_ALPHA } as const;
  const farStyle = { width: THREAD_WIDTH_PX * worldPerPx, color: SLATE, alpha: THREAD_FAR_ALPHA } as const;
  // One batch per dash style: a Graphics strokes its whole accumulated path with
  // a single style, so solid and dashed far edges cannot share one object.
  const farSolid = new Graphics();
  const farDashed = new Graphics();
  let farSolidCount = 0;
  let farDashedCount = 0;
  for (const edge of data.edges) {
    if (edge.from === edge.to) continue;
    const a = bySymbol.get(edge.from);
    const b = bySymbol.get(edge.to);
    if (a == null || b == null) continue;
    const len = Math.hypot(b.x - a.x, b.y - a.y);
    if (len < 1e-6) continue;
    if (!edge.relevant) {
      if (edge.underConstruction) {
        trace(farDashed, a.x, a.y, b.x, b.y, len, true);
        farDashedCount++;
      } else {
        trace(farSolid, a.x, a.y, b.x, b.y, len, false);
        farSolidCount++;
      }
      continue;
    }
    const key = `${edge.from}→${edge.to}`;
    const thread = new Graphics();
    thread.label = `thread:${key}`;
    trace(thread, a.x, a.y, b.x, b.y, len, edge.underConstruction);
    thread.stroke(nearStyle);
    if (events != null) {
      thread.eventMode = 'static';
      thread.on('pointerover', (e: FederatedPointerEvent) => events.hover('lane', key, e));
      thread.on('pointerout', () => events.hoverOut());
    }
    nearBox.addChild(thread);
  }
  // Stroke and mount the batches only if they drew anything — an empty Graphics
  // would otherwise sit in the tier and make "far tier is empty" untestable.
  if (farSolidCount > 0) {
    farSolid.stroke(farStyle);
    farBox.addChild(farSolid);
  } else {
    farSolid.destroy();
  }
  if (farDashedCount > 0) {
    farDashed.stroke(farStyle);
    farBox.addChild(farDashed);
  } else {
    farDashed.destroy();
  }

  // ---- Market freshness: soft band (priced) / hollow dashed ring (dark) -----
  //
  // Drawn only when a freshness payload actually reached the scene. A failed or
  // not-yet-landed poll leaves rotationBoundMinutes at 0, and in that case NOTHING
  // is drawn rather than every system being ringed dark: "we have no freshness
  // data" and "these markets are unsensed" are different claims, and painting the
  // first as the second is the same class of lie as rendering a missing age at the
  // far end of the ramp. The legend states which case the reader is in.
  if (data.rotationBoundMinutes > 0) {
    const auraTex = makeGlowTexture(renderer, ORB_TEXTURE_RADIUS, AURA_STOPS);
    const darkRings = new Graphics();
    const scouts = new Graphics();
    let darkCount = 0;
    let scoutCount = 0;

    for (const sys of data.systems) {
      // ONE radius for both states — see MARK_SCALE.
      const radius = markRadiusPx(orbRadius(sys.activity, maxActivity)) * worldPerPx;
      const f = sys.freshness;

      if (f.priced && f.t != null) {
        const aura = new Sprite(auraTex);
        aura.anchor.set(0.5);
        aura.position.set(sys.x, sys.y);
        aura.width = radius * 2;
        aura.height = radius * 2;
        // Quantised to the ramp step, so the mark wears a legend colour exactly
        // rather than an un-matchable point between two of them. Alpha is flat:
        // the ramp is monotone in BOTH lightness and chroma and carries the
        // ordering on its own.
        aura.tint = FRESHNESS_RAMP[rampStep(f.t)];
        aura.alpha = AURA_ALPHA;
        freshBox.addChild(aura);
      } else {
        // Manual dashes — pixi Graphics has no native dash (the `trace` rationale).
        const step = (Math.PI * 2) / DARK_DASH_COUNT;
        for (let k = 0; k < DARK_DASH_COUNT; k++) {
          const a0 = k * step;
          darkRings.moveTo(sys.x + Math.cos(a0) * radius, sys.y + Math.sin(a0) * radius);
          darkRings.arc(sys.x, sys.y, radius, a0, a0 + step * 0.5);
        }
        darkCount++;
      }

      if (f.scoutPost != null) {
        const d = SCOUT_PX * worldPerPx;
        scouts.moveTo(sys.x, sys.y - d);
        scouts.lineTo(sys.x + d, sys.y);
        scouts.lineTo(sys.x, sys.y + d);
        scouts.lineTo(sys.x - d, sys.y);
        scouts.closePath();
        scoutCount++;
      }
    }

    // Batched: one Graphics for every dark ring and one for every scout marker.
    // Mounted only when non-empty, so "no dark systems" is an empty box rather
    // than an invisible object that makes the state untestable (the far-thread
    // batch rationale).
    if (darkCount > 0) {
      darkRings.stroke({ width: DARK_RING_WIDTH_PX * worldPerPx, color: DARK_COLOR, alpha: DARK_RING_ALPHA });
      freshBox.addChild(darkRings);
    } else {
      darkRings.destroy();
    }
    if (scoutCount > 0) {
      scouts.fill({ color: SCOUT_COLOR, alpha: SCOUT_ALPHA });
      freshBox.addChild(scouts);
    } else {
      scouts.destroy();
    }
  }

  // ---- Orbs: tinted halo + white-hot core per system; gold ring on home.
  const haloTex = makeGlowTexture(renderer, ORB_TEXTURE_RADIUS, HALO_STOPS);
  const coreTex = makeGlowTexture(renderer, ORB_TEXTURE_RADIUS, CORE_STOPS);
  const ringTex = makeGlowTexture(renderer, ORB_TEXTURE_RADIUS, RING_STOPS);
  for (const sys of data.systems) {
    const r = orbRadius(sys.activity, maxActivity) * worldPerPx;
    const active = Math.abs(sys.activity) > 0;

    const halo = new Sprite(haloTex);
    halo.anchor.set(0.5);
    halo.position.set(sys.x, sys.y);
    halo.width = r * 2;
    halo.height = r * 2;
    halo.tint = active ? CYAN : SLATE;
    halo.alpha = active ? ORB_HALO_ALPHA_ACTIVE : ORB_HALO_ALPHA_DORMANT;
    // 'screen': neighbouring halos in dense regions soften instead of summing.
    halo.blendMode = 'screen';
    layers.orbs.addChild(halo);

    const core = new Sprite(coreTex);
    core.anchor.set(0.5);
    core.position.set(sys.x, sys.y);
    core.width = r * 2 * CORE_FRAC;
    core.height = r * 2 * CORE_FRAC;
    // Active cores burn white; dormant ones are a dim slate ember.
    core.tint = active ? 0xffffff : SLATE;
    core.alpha = active ? 1 : 0.5;
    core.blendMode = 'add';
    layers.orbs.addChild(core);

    if (sys.isHome) {
      const ring = new Sprite(ringTex);
      ring.anchor.set(0.5);
      ring.position.set(sys.x, sys.y);
      ring.width = r * 2 * RING_SCALE;
      ring.height = r * 2 * RING_SCALE;
      ring.tint = GOLD;
      ring.alpha = 0.9;
      // 'normal': additive/screen over the bright core washes gold to white.
      ring.blendMode = 'normal';
      layers.orbs.addChild(ring);
    }

    // Invisible pointer target over the orb (`orb-hit:<symbol>`): a circle a
    // touch larger than the visual radius. Living in `orbs`, it fades/culls
    // with the band — no orb hover or tap while the REGION band is hidden.
    if (events != null) {
      const hit = new Container();
      hit.label = `orb-hit:${sys.symbol}`;
      hit.position.set(sys.x, sys.y);
      hit.eventMode = 'static';
      hit.cursor = 'pointer';
      hit.hitArea = new Circle(0, 0, r + ORB_HIT_PAD_PX * worldPerPx);
      hit.on('pointerover', (e: FederatedPointerEvent) => events.hover('system', sys.symbol, e));
      hit.on('pointerout', () => events.hoverOut());
      hit.on('pointertap', () => events.tapSystem(sys.symbol));
      layers.orbs.addChild(hit);
    }
  }

  // ---- Labels: top-20 systems by |activity| (deterministic symbol tiebreak);
  // the rest stay unlabelled until the SYSTEM band (Task 11).
  const top = [...data.systems]
    .sort((s1, s2) => Math.abs(s2.activity) - Math.abs(s1.activity) || (s1.symbol < s2.symbol ? -1 : 1))
    .slice(0, REGION_LABEL_COUNT);
  for (const sys of top) {
    // Offset from the FRESHNESS MARK, not from the orb. A label placed against
    // the orb sits inside its own system's mark on any orb above the floor
    // (the mark reaches 2× the orb radius), and a glyph on a mark is a glyph on
    // ground of very nearly its own luminance — measured 1.05:1 before this
    // (sp-9m0bd). The offset is the mark's whether or not the Freshness toggle
    // is drawing one, so labels never shift when it is flipped.
    const r = markRadiusPx(orbRadius(sys.activity, maxActivity)) * worldPerPx;
    const text = new Text({
      text: sys.symbol,
      style: {
        fontFamily: 'monospace',
        fontSize: LABEL_FONT_PX,
        fill: LABEL,
        // Backdrop-colored halo so the text survives sitting on a bright orb.
        dropShadow: { color: 0x070312, alpha: 1, blur: 3, distance: 0, angle: 0 },
      },
      resolution: 2,
    });
    text.anchor.set(0.5, 0);
    text.scale.set(worldPerPx);
    text.position.set(sys.x, sys.y + r + LABEL_GAP_PX * worldPerPx);
    labelBox.addChild(text);
  }

  return handle;
}
