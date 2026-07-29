// Backdrop layer: a seeded starfield behind everything, plus one soft fog
// sprite per cluster (violet/cyan alternating). Pure placement (starSeeds) is
// unit-tested; buildBackdrop is idempotent — it clears its layer before drawing
// so a rebuild per SceneData snapshot never stacks or leaks.
import { Sprite, type Container, type Renderer } from 'pixi.js';
import type { SceneData } from '../sceneData';
import { worldBounds, type Bounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';

export interface StarSeed { x: number; y: number; r: number; alpha: number }

export const STAR_COUNT = 600;
/** Starfield spreads past the fitted systems so pans never hit a hard edge. */
const BOUNDS_SCALE = 1.5;
const MIN_SPAN = 200; // degenerate (single-system) scenes still get a sky

// FNV-1a — same recipe as server/utils/galaxyLayout.ts hashSymbol, over the
// sorted symbol set: the sky is a function of WHICH systems exist, not of the
// order the poll happened to return them in.
function fnv1a(str: string): number {
  let h = 2166136261;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

// Deterministic PRNG (mulberry32) — no Math.random anywhere in the backdrop.
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Pure, deterministic star placement. Per star, draws in order: x, y, r, alpha. */
export function starSeeds(symbols: string[], count: number, bounds: Bounds): StarSeed[] {
  const rand = mulberry32(fnv1a([...symbols].sort().join('|')));
  const w = bounds.maxX - bounds.minX;
  const h = bounds.maxY - bounds.minY;
  const seeds: StarSeed[] = [];
  for (let i = 0; i < count; i++) {
    seeds.push({
      x: bounds.minX + rand() * w,
      y: bounds.minY + rand() * h,
      r: 0.6 + rand() * 1.8,
      alpha: 0.25 + rand() * 0.55,
    });
  }
  return seeds;
}

const STAR_TEXTURE_RADIUS = 8;
const STAR_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.4, 'rgba(255,255,255,0.9)'],
  [1, 'rgba(255,255,255,0)'],
];
const FOG_TEXTURE_RADIUS = 256;
const FOG_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,0.6)'],
  [0.45, 'rgba(255,255,255,0.3)'],
  [1, 'rgba(255,255,255,0)'],
];
const FOG_TINTS = [0x8b5cf6, 0x22d3ee] as const; // violet / cyan, alternating by cluster index
const FOG_ALPHA_MIN = 0.1;
const FOG_ALPHA_MAX = 0.22;
const FOG_MIN_RADIUS = 60;
const FOG_RADIUS_PAD = 1.2; // fog reaches a little past the outermost member
/** Cap fog radius vs the fitted half-span so a dozen clusters read as patches, not one wash. */
const FOG_MAX_RADIUS_FRAC = 0.28;
/** seed.r is calibrated for a ~1200-unit world; scale it to the actual span. */
const STAR_SIZE_SPAN = 1200;

/**
 * (Re)build the backdrop into `layer` for one SceneData snapshot. Clears any
 * previous build first — call it on every snapshot identity change without
 * worrying about stacking. Fog goes in before stars so stars sit on top.
 */
export function buildBackdrop(layer: Container, data: SceneData, renderer: Renderer): void {
  for (const child of layer.removeChildren()) child.destroy({ children: true });
  if (data.systems.length === 0) return;

  const fit = worldBounds(data.fitPoints);
  const cx = (fit.minX + fit.maxX) / 2;
  const cy = (fit.minY + fit.maxY) / 2;
  const span = Math.max(MIN_SPAN, fit.maxX - fit.minX, fit.maxY - fit.minY);
  const halfW = (Math.max(MIN_SPAN, fit.maxX - fit.minX) * BOUNDS_SCALE) / 2;
  const halfH = (Math.max(MIN_SPAN, fit.maxY - fit.minY) * BOUNDS_SCALE) / 2;
  const starBounds: Bounds = { minX: cx - halfW, minY: cy - halfH, maxX: cx + halfW, maxY: cy + halfH };

  // Cluster fog. Alpha scales with the cluster's share of |activity|
  // (FOG_ALPHA_MIN floor keeps quiet clusters faintly visible).
  const fogTex = makeGlowTexture(renderer, FOG_TEXTURE_RADIUS, FOG_STOPS);
  const bySymbol = new Map(data.systems.map((s) => [s.symbol, s]));
  const totalActivity = data.systems.reduce((sum, s) => sum + Math.abs(s.activity), 0);
  data.clusters.forEach((cluster, i) => {
    let reach = 0;
    let activity = 0;
    for (const m of cluster.members) {
      const sys = bySymbol.get(m);
      if (sys == null) continue;
      reach = Math.max(reach, Math.hypot(sys.x - cluster.cx, sys.y - cluster.cy));
      activity += Math.abs(sys.activity);
    }
    const radius = Math.min(
      span * FOG_MAX_RADIUS_FRAC,
      Math.max(FOG_MIN_RADIUS, reach * FOG_RADIUS_PAD),
    );
    const share = totalActivity > 0 ? activity / totalActivity : 0;
    const fog = new Sprite(fogTex);
    fog.anchor.set(0.5);
    fog.position.set(cluster.cx, cluster.cy);
    fog.width = radius * 2;
    fog.height = radius * 2;
    fog.tint = FOG_TINTS[i % FOG_TINTS.length];
    fog.alpha = Math.min(FOG_ALPHA_MAX, FOG_ALPHA_MIN + share * 0.24);
    // 'screen' (not 'add'): a dozen overlapping fogs soften instead of summing
    // into one washed-out mega-blob.
    fog.blendMode = 'screen';
    layer.addChild(fog);
  });

  // Starfield — one tiny shared texture, per-seed size/alpha. Seed radii are
  // span-relative so stars stay ~1-6 screen px at fit zoom on any galaxy size.
  const starTex = makeGlowTexture(renderer, STAR_TEXTURE_RADIUS, STAR_STOPS);
  const starScale = Math.max(1, span / STAR_SIZE_SPAN);
  for (const seed of starSeeds(data.systems.map((s) => s.symbol), STAR_COUNT, starBounds)) {
    const star = new Sprite(starTex);
    star.anchor.set(0.5);
    star.position.set(seed.x, seed.y);
    star.width = seed.r * starScale * 2;
    star.height = seed.r * starScale * 2;
    star.alpha = seed.alpha;
    star.blendMode = 'add';
    layer.addChild(star);
  }
}
