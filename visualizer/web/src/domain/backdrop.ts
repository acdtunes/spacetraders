export interface BackdropStar { x: number; y: number; r: number; a: number }

// Mulberry32 — tiny deterministic PRNG so the field never "re-rolls" between renders.
function mulberry32(seed: number) {
  let t = seed >>> 0;
  return () => {
    t += 0x6d2b79f5;
    let r = Math.imul(t ^ (t >>> 15), 1 | t);
    r ^= r + Math.imul(r ^ (r >>> 7), 61 | r);
    return ((r ^ (r >>> 14)) >>> 0) / 4294967296;
  };
}

export function starfield(seed: number, count: number, w: number, h: number): BackdropStar[] {
  const rnd = mulberry32(seed);
  return Array.from({ length: count }, () => ({
    x: rnd() * w,
    y: rnd() * h,
    r: 0.4 + rnd() * 1.1,
    a: 0.25 + rnd() * 0.75,
  }));
}

// Screen-space parallax: shift each star layer by a fraction of the layer's
// on-screen translation (world-origin position). A larger factor = a nearer
// layer that moves more. Because the input is screen-space it responds to pan
// (drag/keyboard/programmatic) and to zoom (which moves the on-screen origin),
// and callers clamp the result to the canvas bleed so it can never expose an edge.
export function parallaxOffset(pan: { x: number; y: number }, factor: number): { x: number; y: number } {
  return { x: pan.x * factor, y: pan.y * factor };
}
