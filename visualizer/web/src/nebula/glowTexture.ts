// Radial-gradient glow textures for every soft light in the nebula (backdrop
// fog, orb auras, ship trails — Tasks 7/8/9/11 all draw with these). One canvas
// per distinct (radius, stops) key, cached for the lifetime of the tab: pixi v8
// textures are source-backed, so they survive app re-creation (StrictMode
// remounts) and are lazily re-uploaded to whatever context renders next.
import { Texture, type Renderer } from 'pixi.js';

/** Cache key — pure, exported for unit tests. */
export function glowKey(radius: number, stops: [number, string][]): string {
  return `${radius}|${stops.map(([o, c]) => `${o}:${c}`).join(',')}`;
}

const cache = new Map<string, Texture>();

/**
 * White-to-transparent (or whatever `stops` says) radial gradient as a texture.
 * Tint the consuming Sprite to color it — that way one cached texture serves
 * every hue. `renderer` is used only to eagerly upload the pixels so the first
 * frame with a large glow doesn't hitch; texture creation itself is
 * renderer-independent in pixi v8.
 */
export function makeGlowTexture(renderer: Renderer, radius: number, stops: [number, string][]): Texture {
  const key = glowKey(radius, stops);
  const hit = cache.get(key);
  if (hit != null && !hit.destroyed) return hit;

  const size = Math.max(2, Math.ceil(radius * 2));
  const canvas = document.createElement('canvas');
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext('2d');
  if (ctx == null) return Texture.WHITE; // degraded env (jsdom, no 2d): plain beats a crash

  const grad = ctx.createRadialGradient(size / 2, size / 2, 0, size / 2, size / 2, size / 2);
  for (const [offset, color] of stops) grad.addColorStop(offset, color);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, size, size);

  const tex = Texture.from(canvas);
  cache.set(key, tex);
  // Eager upload (guarded: absent on stub/degraded renderers).
  (renderer as { texture?: { initSource?: (s: unknown) => void } }).texture?.initSource?.(tex.source);
  return tex;
}
