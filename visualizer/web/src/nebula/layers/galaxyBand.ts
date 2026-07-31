// GALAXY band content: one soft aura per cluster (gold ring on the home
// region), aggregate money currents between cluster centers with drifting
// particles, and labels on the top-3 currents. Owns the `auras` and `currents`
// layers outright (this band is their only writer) plus a private
// 'galaxy-labels' sub-container inside the shared `labels` layer, so other
// bands' labels are never faded or cleared by this band.
//
// buildGalaxyBand is idempotent per snapshot: it clears everything it owns
// before drawing. Visibility (250ms cross-fade on band==='GALAXY') and particle
// advancement are driven by the NebulaScene ticker via the returned handle.
import { Container, Graphics, Sprite, Text, type FederatedPointerEvent, type Renderer } from 'pixi.js';
import type { SceneData } from '../sceneData';
import type { Layers, PointerHooks } from './registry';
import { worldBounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';
import { aggregateCurrents } from '../aggregate';
import { DARK_COLOR, rampColor } from '../freshness';

export interface GalaxyBandHandle {
  /** Galaxy-band label sub-container (child of layers.labels) — fade this, not the shared layer. */
  labels: Container;
  /** Per-cluster market-freshness rings; gated by the Freshness toggle. */
  freshness: Container;
  /** Advance drifting current particles. Call from the ticker only while visible. */
  update(dtMs: number): void;
}

/** Cluster-freshness sub-container inside `layers.auras`. */
export const CLUSTER_FRESHNESS_BOX = 'cluster-freshness';

/** Cluster freshness gauge geometry.
 *
 * A SEPARATE mark, not a re-tint of the aura: the aura's tint is paired with its
 * backdrop fog's hue and its alpha carries profit share, so overwriting either
 * would trade one meaning for another rather than add one.
 *
 * FIXED SCREEN-PX RADIUS, deliberately not proportional to the aura. The first
 * version scaled with the aura (1.18× and 1.3×) and rendered as an opaque mat of
 * ~65 overlapping circles that buried the auras and the money currents outright —
 * caught by screenshotting the page, invisible to the build tests. Aura radius
 * already encodes member count; the gauge re-encoding it bought nothing and cost
 * the whole band's legibility. At a constant small size the gauges read as one
 * comparable set of dials scattered over the cloud, which is what they are. */
const GAUGE_PX = 11;
const GAUGE_WIDTH_PX = 1.8;
const GAUGE_ALPHA = 0.9;
/** Dark dashes per full circle — the dark arc uses this density pro-rata, so a
 * quarter-dark cluster gets a quarter of them and the dash pitch stays constant. */
const GAUGE_DARK_DASHES = 12;
/** Gap (radians) between the priced arc and the dark arc, so the split is a
 * visible boundary rather than a colour change mid-stroke. */
const GAUGE_ARC_GAP = 0.12;

// Palette (exact — see revamp spec). Violet is the named #a78bfa here, not the
// backdrop fog's deliberate 0x8b5cf6.
const CYAN = 0x22d3ee;
const VIOLET = 0xa78bfa;
const GOLD = 0xe8d9a0;
const EMBER = 0xef4444;
const LABEL = 0x8b9cc0;

/** Px calibration (backdrop STAR_SIZE_SPAN's trick): sizes below are meant as
 * screen px at fit zoom, assuming the fitted span maps to ~1000 viewport px;
 * worldPerPx = span/1000 scales them to any galaxy size. */
const PX_SPAN = 1000;
const MIN_SPAN = 200;

const AURA_TEXTURE_RADIUS = 256;
const AURA_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0.5)'],
  [0.55, 'rgba(255,255,255,0.22)'],
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
const PARTICLE_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.35, 'rgba(255,255,255,0.85)'],
  [1, 'rgba(255,255,255,0)'],
];

/** Aura radius ∝ √memberCount (scaled fixed-radius texture, mirroring backdrop economy). */
const AURA_RADIUS_FRAC = 0.035;
const AURA_MIN_PX = 24;
const AURA_MAX_FRAC = 0.16;
/** Near-zero floor: with ~200 clusters a visible floor would just re-paint the
 * backdrop fog; profit share alone decides which regions glow. */
const AURA_ALPHA_MIN = 0.04;
const AURA_ALPHA_MAX = 0.5;
const AURA_TINTS = [VIOLET, CYAN] as const; // same parity as the backdrop fog → aura reinforces its fog's hue

const CURRENT_WIDTH_MIN_PX = 2;
const CURRENT_WIDTH_MAX_PX = 14;
const CTRL_BULGE = 0.15; // control point offset ⊥ to the chord, as a fraction of its length
/** Cyan→violet approximated with two overlaid strokes: full-length cyan, then
 * violet over the destination side from this t onward. */
const GRADIENT_SPLIT_T = 0.45;

const PARTICLES_MIN = 8;
const PARTICLES_MAX = 24;
const PARTICLE_SIZE_PX = 4;
/** Curve-fractions per second, cycled per particle index (deterministic — no Math.random). */
const PARTICLE_SPEEDS = [0.12, 0.15, 0.18] as const;

const LABEL_COUNT = 3;
const LABEL_OFFSET_PX = 26;
/** Collision window for the greedy label de-overlap (screen px): the top
 * currents share the dense core, so midpoint labels would otherwise stack. */
const LABEL_ROW_PX = 14;
const LABEL_COL_PX = 78;
const GALAXY_LABELS = 'galaxy-labels';

/** `±X.XM/hr`, or `±XXXk/hr` under 1M. Pure, exported for tests. */
export function formatPerHr(profitPerHr: number): string {
  const sign = profitPerHr < 0 ? '-' : '+';
  const a = Math.abs(profitPerHr);
  const amount = a >= 1e6 ? `${(a / 1e6).toFixed(1)}M` : `${Math.round(a / 1e3)}k`;
  return `${sign}${amount}/hr`;
}

/** Quadratic Bézier component: (1-t)²·a + 2(1-t)t·c + t²·b. */
function q(a: number, c: number, b: number, t: number): number {
  const s = 1 - t;
  return s * s * a + 2 * s * t * c + t * t * b;
}

interface DriftingParticle {
  sprite: Sprite;
  t: number;
  speed: number;
  reversed: boolean;
  x0: number; y0: number; cx: number; cy: number; x1: number; y1: number;
}

function clearLayer(layer: Container): void {
  for (const child of layer.removeChildren()) child.destroy({ children: true });
}

/** Find-or-create the galaxy label sub-container (find + removeChildren only —
 * never removeChild — so the shared labels layer's other children are untouched). */
function ensureGalaxyLabels(labelsLayer: Container): Container {
  const existing = labelsLayer.children.find((c) => c.label === GALAXY_LABELS);
  if (existing != null) return existing as Container;
  const box = new Container();
  box.label = GALAXY_LABELS;
  labelsLayer.addChild(box);
  return box;
}

/**
 * (Re)build the GALAXY band into its layers for one SceneData snapshot. Clears
 * everything this band owns first — call on every snapshot identity change.
 * `events` (Task 12) arms current-ribbon hover; absent, the band is inert.
 */
export function buildGalaxyBand(layers: Layers, data: SceneData, renderer: Renderer, events?: PointerHooks): GalaxyBandHandle {
  clearLayer(layers.auras);
  clearLayer(layers.currents);
  const labelBox = ensureGalaxyLabels(layers.labels);
  clearLayer(labelBox);

  // Mounted unconditionally (the orbs tier-box rationale): the ticker's toggle
  // gate and the build tests must find this container even in an empty scene.
  const freshBox = new Container();
  freshBox.label = CLUSTER_FRESHNESS_BOX;
  layers.auras.addChild(freshBox);

  const particles: DriftingParticle[] = [];
  const handle: GalaxyBandHandle = {
    labels: labelBox,
    freshness: freshBox,
    update(dtMs: number) {
      const dt = dtMs / 1000;
      for (const p of particles) {
        p.t = (p.t + p.speed * dt) % 1;
        const u = p.reversed ? 1 - p.t : p.t;
        p.sprite.position.set(q(p.x0, p.cx, p.x1, u), q(p.y0, p.cy, p.y1, u));
      }
    },
  };
  if (data.clusters.length === 0) return handle;

  const fit = worldBounds(data.fitPoints);
  const span = Math.max(MIN_SPAN, fit.maxX - fit.minX, fit.maxY - fit.minY);
  const worldPerPx = span / PX_SPAN;

  // ---- Auras: one glow per cluster; radius ∝ √members, alpha ∝ profit share.
  const auraTex = makeGlowTexture(renderer, AURA_TEXTURE_RADIUS, AURA_STOPS);
  const ringTex = makeGlowTexture(renderer, AURA_TEXTURE_RADIUS, RING_STOPS);
  const bySymbol = new Map(data.systems.map((s) => [s.symbol, s]));
  const clusterProfit = new Map<string, number>();
  let totalProfit = 0;
  for (const c of data.clusters) {
    let p = 0;
    for (const m of c.members) p += Math.abs(bySymbol.get(m)?.activity ?? 0);
    clusterProfit.set(c.id, p);
    totalProfit += p;
  }
  data.clusters.forEach((cluster, i) => {
    const radius = Math.min(
      span * AURA_MAX_FRAC,
      Math.max(AURA_MIN_PX * worldPerPx, span * AURA_RADIUS_FRAC * Math.sqrt(cluster.members.length)),
    );
    const share = totalProfit > 0 ? (clusterProfit.get(cluster.id) ?? 0) / totalProfit : 0;
    const aura = new Sprite(auraTex);
    aura.anchor.set(0.5);
    aura.position.set(cluster.cx, cluster.cy);
    aura.width = radius * 2;
    aura.height = radius * 2;
    aura.tint = AURA_TINTS[i % AURA_TINTS.length];
    aura.alpha = Math.min(AURA_ALPHA_MAX, AURA_ALPHA_MIN + share * 0.8);
    aura.blendMode = 'screen'; // overlapping auras soften instead of summing (backdrop rationale)
    layers.auras.addChild(aura);

    if (cluster.isHome) {
      const ring = new Sprite(ringTex);
      ring.anchor.set(0.5);
      ring.position.set(cluster.cx, cluster.cy);
      ring.width = radius * 2;
      ring.height = radius * 2;
      ring.tint = GOLD;
      ring.alpha = 0.9;
      // 'normal': additive/screen blending over the bright home aura washes the
      // gold to white — the ring must actually read gold.
      ring.blendMode = 'normal';
      layers.auras.addChild(ring);
    }
  });

  // ---- Cluster market freshness: one small dial per cluster ----------------
  //
  // AGGREGATION IS EXPLICITLY NOT AN AVERAGE, and that is the whole design
  // question at this band. A mean age over a cluster hides a dark system inside a
  // fresh neighbourhood and dilutes one badly-lagging market to nothing — the two
  // states most worth seeing are exactly the two an average erases. So the dial
  // shows two independent facts instead (see clusterFreshnessFor):
  //
  //   priced arc — stroked in the OLDEST priced member's ramp colour, so one
  //                lagging system colours its cluster and cannot be averaged away.
  //   dark arc   — LENGTH ∝ the share of members with no prices at all, dashed
  //                in the same off-ramp slate the REGION band's dark rings use.
  //                Length rather than opacity because an unsensed market is a
  //                countable fact and length is the channel that reads as a count.
  //
  // The two arcs complete one circle, so the split is legible at a glance: a fully
  // sensed cluster is a solid coloured ring, a fully dark one a dashed grey ring,
  // and anything between shows its own proportion. A cluster is ≤ 8 systems
  // (clusters.ts MAX_CLUSTER), so "worst of these" is a claim about a small
  // legible neighbourhood, not a whole region.
  //
  // Lifted to the top of `layers.auras` — a crisp stroke under a soft glow washes
  // out. addChild on an already-parented child re-orders it, which is the intent.
  layers.auras.addChild(freshBox);
  if (data.rotationBoundMinutes > 0) {
    const r = GAUGE_PX * worldPerPx;
    const width = GAUGE_WIDTH_PX * worldPerPx;
    const darkArcs = new Graphics();
    let arcCount = 0;

    for (const cluster of data.clusters) {
      const cf = data.clusterFreshness.get(cluster.id);
      if (cf == null || cf.members === 0) continue;
      const darkSweep = Math.PI * 2 * cf.darkRatio;
      const start = -Math.PI / 2;

      // Priced arc: the remainder of the circle, in the worst member's colour.
      // Its own Graphics — a Graphics strokes its whole path with ONE style, so
      // per-cluster colours cannot share an object (the far-thread rationale).
      if (cf.worstT != null && cf.darkRatio < 1) {
        const gap = cf.darkCount > 0 ? GAUGE_ARC_GAP : 0;
        const g = new Graphics();
        g.arc(cluster.cx, cluster.cy, r, start + darkSweep + gap, start + Math.PI * 2 - gap);
        g.stroke({ width, color: rampColor(cf.worstT), alpha: GAUGE_ALPHA });
        freshBox.addChild(g);
      }

      if (cf.darkCount > 0) {
        const dashes = Math.max(1, Math.round(GAUGE_DARK_DASHES * cf.darkRatio));
        const seg = darkSweep / dashes;
        for (let k = 0; k < dashes; k++) {
          const a0 = start + k * seg;
          darkArcs.moveTo(cluster.cx + Math.cos(a0) * r, cluster.cy + Math.sin(a0) * r);
          darkArcs.arc(cluster.cx, cluster.cy, r, a0, a0 + seg * 0.6);
        }
        arcCount++;
      }
    }

    // Every dark arc shares one style, so they batch into a single object.
    if (arcCount > 0) {
      darkArcs.stroke({ width, color: DARK_COLOR, alpha: GAUGE_ALPHA });
      freshBox.addChild(darkArcs);
    } else {
      darkArcs.destroy();
    }
  }

  // ---- Currents: quadratic ribbons between cluster centers + drifting particles.
  const byId = new Map(data.clusters.map((c) => [c.id, c]));
  const currents = aggregateCurrents(data.clusters, data.lanes);
  const maxAbsProfit = currents.reduce((m, c) => Math.max(m, Math.abs(c.profitPerHr)), 0);
  const maxVolume = currents.reduce((m, c) => Math.max(m, c.volume), 0);
  const particleTex = makeGlowTexture(renderer, 16, PARTICLE_STOPS);

  interface CurrentGeom { cur: (typeof currents)[number]; x0: number; y0: number; cx: number; cy: number; x1: number; y1: number; px: number; py: number }
  const geoms: CurrentGeom[] = [];

  for (const cur of currents) {
    const a = byId.get(cur.fromCluster);
    const b = byId.get(cur.toCluster);
    if (a == null || b == null) continue;
    const dx = b.cx - a.cx;
    const dy = b.cy - a.cy;
    const len = Math.hypot(dx, dy);
    if (len < 1e-6) continue;
    const px = -dy / len; // unit perpendicular — deterministic bulge side
    const py = dx / len;
    const cx = (a.cx + b.cx) / 2 + px * CTRL_BULGE * len;
    const cy = (a.cy + b.cy) / 2 + py * CTRL_BULGE * len;
    geoms.push({ cur, x0: a.cx, y0: a.cy, cx, cy, x1: b.cx, y1: b.cy, px, py });

    const share = maxAbsProfit > 0 ? Math.abs(cur.profitPerHr) / maxAbsProfit : 0;
    const width = (CURRENT_WIDTH_MIN_PX + (CURRENT_WIDTH_MAX_PX - CURRENT_WIDTH_MIN_PX) * share) * worldPerPx;
    // Hover target (Task 12): the ribbon's own stroke bounds are the hit area;
    // the gradient tip is a second Graphics, so it is armed with the same key.
    const currentKey = `${cur.fromCluster}|${cur.toCluster}`;
    const armHover = (target: Graphics) => {
      if (events == null) return;
      target.eventMode = 'static';
      target.on('pointerover', (e: FederatedPointerEvent) => events.hover('current', currentKey, e));
      target.on('pointerout', () => events.hoverOut());
    };
    const g = new Graphics();
    g.label = `current:${currentKey}`;
    // 'screen', not 'add': the active region stacks many short currents — they
    // must soften where they overlap, not sum into a white-hot scribble.
    g.blendMode = 'screen';
    armHover(g);
    if (cur.profitPerHr < 0) {
      g.moveTo(a.cx, a.cy);
      g.quadraticCurveTo(cx, cy, b.cx, b.cy);
      g.stroke({ width, color: EMBER, alpha: 0.55, cap: 'round', join: 'round' });
      layers.currents.addChild(g);
    } else {
      // Two overlaid strokes approximate the cyan→violet gradient: full-length
      // cyan, then a violet tip over the destination side. The tip lives in its
      // own Graphics so each stroke's path scope is unambiguous, and the
      // de Casteljau split at GRADIENT_SPLIT_T keeps it an exact quadratic.
      g.moveTo(a.cx, a.cy);
      g.quadraticCurveTo(cx, cy, b.cx, b.cy);
      g.stroke({ width, color: CYAN, alpha: 0.45, cap: 'round', join: 'round' });
      layers.currents.addChild(g);
      const s = GRADIENT_SPLIT_T;
      const abx = a.cx + (cx - a.cx) * s, aby = a.cy + (cy - a.cy) * s;
      const bcx = cx + (b.cx - cx) * s, bcy = cy + (b.cy - cy) * s;
      const mx = abx + (bcx - abx) * s, my = aby + (bcy - aby) * s;
      const tip = new Graphics();
      tip.label = `current-tip:${currentKey}`;
      tip.blendMode = 'screen';
      armHover(tip);
      tip.moveTo(mx, my);
      tip.quadraticCurveTo(bcx, bcy, b.cx, b.cy);
      tip.stroke({ width: width * 0.8, color: VIOLET, alpha: 0.6, cap: 'round', join: 'round' });
      layers.currents.addChild(tip);
    }

    // Drifting particles: count ∝ volume share; from→to (reversed when negative).
    const volShare = maxVolume > 0 ? cur.volume / maxVolume : 0;
    const count = Math.min(PARTICLES_MAX, PARTICLES_MIN + Math.round((PARTICLES_MAX - PARTICLES_MIN) * volShare));
    const reversed = cur.profitPerHr < 0;
    const tint = reversed ? EMBER : CYAN;
    for (let i = 0; i < count; i++) {
      const sprite = new Sprite(particleTex);
      sprite.anchor.set(0.5);
      sprite.width = PARTICLE_SIZE_PX * worldPerPx;
      sprite.height = PARTICLE_SIZE_PX * worldPerPx;
      sprite.tint = tint;
      sprite.alpha = 0.7;
      sprite.blendMode = 'add';
      const t = i / count;
      const u = reversed ? 1 - t : t;
      sprite.position.set(q(a.cx, cx, b.cx, u), q(a.cy, cy, b.cy, u));
      layers.currents.addChild(sprite);
      particles.push({
        sprite, t, reversed,
        speed: PARTICLE_SPEEDS[i % PARTICLE_SPEEDS.length],
        x0: a.cx, y0: a.cy, cx, cy, x1: b.cx, y1: b.cy,
      });
    }
  }

  // ---- Labels: top-3 currents by |profit/hr|, at the curve midpoint, nudged
  // past the arc's crown along the bulge perpendicular.
  const top = [...geoms]
    .sort((g1, g2) =>
      Math.abs(g2.cur.profitPerHr) - Math.abs(g1.cur.profitPerHr) ||
      (g1.cur.fromCluster < g2.cur.fromCluster ? -1 : 1),
    )
    .slice(0, LABEL_COUNT);
  const off = LABEL_OFFSET_PX * worldPerPx;
  const spots = top.map((geo) => ({
    geo,
    x: q(geo.x0, geo.cx, geo.x1, 0.5) + geo.px * off,
    y: q(geo.y0, geo.cy, geo.y1, 0.5) + geo.py * off,
  }));
  // Greedy de-overlap: whenever a label lands inside a previous label's window,
  // push it a row below (two passes settle 3 labels deterministically).
  for (let pass = 0; pass < 2; pass++) {
    for (let i = 1; i < spots.length; i++) {
      for (let j = 0; j < i; j++) {
        if (
          Math.abs(spots[i].x - spots[j].x) < LABEL_COL_PX * worldPerPx &&
          Math.abs(spots[i].y - spots[j].y) < LABEL_ROW_PX * worldPerPx
        ) {
          spots[i].y = spots[j].y + LABEL_ROW_PX * worldPerPx;
        }
      }
    }
  }
  for (const spot of spots) {
    const text = new Text({
      text: formatPerHr(spot.geo.cur.profitPerHr),
      style: {
        fontFamily: 'monospace',
        fontSize: 11,
        fill: LABEL,
        // Backdrop-colored halo so the text survives sitting on a bright ribbon.
        dropShadow: { color: 0x070312, alpha: 1, blur: 3, distance: 0, angle: 0 },
      },
      resolution: 2,
    });
    text.anchor.set(0.5);
    text.scale.set(worldPerPx);
    text.position.set(spot.x, spot.y);
    labelBox.addChild(text);
  }

  return handle;
}
