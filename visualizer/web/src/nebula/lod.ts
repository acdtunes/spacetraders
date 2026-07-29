export type Band = 'GALAXY' | 'REGION' | 'SYSTEM';
export const REGION_ENTER = 2.2;
export const REGION_EXIT = 1.8;
export const SYSTEM_ENTER = 9.0;
export const SYSTEM_EXIT = 7.5;

// Hysteresis: entering a deeper band needs z ≥ *_ENTER; leaving it needs z < *_EXIT.
// Inside the gap the previous band wins, so a wheel hovering a boundary never flickers.
export function bandFor(scale: number, fitScale: number, prev: Band | null): Band {
  if (!isFinite(fitScale) || fitScale <= 0) return 'GALAXY';
  const z = scale / fitScale;
  if (prev === 'SYSTEM') return z < SYSTEM_EXIT ? (z < REGION_EXIT ? 'GALAXY' : 'REGION') : 'SYSTEM';
  if (prev === 'REGION') {
    if (z >= SYSTEM_ENTER) return 'SYSTEM';
    return z < REGION_EXIT ? 'GALAXY' : 'REGION';
  }
  // prev GALAXY or null → enter thresholds
  if (z >= SYSTEM_ENTER) return 'SYSTEM';
  if (z >= REGION_ENTER) return 'REGION';
  return 'GALAXY';
}
