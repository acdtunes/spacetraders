// REGION/SYSTEM band motion: lane particle streams + ship motes. All lane
// particles live in ONE pixi v8 ParticleContainer (lightweight Particle
// entries, position+color dynamic) inside `layers.lanes`; ships are per-ship
// mote+streak sprite pairs inside `layers.ships`. This band owns both layers
// outright. Visibility (the REGION fade in NebulaScene) and advancement
// (handle.update) are driven by the NebulaScene ticker; nothing here animates
// on its own.
//
// buildStreams is idempotent per snapshot: it clears everything it owns before
// drawing. Pass the previous handle back in so ship velocities can be
// dead-reckoned from the last two snapshot positions (0 until two exist).
import { Container, Particle, ParticleContainer, Sprite, type Renderer } from 'pixi.js';
import type { SceneData, SceneLane } from '../sceneData';
import type { Layers } from './registry';
import { worldBounds } from '../camera';
import { makeGlowTexture } from '../glowTexture';

// Palette (exact — see revamp spec).
const CYAN = 0x22d3ee;
const VIOLET = 0xa78bfa;
const EMBER = 0xef4444;

/** Global particle budget across every lane stream. */
export const PARTICLE_CAP = 5000;
/** Minimum particles per active lane (when 4·laneCount fits the cap). */
export const LANE_PARTICLE_MIN = 4;

/** Px calibration (orbs.ts trick): sizes are screen px at the REGION reference
 * zoom (z≈3×fit), assuming the fitted span maps to ~1000 viewport px. */
const PX_SPAN = 1000;
const MIN_SPAN = 200;
const REGION_PX_ZOOM = 3;

const LANE_PARTICLE_PX = 2;
const SHIP_MOTE_PX = 3;
const SHIP_STREAK_LEN_PX = 6;
const SHIP_STREAK_THICK_PX = 1.6;

/** Lane progress speed in lane-fractions/sec ∝ |profit/hr| share, clamped. */
const LANE_SPEED_MIN = 0.05;
const LANE_SPEED_MAX = 0.3;

/** Ship dead-reckoning sanity clamps: ignore snapshot pairs closer than this
 * (velocity from a near-zero dt explodes), and cap speed at a fraction of the
 * galaxy span per second (a poll glitch must not rocket a mote off screen). */
const SHIP_MIN_SNAP_DT_MS = 100;
const SHIP_MAX_SPEED_SPAN_FRAC = 0.25;

const PARTICLE_TEXTURE_RADIUS = 16; // 32px canvas, scaled down to px sizes above
/** Round dot with a soft edge — reads as a 2px bead, not a fuzzy blob. */
const DOT_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.55, 'rgba(255,255,255,0.9)'],
  [1, 'rgba(255,255,255,0)'],
];
/** Softer falloff for the trailing heading streak. */
const STREAK_STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [0.35, 'rgba(255,255,255,0.7)'],
  [1, 'rgba(255,255,255,0)'],
];

/** Budget key for a directed lane. */
export function laneKey(from: string, to: string): string {
  return `${from}|${to}`;
}

/**
 * Pure particle allocation: proportional to volume, total ≤ cap, every active
 * lane ≥ LANE_PARTICLE_MIN when that floor fits the cap. Duplicate directed
 * lanes merge their volume under one key; empty input → empty map.
 *
 * Reallocation rule (documented for the tests): allocations start as
 * floor(cap·volume/totalVolume); lanes below the 4 floor are raised to 4, and
 * the overshoot (if the raised total exceeds cap) is taken back from the
 * largest allocations first (ties broken by key), never below 4. When
 * 4·laneCount > cap the floor is unaffordable — the cap wins and every lane
 * gets the even split floor(cap/laneCount). Flooring slack is NOT
 * redistributed, keeping the proportional pin (10/30 @ cap 100 → 25/75) exact.
 */
export function particleBudget(lanes: SceneLane[], cap: number): Map<string, number> {
  const out = new Map<string, number>();
  if (lanes.length === 0 || !(cap > 0)) return out;

  // Aggregate volume per directed key (duplicates merge; negatives clamp to 0).
  const volumes = new Map<string, number>();
  for (const l of lanes) {
    const key = laneKey(l.from, l.to);
    volumes.set(key, (volumes.get(key) ?? 0) + Math.max(0, l.volume));
  }

  const n = volumes.size;
  if (LANE_PARTICLE_MIN * n > cap) {
    const even = Math.floor(cap / n);
    for (const key of volumes.keys()) out.set(key, even);
    return out;
  }

  let totalVolume = 0;
  for (const v of volumes.values()) totalVolume += v;

  let total = 0;
  for (const [key, v] of volumes) {
    const share = totalVolume > 0 ? Math.floor((cap * v) / totalVolume) : 0;
    const alloc = Math.max(LANE_PARTICLE_MIN, share);
    out.set(key, alloc);
    total += alloc;
  }

  // Pay the floor back from the largest allocations (deterministic order).
  let overshoot = total - cap;
  if (overshoot > 0) {
    const desc = [...out.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1));
    for (const [key, alloc] of desc) {
      if (overshoot <= 0) break;
      const take = Math.min(alloc - LANE_PARTICLE_MIN, overshoot);
      if (take > 0) {
        out.set(key, alloc - take);
        overshoot -= take;
      }
    }
  }
  return out;
}

/** Per-channel linear blend of two 0xRRGGBB tints (rounded). Pure. */
export function lerpTint(a: number, b: number, t: number): number {
  const ar = (a >> 16) & 0xff, ag = (a >> 8) & 0xff, ab = a & 0xff;
  const br = (b >> 16) & 0xff, bg = (b >> 8) & 0xff, bb = b & 0xff;
  const r = Math.round(ar + (br - ar) * t);
  const g = Math.round(ag + (bg - ag) * t);
  const bl = Math.round(ab + (bb - ab) * t);
  return (r << 16) | (g << 8) | bl;
}

/** Last-snapshot ship position (world) + the wall-clock it was taken at. */
export interface ShipSnap { x: number; y: number; atMs: number }

export interface StreamsHandle {
  /** Advance lane particles + dead-reckoned ships. Ticker calls this only while the REGION fade is visible. */
  update(dtMs: number): void;
  /** Per-ship snapshot state — pass this handle as `prev` to the next buildStreams so velocities persist across rebuilds. */
  shipSnaps: Map<string, ShipSnap>;
}

interface LaneStream {
  x0: number; y0: number; dx: number; dy: number;
  reversed: boolean; ember: boolean; speed: number;
  ts: Float64Array;
  particles: Particle[];
}

interface ShipMote {
  mote: Sprite; streak: Sprite;
  x: number; y: number; vx: number; vy: number;
}

function clearLayer(layer: Container): void {
  for (const child of layer.removeChildren()) child.destroy({ children: true });
}

const nowMs = (): number => (typeof performance !== 'undefined' ? performance.now() : Date.now());

/**
 * (Re)build lane streams + ship motes for one SceneData snapshot. Clears the
 * `lanes` and `ships` layers first — call on every snapshot identity change,
 * threading the previous handle through `prev` for ship dead-reckoning.
 */
export function buildStreams(
  layers: Layers,
  data: SceneData,
  renderer: Renderer,
  prev?: StreamsHandle,
): StreamsHandle {
  clearLayer(layers.lanes);
  clearLayer(layers.ships);

  const streams: LaneStream[] = [];
  const motes: ShipMote[] = [];
  const shipSnaps = new Map<string, ShipSnap>();
  const handle: StreamsHandle = {
    shipSnaps,
    update(dtMs: number) {
      const dt = dtMs / 1000;
      for (const s of streams) {
        for (let i = 0; i < s.particles.length; i++) {
          const t = (s.ts[i] + s.speed * dt) % 1;
          s.ts[i] = t;
          const u = s.reversed ? 1 - t : t;
          const p = s.particles[i];
          p.x = s.x0 + s.dx * u;
          p.y = s.y0 + s.dy * u;
          if (!s.ember) p.tint = lerpTint(CYAN, VIOLET, u);
        }
      }
      for (const m of motes) {
        m.x += m.vx * dt;
        m.y += m.vy * dt;
        m.mote.position.set(m.x, m.y);
        m.streak.position.set(m.x, m.y);
      }
    },
  };
  if (data.systems.length === 0) return handle;

  const fit = worldBounds(data.fitPoints);
  const span = Math.max(MIN_SPAN, fit.maxX - fit.minX, fit.maxY - fit.minY);
  const worldPerPx = span / PX_SPAN / REGION_PX_ZOOM;
  const bySymbol = new Map(data.systems.map((s) => [s.symbol, s]));
  const dotTex = makeGlowTexture(renderer, PARTICLE_TEXTURE_RADIUS, DOT_STOPS);
  const streakTex = makeGlowTexture(renderer, PARTICLE_TEXTURE_RADIUS, STREAK_STOPS);
  const texSize = PARTICLE_TEXTURE_RADIUS * 2;

  // ---- Lane particles: one ParticleContainer for every lane's beads.
  const budget = particleBudget(data.lanes, PARTICLE_CAP);
  const maxAbsPph = data.lanes.reduce((m, l) => Math.max(m, Math.abs(l.profitPerHr)), 0);
  const pc = new ParticleContainer({
    dynamicProperties: { position: true, color: true },
  });
  pc.label = 'lane-particles';
  pc.blendMode = 'add';
  layers.lanes.addChild(pc);

  const particleScale = (LANE_PARTICLE_PX * worldPerPx) / texSize;
  const built = new Set<string>();
  for (const lane of data.lanes) {
    const key = laneKey(lane.from, lane.to);
    if (built.has(key)) continue; // duplicates merged into the first record's stream
    built.add(key);
    const count = budget.get(key) ?? 0;
    if (count <= 0) continue;
    const a = bySymbol.get(lane.from);
    const b = bySymbol.get(lane.to);
    if (a == null || b == null) continue;
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    if (Math.hypot(dx, dy) < 1e-6) continue;

    const share = maxAbsPph > 0 ? Math.abs(lane.profitPerHr) / maxAbsPph : 0;
    const ember = lane.profitPerHr < 0;
    const stream: LaneStream = {
      x0: a.x, y0: a.y, dx, dy,
      reversed: ember,
      ember,
      speed: LANE_SPEED_MIN + (LANE_SPEED_MAX - LANE_SPEED_MIN) * share,
      ts: new Float64Array(count),
      particles: [],
    };
    for (let i = 0; i < count; i++) {
      const t = i / count;
      stream.ts[i] = t;
      const u = stream.reversed ? 1 - t : t;
      const particle = new Particle({
        texture: dotTex,
        x: a.x + dx * u,
        y: a.y + dy * u,
        scaleX: particleScale,
        scaleY: particleScale,
        anchorX: 0.5,
        anchorY: 0.5,
        tint: ember ? EMBER : lerpTint(CYAN, VIOLET, u),
        alpha: 0.85,
      });
      pc.addParticle(particle);
      stream.particles.push(particle);
    }
    streams.push(stream);
  }

  // ---- Ship motes: white-hot 3px bead + a cyan 6px streak trailing opposite
  // the heading (anchor at the streak's tip). Velocity is dead-reckoned along
  // headingRad at the speed implied by the last two snapshot positions.
  const at = nowMs();
  const maxSpeed = span * SHIP_MAX_SPEED_SPAN_FRAC;
  for (const ship of data.ships) {
    const last = prev?.shipSnaps.get(ship.id);
    let vx = 0;
    let vy = 0;
    if (last != null && at - last.atMs >= SHIP_MIN_SNAP_DT_MS) {
      const dtSec = (at - last.atMs) / 1000;
      const speed = Math.min(maxSpeed, Math.hypot(ship.x - last.x, ship.y - last.y) / dtSec);
      vx = Math.cos(ship.headingRad) * speed;
      vy = Math.sin(ship.headingRad) * speed;
    }
    shipSnaps.set(ship.id, { x: ship.x, y: ship.y, atMs: at });

    const streak = new Sprite(streakTex);
    streak.anchor.set(1, 0.5);
    streak.position.set(ship.x, ship.y);
    streak.width = SHIP_STREAK_LEN_PX * worldPerPx;
    streak.height = SHIP_STREAK_THICK_PX * worldPerPx;
    streak.rotation = ship.headingRad;
    streak.tint = CYAN;
    streak.alpha = 0.75;
    streak.blendMode = 'add';
    layers.ships.addChild(streak);

    const mote = new Sprite(dotTex);
    mote.anchor.set(0.5);
    mote.position.set(ship.x, ship.y);
    mote.width = SHIP_MOTE_PX * worldPerPx;
    mote.height = SHIP_MOTE_PX * worldPerPx;
    mote.tint = 0xffffff;
    mote.alpha = 1;
    mote.blendMode = 'add';
    layers.ships.addChild(mote);

    motes.push({ mote, streak, x: ship.x, y: ship.y, vx, vy });
  }

  return handle;
}
