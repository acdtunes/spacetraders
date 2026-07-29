export interface CamXform { x: number; y: number; scale: number }
export interface Bounds { minX: number; minY: number; maxX: number; maxY: number }

export function worldBounds(points: { x: number; y: number }[], pad = 0): Bounds {
  if (points.length === 0) return { minX: -1, minY: -1, maxX: 1, maxY: 1 };
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const p of points) {
    if (p.x < minX) minX = p.x; if (p.y < minY) minY = p.y;
    if (p.x > maxX) maxX = p.x; if (p.y > maxY) maxY = p.y;
  }
  return { minX: minX - pad, minY: minY - pad, maxX: maxX + pad, maxY: maxY + pad };
}

export function fitTransform(b: Bounds, viewportW: number, viewportH: number): CamXform {
  const w = Math.max(1e-6, b.maxX - b.minX), h = Math.max(1e-6, b.maxY - b.minY);
  const scale = Math.min(viewportW / w, viewportH / h);
  const cx = (b.minX + b.maxX) / 2, cy = (b.minY + b.maxY) / 2;
  return { x: viewportW / 2 - cx * scale, y: viewportH / 2 - cy * scale, scale };
}

export const MIN_FIT_RATIO = 0.5;
export const MAX_FIT_RATIO = 40;
export function clampScale(scale: number, fitScale: number): number {
  return Math.max(fitScale * MIN_FIT_RATIO, Math.min(fitScale * MAX_FIT_RATIO, scale));
}

export function anchoredZoom(cam: CamXform, px: number, py: number, factor: number, fitScale: number): CamXform {
  const wx = (px - cam.x) / cam.scale, wy = (py - cam.y) / cam.scale;
  const scale = clampScale(cam.scale * factor, fitScale);
  return { x: px - wx * scale, y: py - wy * scale, scale };
}

export function easeInOutCubic(t: number): number {
  return t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
}

export function lerpCam(a: CamXform, b: CamXform, t: number): CamXform {
  const e = easeInOutCubic(Math.max(0, Math.min(1, t)));
  return { x: a.x + (b.x - a.x) * e, y: a.y + (b.y - a.y) * e, scale: a.scale + (b.scale - a.scale) * e };
}
