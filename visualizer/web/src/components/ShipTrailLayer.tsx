import { memo } from 'react';
import { Shape } from 'react-konva';
import type { TaggedShip, ShipTrailPoint, FlightMode } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import {
  filterActiveTrail,
  hexToRgb,
  boostColor,
  rgba,
  computeParticleCount,
  type TrailVisualSettings,
} from './shipTrailUtils';

const TRAIL_VISUAL_CONFIG: Record<FlightMode, TrailVisualSettings> = {
  DRIFT: {
    maxAgeMs: 0,
    baseWidth: 0,
    baseAlpha: 0,
    tailAlpha: 0,
    glowBlur: 0,
    glowAlpha: 0,
    particleDensity: 0,
    particleSize: [0, 0],
    particleAlpha: 0,
    colorBoost: 0,
  },
  CRUISE: {
    maxAgeMs: 7000,
    baseWidth: 0.7,
    baseAlpha: 0.28,
    tailAlpha: 0.06,
    glowBlur: 4,
    glowAlpha: 0.22,
    particleDensity: 0.18,
    particleSize: [0.25, 0.5],
    particleAlpha: 0.22,
    colorBoost: 0.18,
  },
  BURN: {
    maxAgeMs: 4000,     // Shorter trail (was 12000) - fast ships leave brief trails
    baseWidth: 3.0,     // Slightly wider for intensity
    baseAlpha: 0.75,    // Much brighter (was 0.55)
    tailAlpha: 0.25,    // Brighter tail (was 0.15)
    glowBlur: 16,       // More glow (was 12)
    glowAlpha: 0.85,    // Intense glow (was 0.65)
    particleDensity: 0.8, // More particles (was 0.6)
    particleSize: [0.8, 1.6], // Larger particles (was [0.6, 1.3])
    particleAlpha: 0.7, // Brighter particles (was 0.5)
    colorBoost: 0.65,   // Much brighter color (was 0.45)
  },
  STEALTH: {
    maxAgeMs: 5000,
    baseWidth: 0.9,
    baseAlpha: 0.22,
    tailAlpha: 0.08,
    glowBlur: 6,
    glowAlpha: 0.35,
    particleDensity: 0.3,
    particleSize: [0.4, 0.9],
    particleAlpha: 0.3,
    colorBoost: 0.2,
  },
};

export interface ShipTrailLayerProps {
  ships: TaggedShip[];
  trails: Map<string, ShipTrailPoint[]>;
  animationFrame: number;
}

export const ShipTrailLayer = memo(function ShipTrailLayer({
  ships,
  trails,
  animationFrame,
}: ShipTrailLayerProps) {
  const now = Date.now();

  return (
    <>
      {ships.map((ship) => {
        const trail = trails.get(ship.symbol);
        if (!trail || trail.length < 2) {
          return null;
        }

        const activeTrail = filterActiveTrail(trail, now, TRAIL_VISUAL_CONFIG);

        if (activeTrail.length < 2) {
          return null;
        }

        const latestMode = activeTrail[activeTrail.length - 1].flightMode;
        const config = TRAIL_VISUAL_CONFIG[latestMode] ?? TRAIL_VISUAL_CONFIG.CRUISE;
        if (config.maxAgeMs === 0) {
          return null;
        }

        const baseColor = hexToRgb(Ship.getDisplayColor(ship));
        const boostedColor = boostColor(baseColor, config.colorBoost);
        const sparkColor = boostColor(boostedColor, 0.25);

        return (
          <Shape
            key={`trail-${ship.symbol}`}
            listening={false}
            sceneFunc={(context) => {
              const ctx = context._context as CanvasRenderingContext2D;
              ctx.save();
              ctx.lineCap = 'round';
              ctx.lineJoin = 'round';

              for (let i = 0; i < activeTrail.length - 1; i++) {
                const start = activeTrail[i];
                const end = activeTrail[i + 1];
                const progress = (i + 1) / activeTrail.length;
                const alpha = config.tailAlpha + (config.baseAlpha - config.tailAlpha) * progress;

                ctx.shadowColor = rgba(boostedColor, config.glowAlpha * progress);
                ctx.shadowBlur = config.glowBlur * progress;
                ctx.lineWidth = config.baseWidth * (0.6 + progress * 0.4);
                ctx.strokeStyle = rgba(boostedColor, alpha);
                ctx.beginPath();
                ctx.moveTo(start.x, start.y);
                ctx.lineTo(end.x, end.y);
                ctx.stroke();
              }

              ctx.shadowBlur = 0;

              if (ship.nav.status === 'IN_TRANSIT') {
                const particleCount = computeParticleCount(activeTrail, config);
                const segmentCount = activeTrail.length - 1;

                for (let p = 0; p < particleCount; p++) {
                  const index = Math.max(1, segmentCount - Math.floor((p / particleCount) * segmentCount));
                  const head = activeTrail[index];
                  const tail = activeTrail[index - 1];
                  const t = ((animationFrame * 0.08 + p * 0.37) % 1 + 1) % 1;
                  const x = head.x + (tail.x - head.x) * t;
                  const y = head.y + (tail.y - head.y) * t;
                  const oscillation = (Math.sin(animationFrame * 0.15 + p) + 1) / 2;
                  const radius =
                    config.particleSize[0] +
                    (config.particleSize[1] - config.particleSize[0]) * oscillation;

                  ctx.fillStyle = rgba(sparkColor, config.particleAlpha * (0.8 + 0.2 * oscillation));
                  ctx.beginPath();
                  ctx.arc(x, y, radius, 0, Math.PI * 2);
                  ctx.fill();
                }
              }

              ctx.restore();
            }}
          />
        );
      })}
    </>
  );
});
