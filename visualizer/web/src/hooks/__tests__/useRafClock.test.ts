import { describe, it, expect, vi, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useRafClock } from '../useRafClock';

function mockMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })) as unknown as typeof window.matchMedia;
}

describe('useRafClock', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('returns a current timestamp on first render', () => {
    mockMatchMedia(false);
    vi.stubGlobal('requestAnimationFrame', () => 1);
    vi.stubGlobal('cancelAnimationFrame', () => {});
    const { result } = renderHook(() => useRafClock());
    expect(typeof result.current).toBe('number');
    expect(result.current).toBeGreaterThan(0);
  });

  it('advances to Date.now() on each animation frame when motion is allowed', () => {
    mockMatchMedia(false);
    let frame: FrameRequestCallback | null = null;
    vi.stubGlobal('requestAnimationFrame', (fn: FrameRequestCallback) => { frame = fn; return 1; });
    vi.stubGlobal('cancelAnimationFrame', () => {});
    const nowSpy = vi.spyOn(Date, 'now').mockReturnValue(1000);

    const { result } = renderHook(() => useRafClock());
    expect(result.current).toBe(1000);

    nowSpy.mockReturnValue(2500);
    act(() => { frame?.(0); });
    expect(result.current).toBe(2500);
  });

  it('falls back to a 1s interval (not per-frame) under prefers-reduced-motion', () => {
    vi.useFakeTimers();
    mockMatchMedia(true);
    const rafSpy = vi.fn(() => 1);
    vi.stubGlobal('requestAnimationFrame', rafSpy);
    const nowSpy = vi.spyOn(Date, 'now').mockReturnValue(5000);

    const { result } = renderHook(() => useRafClock());
    expect(result.current).toBe(5000);

    nowSpy.mockReturnValue(6000);
    act(() => { vi.advanceTimersByTime(1000); });
    expect(result.current).toBe(6000);
    expect(rafSpy).not.toHaveBeenCalled(); // steady tick, no animation frames
  });
});
