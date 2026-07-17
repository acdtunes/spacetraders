import { NOIR } from '../../theme/noir';

export interface RingSpec {
  fill: number;              // 0..1 arc sweep
  color: string;
  underGlow: string | null;  // red while capital is committed (net < 0)
  overshoot: boolean;        // realized beat the projection
}

// Realized-vs-projected as a glanceable ring. Realized is a SIGNED sum, so a
// young tour is negative (cargo bought, nothing sold): that renders as an
// empty ring with a red under-glow, not as fake progress.
export function ringSpec(realizedNet: number | null | undefined, projectedProfit: number | null | undefined): RingSpec {
  const projected = projectedProfit ?? 0;
  if (projected <= 0) return { fill: 0, color: NOIR.dim, underGlow: null, overshoot: false };
  const net = realizedNet ?? 0;
  if (net < 0) return { fill: 0, color: NOIR.warn, underGlow: NOIR.bad, overshoot: false };
  const ratio = net / projected;
  if (ratio >= 1) return { fill: 1, color: NOIR.good, underGlow: null, overshoot: true };
  return { fill: ratio, color: ratio < 0.6 ? NOIR.warn : NOIR.good, underGlow: null, overshoot: false };
}
