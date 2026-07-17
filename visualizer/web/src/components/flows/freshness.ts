import { NOIR, noirRgb } from '../../theme/noir';

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

function lerpRgb(a: string, b: string, t: number): string {
  const ca = noirRgb(a);
  const cb = noirRgb(b);
  const r = Math.round(ca.r + (cb.r - ca.r) * t);
  const g = Math.round(ca.g + (cb.g - ca.g) * t);
  const bl = Math.round(ca.b + (cb.b - ca.b) * t);
  return `rgb(${r}, ${g}, ${bl})`;
}

// Continuous solver-visibility ramp over the existing NOIR tokens:
// 0% bad (dead red) -> 50% warn (amber) -> 100% good (green).
export function freshnessColor(pct: number): string {
  const p = clamp(pct, 0, 100);
  return p <= 50 ? lerpRgb(NOIR.bad, NOIR.warn, p / 50) : lerpRgb(NOIR.warn, NOIR.good, (p - 50) / 50);
}

// Center alpha of the halo: green coverage glows brighter than red decay.
export function haloAlpha(pct: number): number {
  return 0.18 + 0.27 * (clamp(pct, 0, 100) / 100);
}
