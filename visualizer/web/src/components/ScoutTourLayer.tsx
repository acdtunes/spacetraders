import { memo } from 'react';
import { Group, Line, Circle } from 'react-konva';
import type { ScoutTour, Waypoint as WaypointType } from '../types/spacetraders';

interface ScoutTourLayerProps {
  tours: ScoutTour[];
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
}

/**
 * Generate a color for a tour based on its system symbol
 */
function getTourColor(systemSymbol: string): string {
  // Hash the system symbol to get a consistent color
  let hash = 0;
  for (let i = 0; i < systemSymbol.length; i++) {
    hash = systemSymbol.charCodeAt(i) + ((hash << 5) - hash);
  }

  // Convert to HSL for better color distribution
  const hue = Math.abs(hash % 360);
  return `hsl(${hue}, 70%, 60%)`;
}

/**
 * Convert HSL string to RGB hex for Konva
 */
function hslToRgb(h: number, s: number, l: number): string {
  s /= 100;
  l /= 100;

  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) =>
    l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));

  const r = Math.round(255 * f(0));
  const g = Math.round(255 * f(8));
  const b = Math.round(255 * f(4));

  return `#${r.toString(16).padStart(2, '0')}${g.toString(16).padStart(2, '0')}${b.toString(16).padStart(2, '0')}`;
}

export const ScoutTourLayer = memo(function ScoutTourLayer({
  tours,
  waypoints,
  currentScale,
  animationFrame,
}: ScoutTourLayerProps) {
  if (tours.length === 0) return null;

  return (
    <Group listening={false}>
      {tours.map((tour, tourIndex) => {
        const tourOrder = tour.tour_order;
        if (tourOrder.length < 2) return null;

        // Get waypoint positions
        const positions = tourOrder
          .map((symbol) => {
            const waypoint = waypoints.get(symbol);
            return waypoint ? { x: waypoint.x, y: waypoint.y, symbol } : null;
          })
          .filter((pos): pos is { x: number; y: number; symbol: string } => pos !== null);

        if (positions.length < 2) return null;

        // Get color for this tour
        const colorHsl = getTourColor(tour.system);
        // Extract hue from hsl string
        const hueMatch = colorHsl.match(/hsl\((\d+),/);
        const hue = hueMatch ? parseInt(hueMatch[1]) : 0;
        const color = hslToRgb(hue, 70, 60);

        // Create line points
        const linePoints: number[] = [];
        positions.forEach((pos) => {
          linePoints.push(pos.x, pos.y);
        });

        const strokeWidth = Math.max(1, 2 / currentScale);
        const dotRadius = Math.max(1.5, 3 / currentScale);

        // Animated dash effect
        const dashLength = 8 / currentScale;
        const gapLength = 4 / currentScale;
        const dashOffset = (animationFrame * 0.5) % (dashLength + gapLength);

        return (
          <Group key={`tour-${tourIndex}`}>
            {/* Tour path lines */}
            <Line
              points={linePoints}
              stroke={color}
              strokeWidth={strokeWidth}
              opacity={0.6}
              dash={[dashLength, gapLength]}
              dashOffset={dashOffset}
              listening={false}
              lineCap="round"
              lineJoin="round"
            />

            {/* Waypoint markers */}
            {positions.map((pos, idx) => (
              <Circle
                key={`${tour.system}-${pos.symbol}-${idx}`}
                x={pos.x}
                y={pos.y}
                radius={dotRadius}
                fill={color}
                opacity={0.8}
                listening={false}
              />
            ))}

            {/* Start marker (larger circle) */}
            {positions.length > 0 && (
              <Circle
                x={positions[0].x}
                y={positions[0].y}
                radius={dotRadius * 1.8}
                fill={color}
                opacity={0.5}
                listening={false}
              />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
