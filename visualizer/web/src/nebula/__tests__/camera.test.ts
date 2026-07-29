import { describe, it, expect } from 'vitest';
import { worldBounds, fitTransform, clampScale, anchoredZoom, lerpCam, easeInOutCubic } from '../camera';

describe('camera', () => {
  const pts = [{ x: -100, y: 0 }, { x: 300, y: 200 }];
  it('worldBounds pads the extent', () => {
    expect(worldBounds(pts, 50)).toEqual({ minX: -150, minY: -50, maxX: 350, maxY: 250 });
  });
  it('fitTransform fits and centers the bounds in the viewport', () => {
    const cam = fitTransform(worldBounds(pts, 0), 800, 400);
    // world w=400,h=200 → scale = min(800/400, 400/200) = 2; center (100,100) → screen center (400,200)
    expect(cam.scale).toBe(2);
    expect(cam.x).toBe(400 - 100 * 2);
    expect(cam.y).toBe(200 - 100 * 2);
  });
  it('clampScale bounds to [fit×0.5, fit×40]', () => {
    expect(clampScale(0.01, 0.1)).toBe(0.05);
    expect(clampScale(10, 0.1)).toBe(4);
    expect(clampScale(1, 0.1)).toBe(1);
  });
  it('anchoredZoom keeps the world point under the pointer fixed', () => {
    const cam = { x: 0, y: 0, scale: 1 };
    const out = anchoredZoom(cam, 100, 50, 2, 1); // zoom in ×2 at (100,50)
    // world point under pointer was (100,50); after: screen = world*2 + t ⇒ t = 100-200=-100, 50-100=-50
    expect(out).toEqual({ x: -100, y: -50, scale: 2 });
  });
  it('lerpCam eases between transforms', () => {
    const a = { x: 0, y: 0, scale: 1 }, b = { x: 100, y: 100, scale: 3 };
    expect(lerpCam(a, b, 0)).toEqual(a);
    expect(lerpCam(a, b, 1)).toEqual(b);
    const mid = lerpCam(a, b, 0.5);
    expect(mid.x).toBeCloseTo(50); expect(mid.scale).toBeCloseTo(2);
  });
  it('easeInOutCubic endpoints and midpoint', () => {
    expect(easeInOutCubic(0)).toBe(0); expect(easeInOutCubic(1)).toBe(1);
    expect(easeInOutCubic(0.5)).toBeCloseTo(0.5);
  });
});
