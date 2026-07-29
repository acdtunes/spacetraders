import { describe, it, expect } from 'vitest';
import * as PIXI from 'pixi.js';

describe('pixi.js v8 availability', () => {
  it('exports Application and Container (v8 API)', () => {
    expect(typeof PIXI.Application).toBe('function');
    expect(typeof PIXI.Container).toBe('function');
    // v8 signature: init() is an instance method (v7 had constructor options only)
    expect(typeof PIXI.Application.prototype.init).toBe('function');
  });
});
