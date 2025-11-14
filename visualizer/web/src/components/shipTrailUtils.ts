import type { ShipTrailPoint, FlightMode } from '../types/spacetraders';

type RGB = { r: number; g: number; b: number };

type TrailVisualSettings = {
  maxAgeMs: number;
  baseWidth: number;
  baseAlpha: number;
  tailAlpha: number;
  glowBlur: number;
  glowAlpha: number;
  particleDensity: number;
  particleSize: [number, number];
  particleAlpha: number;
  colorBoost: number;
};

type VisualConfig = Record<FlightMode, TrailVisualSettings>;

export const filterActiveTrail = (
  trail: ShipTrailPoint[],
  now: number,
  configMap: VisualConfig
): ShipTrailPoint[] =>
  trail.filter((point) => {
    const config = configMap[point.flightMode];
    return config?.maxAgeMs > 0 && now - point.timestamp <= config.maxAgeMs;
  });

export const computeParticleCount = (
  activeTrail: ShipTrailPoint[],
  config: TrailVisualSettings
): number => {
  if (config.particleDensity <= 0) return 0;
  const segmentCount = Math.max(0, activeTrail.length - 1);
  return Math.max(1, Math.floor(segmentCount * config.particleDensity));
};

export const hexToRgb = (hex: string): RGB => {
  const normalized = hex.replace('#', '');
  const parsed = normalized.length === 3
    ? normalized
        .split('')
        .map((char) => char + char)
        .join('')
    : normalized;
  const value = Number.parseInt(parsed, 16);
  if (Number.isNaN(value)) {
    return { r: 255, g: 107, b: 107 };
  }
  return {
    r: (value >> 16) & 255,
    g: (value >> 8) & 255,
    b: value & 255,
  };
};

export const boostColor = (rgb: RGB, amount: number): RGB => ({
  r: Math.min(255, rgb.r + (255 - rgb.r) * amount),
  g: Math.min(255, rgb.g + (255 - rgb.g) * amount),
  b: Math.min(255, rgb.b + (255 - rgb.b) * amount),
});

export const rgba = (rgb: RGB, alpha: number): string => `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, ${alpha})`;

export type { TrailVisualSettings, VisualConfig };
