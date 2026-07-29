import { describe, it, expect } from 'vitest';
import type { Renderer } from 'pixi.js';
import { glowKey, makeGlowTexture } from '../glowTexture';

const STOPS: [number, string][] = [
  [0, 'rgba(255,255,255,1)'],
  [1, 'rgba(255,255,255,0)'],
];

describe('glowKey', () => {
  it('is stable for identical inputs and distinguishes radius/stops', () => {
    expect(glowKey(64, STOPS)).toBe(glowKey(64, STOPS));
    expect(glowKey(32, STOPS)).not.toBe(glowKey(64, STOPS));
    expect(glowKey(64, [[0, '#fff'], [1, '#000']])).not.toBe(glowKey(64, STOPS));
  });
});

describe('makeGlowTexture', () => {
  it('returns a texture and the SAME instance for identical inputs (cached by key)', () => {
    // Renderer only eager-uploads (optional-chained), so a bare stub is fine —
    // and the cache-identity contract holds in both the canvas-2d and the
    // degraded (jsdom, ctx null → Texture.WHITE) paths.
    const renderer = {} as Renderer;
    const a = makeGlowTexture(renderer, 16, STOPS);
    const b = makeGlowTexture(renderer, 16, STOPS);
    expect(a).toBeTruthy();
    expect(b).toBe(a);
  });
});
