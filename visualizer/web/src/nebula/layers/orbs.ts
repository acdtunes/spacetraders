// REGION band content: one bloom orb per system (white-hot core + tinted halo,
// gold ring on the home system), the dormant gate topology (raw edges) as faint
// 1px threads UNDER the orbs (dashed where the edge is under construction), and
// monospace labels on the top-20 systems by activity. Owns the `orbs` layer
// outright (this band is its only writer) plus a private 'region-labels'
// sub-container inside the shared `labels` layer — the galaxyBand pattern — so
// other bands' labels are never faded or cleared by this band.
//
// buildOrbs is idempotent per snapshot: it clears everything it owns before
// drawing. Visibility (250ms cross-fade on band ∈ {REGION, SYSTEM}) is driven
// by the NebulaScene ticker via the returned handle; nothing here animates.
import { Circle, Container, Graphics, Sprite, Text, type FederatedPointerEvent, type Renderer } from 'pixi.js';
import type { SceneData } from '../sceneData';
import type { Layers, PointerHooks } from './registry';
import { worldBounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';

export interface OrbsHandle {
  /** Region-band label sub-container (child of layers.labels) — fade this, not the shared layer. */
  labels: Container;
  /** Threads touching the traded neighbourhood: hoverable, one Graphics each.
   * Rides `layers.orbs` visibility, so it shows across REGION and SYSTEM. */
  nearThreads: Container;
  /** The rest of the lattice: batched and inert. NebulaScene holds this back
   * until the SYSTEM band (or the Lattice toggle) asks for the full web. */
  farThreads: Container;
}

// Palette (exact — see revamp spec).
const CYAN = 0x22d3ee;
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
  const handle: OrbsHandle = { labels: labelBox, nearThreads: nearBox, farThreads: farBox };
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
    halo.alpha = active ? 0.85 : 0.45;
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
    const r = orbRadius(sys.activity, maxActivity) * worldPerPx;
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
