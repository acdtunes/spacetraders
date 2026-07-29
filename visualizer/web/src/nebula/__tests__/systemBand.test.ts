import { describe, it, expect } from 'vitest';
import { ringStyleForFreshness, gateBadgeText } from '../layers/systemBand';
import type { GateProgress } from '../../types/spacetraders';

describe('ringStyleForFreshness', () => {
  it('fresh (pct at/above the freshness.ts warn anchor) is solid cyan', () => {
    expect(ringStyleForFreshness(50)).toEqual({ color: 0x22d3ee, dashed: false });
    expect(ringStyleForFreshness(100)).toEqual({ color: 0x22d3ee, dashed: false });
  });

  it('stale (below the anchor) is dashed amber', () => {
    expect(ringStyleForFreshness(49)).toEqual({ color: 0xf5c518, dashed: true });
    expect(ringStyleForFreshness(0)).toEqual({ color: 0xf5c518, dashed: true });
  });

  it('unknown visibility (NaN) reads as stale, never fresh', () => {
    expect(ringStyleForFreshness(Number.NaN).dashed).toBe(true);
  });
});

describe('gateBadgeText', () => {
  const gate = (progress: number | null, materials: GateProgress['materials']): GateProgress => ({
    progress,
    materials,
  });

  it('formats `gate NN% · GOOD X/Y` using the least-complete incomplete material', () => {
    const g = gate(45, [
      { tradeSymbol: 'ADVANCED_CIRCUITRY', required: 300, fulfilled: 300 },
      { tradeSymbol: 'FAB_MATS', required: 3000, fulfilled: 1200 },
    ]);
    expect(gateBadgeText(g)).toBe('gate 45% · FAB_MATS 1200/3000');
  });

  it('falls back to the first material when everything is fulfilled', () => {
    const g = gate(100, [
      { tradeSymbol: 'FAB_MATS', required: 3000, fulfilled: 3000 },
      { tradeSymbol: 'ADVANCED_CIRCUITRY', required: 300, fulfilled: 300 },
    ]);
    expect(gateBadgeText(g)).toBe('gate 100% · FAB_MATS 3000/3000');
  });

  it('computes the percentage from material sums when progress is null', () => {
    const g = gate(null, [
      { tradeSymbol: 'FAB_MATS', required: 100, fulfilled: 25 },
      { tradeSymbol: 'ADVANCED_CIRCUITRY', required: 100, fulfilled: 25 },
    ]);
    expect(gateBadgeText(g)).toBe('gate 25% · ADVANCED_CIRCUITRY 25/100');
  });

  it('renders bare `gate NN%` when there are no material lines', () => {
    expect(gateBadgeText(gate(80, []))).toBe('gate 80%');
  });

  it('returns null for an unstarted bill (null progress, no materials) or a null gate', () => {
    expect(gateBadgeText(gate(null, []))).toBeNull();
    expect(gateBadgeText(null)).toBeNull();
  });
});
