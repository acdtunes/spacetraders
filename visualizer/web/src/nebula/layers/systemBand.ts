// SYSTEM band content: the drilldown-in-place. When a system is focused the
// `fx` layer (this band's outright property) shows that system's interior at
// its world position — waypoints to scale via the
// ported drilldownGeometry placement (reused verbatim, never rewritten), a
// market-freshness ring per marketplace waypoint, the focused system's ships as
// thin dotted heading trails, and a gate-construction badge chip when the
// detail payload carries construction data. Labels live in a private
// 'system-labels' sub-container inside the shared `labels` layer (the
// orbs/galaxyBand pattern) so other bands' labels are never faded or cleared
// by this band.
//
// buildSystemBand is idempotent per (detail, scene) snapshot: it clears
// everything it owns before drawing. Visibility (250ms cross-fade on
// band === SYSTEM) and the non-focus dimmer are driven by the NebulaScene
// ticker; nothing here animates.
import { Container, Graphics, Sprite, Text, type Renderer } from 'pixi.js';
import type { GateProgress, Waypoint } from '../../types/spacetraders';
import { applyFit, fitToViewport, gateAnchor } from '../../components/flows/drilldownGeometry';
import type { SceneData } from '../sceneData';
import type { SystemDetail } from '../useSystemDetail';
import type { Layers } from './registry';
import { worldBounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';

export interface SystemBandHandle {
  /** System-band label sub-container (child of layers.labels) — fade this, not the shared layer. */
  labels: Container;
}

// Palette (exact — see revamp spec).
const GOLD = 0xe8d9a0;
const CYAN = 0x22d3ee;
/** NOIR.warn — the amber the freshness ramp anchors at 50%. */
const AMBER = 0xf5c518;
const LABEL = 0x8b9cc0;
const SLATE = 0x475569;
const CHIP_BG = 0x0d1220;

/** Px calibration (orbs.ts trick, shifted to the SYSTEM reference zoom): sizes
 * below are screen px at z≈12×fit — the `focusSystem` camera — assuming the
 * fitted span maps to ~1000 viewport px. */
const PX_SPAN = 1000;
const MIN_SPAN = 200;
const SYSTEM_PX_ZOOM = 12;

/** The local chart box: fraction of the world span visible at z=12×fit. */
const CHART_FILL = 0.8;
/** fitToViewport padding as a fraction of the chart box. */
const CHART_PAD_FRAC = 0.06;

const STAR_PX = 12;
const WP_GATE_PX = 5;
const WP_MARKET_PX = 3.5;
const WP_PLAIN_PX = 2.5;

const FRESH_RING_GAP_PX = 3;
const FRESH_RING_WIDTH_PX = 1.2;
const FRESH_DASH_ON_PX = 3;
const FRESH_DASH_OFF_PX = 3;
const FRESH_RING_ALPHA = 0.9;

const SHIP_DOT_PX = 1.4;
const SHIP_MOTE_PX = 2.5;
const SHIP_TRAIL_LEN_PX = 24;
const SHIP_TRAIL_STEP_PX = 3;
/** Ships closer to the system point than this sit "at" it — spread them on a
 * small parking ring so stacked docked hulls stay individually visible. */
const PARKING_RING_PX = 14;

const LABEL_FONT_PX = 7;
const LABEL_GAP_PX = 4;
const CHIP_FONT_PX = 9;
/** Monospace glyph advance as a fraction of the font size — sizes the chip
 * without canvas text measurement (which degraded environments lack). */
const CHIP_GLYPH_ADVANCE = 0.62;
const CHIP_PAD_X_PX = 5;
const CHIP_PAD_Y_PX = 3;
const CHIP_LIFT_PX = 14;

const SYSTEM_LABELS = 'system-labels';

const TEXTURE_RADIUS = 32;
/** Tight bright dot with a soft edge (waypoint orbs, ship motes/trail beads). */
const DOT_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.5, 'rgba(255,255,255,0.9)'],
  [1, 'rgba(255,255,255,0)'],
];
/** Soft halo for the star bloom. */
const HALO_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0.9)'],
  [0.35, 'rgba(255,255,255,0.4)'],
  [1, 'rgba(255,255,255,0)'],
];

// ---- Pure helpers (unit-tested) ---------------------------------------------

/**
 * Freshness ring style from the system's solver-visibility percentage. The
 * boundary is the freshness.ts 50% anchor (its bad→warn / warn→good midpoint):
 * at or above reads fresh (solid cyan), below — or unknown — reads stale
 * (dashed amber).
 */
export function ringStyleForFreshness(pct: number): { color: number; dashed: boolean } {
  return pct >= 50 ? { color: CYAN, dashed: false } : { color: AMBER, dashed: true };
}

/**
 * Gate badge text: `gate NN% · GOOD X/Y` — the aggregate percentage plus the
 * least-complete outstanding material line (the binding constraint; ties break
 * by symbol; a fully-fulfilled bill shows its first line). Percentage falls
 * back to the material sums when the payload's `progress` is null; null when
 * the bill is empty and unstarted (nothing to say).
 */
export function gateBadgeText(gate: GateProgress | null): string | null {
  if (gate == null) return null;
  const mats = gate.materials.filter((m) => m.required > 0);
  let pct = gate.progress;
  if (pct == null && mats.length > 0) {
    const required = mats.reduce((a, m) => a + m.required, 0);
    const fulfilled = mats.reduce((a, m) => a + Math.min(m.fulfilled, m.required), 0);
    pct = required > 0 ? (100 * fulfilled) / required : null;
  }
  if (pct == null) return null;
  const open = mats
    .filter((m) => m.fulfilled < m.required)
    .sort((a, b) => a.fulfilled / a.required - b.fulfilled / b.required || (a.tradeSymbol < b.tradeSymbol ? -1 : 1));
  const line = open[0] ?? mats[0] ?? null;
  const head = `gate ${Math.round(pct)}%`;
  return line ? `${head} · ${line.tradeSymbol} ${line.fulfilled}/${line.required}` : head;
}

// ---- Layer plumbing ----------------------------------------------------------

/** Find-or-create the system label sub-container (find + clear own box only —
 * never removeChild on the shared layer — so other bands' labels are untouched). */
function ensureSystemLabels(labelsLayer: Container): Container {
  const existing = labelsLayer.children.find((c) => c.label === SYSTEM_LABELS);
  if (existing != null) return existing as Container;
  const box = new Container();
  box.label = SYSTEM_LABELS;
  labelsLayer.addChild(box);
  return box;
}

function clearLayer(layer: Container): void {
  for (const child of layer.removeChildren()) child.destroy({ children: true });
}

/** Tear down everything this band owns (focus cleared / band dismantled). */
export function clearSystemBand(layers: Layers): void {
  clearLayer(layers.fx);
  clearLayer(ensureSystemLabels(layers.labels));
}

/** Manual dashed circle (pixi Graphics has no native dash): arc segments of
 * dashOn length separated by dashOff, accumulated onto `g` (stroke it after). */
function dashedCircle(g: Graphics, cx: number, cy: number, r: number, dashOn: number, dashOff: number): void {
  if (r <= 0) return;
  const step = (dashOn + dashOff) / r; // radians per on+off period
  const arc = dashOn / r;
  for (let a = 0; a < Math.PI * 2; a += step) {
    const end = Math.min(a + arc, Math.PI * 2);
    g.moveTo(cx + Math.cos(a) * r, cy + Math.sin(a) * r);
    g.arc(cx, cy, r, a, end);
  }
}

const isMarketplace = (w: Waypoint): boolean =>
  w.hasMarketplace === true || (Array.isArray(w.traits) && w.traits.some((t) => t?.symbol === 'MARKETPLACE'));

/**
 * (Re)build the SYSTEM band for one (detail, scene) snapshot pair: the focused
 * system's interior drawn in-place around its world position. Clears everything
 * this band owns first — call on every snapshot identity change; `scene`
 * supplies the system's world anchor and its resident ships.
 */
export function buildSystemBand(
  layers: Layers,
  detail: SystemDetail,
  focusSymbol: string,
  renderer: Renderer,
  scene: SceneData,
): SystemBandHandle {
  clearLayer(layers.fx);
  const labelBox = ensureSystemLabels(layers.labels);
  clearLayer(labelBox);
  const handle: SystemBandHandle = { labels: labelBox };

  const sys = scene.systems.find((s) => s.symbol === focusSymbol);
  if (sys == null || detail.symbol !== focusSymbol || detail.waypoints.length === 0) return handle;

  // Local chart: the ported fit transform maps star-relative waypoint x/y into
  // a box sized to the world span visible at the z=12×fit focus zoom, centered
  // on the system's world position. Placement is drilldownGeometry, verbatim.
  const fit = worldBounds(scene.fitPoints);
  const span = Math.max(MIN_SPAN, fit.maxX - fit.minX, fit.maxY - fit.minY);
  const worldPerPx = span / PX_SPAN / SYSTEM_PX_ZOOM;
  const box = (span / SYSTEM_PX_ZOOM) * CHART_FILL;
  const t = fitToViewport(detail.waypoints, box, box, box * CHART_PAD_FRAC);
  const ox = sys.x - box / 2;
  const oy = sys.y - box / 2;
  const place = (p: { x: number; y: number }): { x: number; y: number } => {
    const q = applyFit(p, t);
    return { x: q.x + ox, y: q.y + oy };
  };
  const star = place({ x: 0, y: 0 });

  const dotTex = makeGlowTexture(renderer, TEXTURE_RADIUS, DOT_STOPS);
  const haloTex = makeGlowTexture(renderer, TEXTURE_RADIUS, HALO_STOPS);

  // ---- The star: a warm gold bloom at the waypoint-space origin.
  const starHalo = new Sprite(haloTex);
  starHalo.anchor.set(0.5);
  starHalo.position.set(star.x, star.y);
  starHalo.width = STAR_PX * 2 * worldPerPx;
  starHalo.height = STAR_PX * 2 * worldPerPx;
  starHalo.tint = GOLD;
  starHalo.alpha = 0.9;
  starHalo.blendMode = 'screen';
  layers.fx.addChild(starHalo);
  const starCore = new Sprite(dotTex);
  starCore.anchor.set(0.5);
  starCore.position.set(star.x, star.y);
  starCore.width = STAR_PX * worldPerPx;
  starCore.height = STAR_PX * worldPerPx;
  starCore.tint = 0xffffff;
  starCore.blendMode = 'add';
  layers.fx.addChild(starCore);

  // ---- Market freshness rings: one per marketplace waypoint, styled from the
  // system's solver visibility (the granularity the freshness endpoint has —
  // the same record the old drilldown's sensor line displayed).
  const pct = detail.freshness?.freshnessPct ?? Number.NaN;
  const ringStyle = ringStyleForFreshness(pct);
  const freshRings = new Graphics();
  freshRings.label = 'system-freshness';
  const freshR = (WP_MARKET_PX + FRESH_RING_GAP_PX) * worldPerPx;
  for (const w of detail.waypoints) {
    if (!isMarketplace(w)) continue;
    const p = place(w);
    if (ringStyle.dashed) {
      dashedCircle(freshRings, p.x, p.y, freshR, FRESH_DASH_ON_PX * worldPerPx, FRESH_DASH_OFF_PX * worldPerPx);
    } else {
      freshRings.circle(p.x, p.y, freshR);
    }
  }
  freshRings.stroke({ width: FRESH_RING_WIDTH_PX * worldPerPx, color: ringStyle.color, alpha: FRESH_RING_ALPHA });
  layers.fx.addChild(freshRings);

  // ---- Waypoint orbs: gate gold, marketplaces cyan-white, the rest slate.
  for (const w of detail.waypoints) {
    const p = place(w);
    const gate = w.type === 'JUMP_GATE';
    const market = isMarketplace(w);
    const px = gate ? WP_GATE_PX : market ? WP_MARKET_PX : WP_PLAIN_PX;
    const orb = new Sprite(dotTex);
    orb.anchor.set(0.5);
    orb.position.set(p.x, p.y);
    orb.width = px * 2 * worldPerPx;
    orb.height = px * 2 * worldPerPx;
    orb.tint = gate ? GOLD : market ? CYAN : SLATE;
    orb.alpha = gate || market ? 1 : 0.8;
    orb.blendMode = 'add';
    layers.fx.addChild(orb);
  }

  // ---- In-system ship legs: thin dotted trails from each resident ship's
  // position toward its heading (its destination direction). Hulls sitting on
  // the system point (docked — the galaxy motion model parks them there)
  // spread onto a small parking ring so they stay individually visible.
  const parkR = PARKING_RING_PX * worldPerPx;
  const residents = scene.ships.filter((s) => s.system === focusSymbol);
  let parked = 0;
  const parkedTotal = residents.filter((s) => Math.hypot(s.x - sys.x, s.y - sys.y) < parkR).length;
  for (const ship of residents) {
    let sx = ship.x;
    let sy = ship.y;
    if (Math.hypot(sx - sys.x, sy - sys.y) < parkR) {
      const a = (parked / Math.max(1, parkedTotal)) * Math.PI * 2 - Math.PI / 2;
      parked += 1;
      sx = star.x + Math.cos(a) * parkR;
      sy = star.y + Math.sin(a) * parkR;
    }
    const ux = Math.cos(ship.headingRad);
    const uy = Math.sin(ship.headingRad);
    for (let d = SHIP_TRAIL_STEP_PX; d <= SHIP_TRAIL_LEN_PX; d += SHIP_TRAIL_STEP_PX) {
      const dot = new Sprite(dotTex);
      dot.anchor.set(0.5);
      dot.position.set(sx + ux * d * worldPerPx, sy + uy * d * worldPerPx);
      dot.width = SHIP_DOT_PX * 2 * worldPerPx;
      dot.height = SHIP_DOT_PX * 2 * worldPerPx;
      dot.tint = CYAN;
      dot.alpha = 0.75 * (1 - d / (SHIP_TRAIL_LEN_PX + SHIP_TRAIL_STEP_PX));
      dot.blendMode = 'add';
      layers.fx.addChild(dot);
    }
    const mote = new Sprite(dotTex);
    mote.anchor.set(0.5);
    mote.position.set(sx, sy);
    mote.width = SHIP_MOTE_PX * 2 * worldPerPx;
    mote.height = SHIP_MOTE_PX * 2 * worldPerPx;
    mote.tint = 0xffffff;
    mote.blendMode = 'add';
    layers.fx.addChild(mote);
  }

  // ---- Waypoint labels: the waypoint-local suffix under each orb; co-located
  // orbitals ladder downward so stacked names stay readable.
  const stacked = new Map<string, number>();
  for (const w of detail.waypoints) {
    const p = place(w);
    const key = `${Math.round(p.x / worldPerPx)}|${Math.round(p.y / worldPerPx)}`;
    const dup = stacked.get(key) ?? 0;
    stacked.set(key, dup + 1);
    const short = w.symbol.split('-').slice(2).join('-') || w.symbol;
    const text = new Text({
      text: short,
      style: {
        fontFamily: 'monospace',
        fontSize: LABEL_FONT_PX,
        fill: LABEL,
        dropShadow: { color: 0x070312, alpha: 1, blur: 2, distance: 0, angle: 0 },
      },
      resolution: 3,
    });
    text.anchor.set(0.5, 0);
    text.scale.set(worldPerPx);
    text.position.set(p.x, p.y + (WP_GATE_PX + LABEL_GAP_PX + dup * (LABEL_FONT_PX + 1)) * worldPerPx);
    labelBox.addChild(text);
  }

  // ---- Gate-site badge: `gate NN% · GOOD X/Y` gold on a dark chip, above the
  // construction waypoint (fallback: the jump gate anchor), when the detail
  // payload carries construction data.
  const badge = gateBadgeText(detail.gate);
  if (badge != null) {
    const site = detail.waypoints.find((w) => w.isUnderConstruction) ?? null;
    const anchor = site != null ? place(site) : (() => {
      const g = gateAnchor(detail.waypoints);
      return g != null ? place(g) : star;
    })();
    const chip = new Container();
    chip.label = 'gate-badge';
    const text = new Text({
      text: badge,
      style: { fontFamily: 'monospace', fontSize: CHIP_FONT_PX, fill: GOLD },
      resolution: 3,
    });
    text.anchor.set(0.5);
    const bg = new Graphics();
    const w = badge.length * CHIP_FONT_PX * CHIP_GLYPH_ADVANCE + CHIP_PAD_X_PX * 2;
    const h = CHIP_FONT_PX + CHIP_PAD_Y_PX * 2;
    bg.roundRect(-w / 2, -h / 2, w, h, 3);
    bg.fill({ color: CHIP_BG, alpha: 0.92 });
    bg.stroke({ width: 0.8, color: GOLD, alpha: 0.55 });
    chip.addChild(bg);
    chip.addChild(text);
    chip.scale.set(worldPerPx);
    chip.position.set(anchor.x, anchor.y - CHIP_LIFT_PX * worldPerPx);
    labelBox.addChild(chip);
  }

  return handle;
}
