import { memo } from 'react';
import { Shape } from 'react-konva';
import type { TaggedShip, ShipTrailPoint } from '../types/spacetraders';
import { NOIR, noirRgb } from '../theme/noir';
import { trailOpacity } from '../store/trails';

// Noir wake: a single accent hue that tapers by age (older = fainter/thinner).
// No per-segment shadow — the buffer can hold up to TRAIL_MAX_POINTS segments
// per ship, so we keep each stroke cheap and let the engine glow supply bloom.
const TRAIL_BASE_ALPHA = 0.55;
const TRAIL_HEAD_WIDTH = 1.6;
const TRAIL_TAIL_WIDTH = 0.4;

// Parse the accent hue once at module load so the hot per-segment path only
// concatenates the alpha, never re-parsing the hex on every stroke every frame.
const TRAIL_RGB = noirRgb(NOIR.accentSoft);
const TRAIL_STROKE_PREFIX = `rgba(${TRAIL_RGB.r}, ${TRAIL_RGB.g}, ${TRAIL_RGB.b}, `;

export interface ShipTrailLayerProps {
  ships: TaggedShip[];
  trails: Map<string, ShipTrailPoint[]>;
  frameTimestamp: number;
}

export const ShipTrailLayer = memo(function ShipTrailLayer({
  ships,
  trails,
  frameTimestamp,
}: ShipTrailLayerProps) {
  return (
    <>
      {ships.map((ship) => {
        const trail = trails.get(ship.symbol);
        if (!trail || trail.length < 2) {
          return null;
        }

        return (
          <Shape
            key={`trail-${ship.symbol}`}
            listening={false}
            sceneFunc={(context) => {
              const ctx = context._context as CanvasRenderingContext2D;
              ctx.save();
              ctx.lineCap = 'round';
              ctx.lineJoin = 'round';

              for (let i = 0; i < trail.length - 1; i++) {
                const start = trail[i];
                const end = trail[i + 1];
                // Newest points sit at the tail of the buffer, so the leading
                // endpoint's age drives the fade of the whole segment.
                const ageFade = trailOpacity(end, frameTimestamp);
                if (ageFade <= 0) {
                  continue;
                }

                // Buffer-position taper: the oldest end always fades to
                // transparent even when the 120-point cap truncates a fast
                // ship's history, so the tail never hard-stops at partial alpha.
                const progress = (i + 1) / trail.length;
                ctx.strokeStyle = TRAIL_STROKE_PREFIX + ageFade * progress * TRAIL_BASE_ALPHA + ')';
                ctx.lineWidth = TRAIL_TAIL_WIDTH + (TRAIL_HEAD_WIDTH - TRAIL_TAIL_WIDTH) * progress;
                ctx.beginPath();
                ctx.moveTo(start.x, start.y);
                ctx.lineTo(end.x, end.y);
                ctx.stroke();
              }

              ctx.restore();
            }}
          />
        );
      })}
    </>
  );
});
