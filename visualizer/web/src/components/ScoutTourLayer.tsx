import { memo } from 'react';
import { Group, Line, Circle } from 'react-konva';
import type { ScoutTour, Waypoint as WaypointType } from '../types/spacetraders';
import { getTourId } from '../utils/tourHelpers';

interface ScoutTourLayerProps {
  tours: ScoutTour[];
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
  visibleTours?: Set<string>;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
}

/**
 * Distinct color palette for scout tours
 */
const TOUR_COLOR_PALETTE = [
  '#FF6B6B', // Red
  '#4ECDC4', // Teal
  '#45B7D1', // Blue
  '#FFA07A', // Light Orange
  '#98D8C8', // Mint
  '#F7DC6F', // Yellow
  '#BB8FCE', // Purple
  '#85C1E2', // Sky Blue
  '#F8B739', // Orange
  '#52B788', // Green
  '#EE6C4D', // Coral
  '#3D5A80', // Navy
  '#E63946', // Crimson
  '#06FFA5', // Bright Green
  '#C77DFF', // Lavender
];

/**
 * Get a consistent color for a tour based on its system symbol
 */
function getTourColor(systemSymbol: string, index: number): string {
  // Use index primarily, but hash system symbol for consistency if palette exhausted
  if (index < TOUR_COLOR_PALETTE.length) {
    return TOUR_COLOR_PALETTE[index];
  }

  // Fallback: hash system symbol
  let hash = 0;
  for (let i = 0; i < systemSymbol.length; i++) {
    hash = systemSymbol.charCodeAt(i) + ((hash << 5) - hash);
  }

  return TOUR_COLOR_PALETTE[Math.abs(hash) % TOUR_COLOR_PALETTE.length];
}

export const ScoutTourLayer = memo(function ScoutTourLayer({
  tours,
  waypoints,
  currentScale,
  animationFrame,
  visibleTours,
  getWaypointPosition,
}: ScoutTourLayerProps) {
  if (tours.length === 0) return null;

  return (
    <Group listening={false}>
      {tours.map((tour, tourIndex) => {
        // Filter based on visibility
        const tourId = getTourId(tour);
        if (visibleTours && !visibleTours.has(tourId)) {
          return null;
        }

        const tourOrder = tour.tour_order;
        if (tourOrder.length < 2) return null;

        // Get waypoint positions (using display positions for overlapping waypoints)
        const positions = tourOrder
          .map((symbol) => {
            const waypoint = waypoints.get(symbol);
            if (!waypoint) return null;
            const pos = getWaypointPosition(waypoint);
            return { x: pos.x, y: pos.y, symbol };
          })
          .filter((pos): pos is { x: number; y: number; symbol: string } => pos !== null);

        if (positions.length < 2) return null;

        // Get distinct color for this tour
        const color = getTourColor(tour.system, tourIndex);

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
          <Group key={`tour-${tourId}`}>
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
