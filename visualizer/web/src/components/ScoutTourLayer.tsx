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
  orbitalClusters?: Map<
    string,
    {
      id: string;
      center: { x: number; y: number };
      members: string[];
    }
  >;
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
  orbitalClusters,
}: ScoutTourLayerProps) {
  if (tours.length === 0) return null;

  const clusters =
    orbitalClusters ??
    new Map<
      string,
      {
        id: string;
        center: { x: number; y: number };
        members: string[];
      }
    >();

  const connectorLines: JSX.Element[] = [];

  clusters.forEach((cluster) => {
    if (cluster.members.length <= 1) return;
    const connectorStrokeWidth = Math.max(0.5, 1.2 / currentScale);
    cluster.members.forEach((symbol) => {
      const waypoint = waypoints.get(symbol);
      if (!waypoint) return;
      const displayPos = getWaypointPosition(waypoint);
      connectorLines.push(
        <Line
          key={`cluster-connector-${cluster.id}-${symbol}`}
          points={[cluster.center.x, cluster.center.y, displayPos.x, displayPos.y]}
          stroke="rgba(148, 163, 184, 0.35)"
          strokeWidth={connectorStrokeWidth}
          listening={false}
        />
      );
    });
  });

  return (
    <Group listening={false}>
      {connectorLines}
      {tours.map((tour, tourIndex) => {
        // Filter based on visibility
        const tourId = getTourId(tour);
        if (visibleTours && !visibleTours.has(tourId)) {
          return null;
        }

        const tourOrder = tour.tour_order;
        if (tourOrder.length < 2) return null;

        // Get waypoint positions (using ACTUAL coordinates for geometric accuracy)
        // Note: We use actual coordinates for tour lines to show the true geometric path.
        // Waypoint markers below still use display positions for visual clarity.
        const positions = tourOrder
          .map((symbol) => {
            const waypoint = waypoints.get(symbol);
            if (!waypoint) return null;
            // Use actual database coordinates, not display offsets
            return { x: waypoint.x, y: waypoint.y, symbol };
          })
          .filter((pos): pos is { x: number; y: number; symbol: string } => pos !== null);

        if (positions.length < 2) return null;

        // Get distinct color for this tour
        const color = getTourColor(tour.system, tourIndex);

        // Create line segments, SKIPPING orbital jumps (same coordinates)
        // Orbitals are instant 0-distance moves that shouldn't be drawn as lines
        const lineSegments: number[][] = [];
        let currentSegment: number[] = [];

        for (let i = 0; i < positions.length - 1; i++) {
          const current = positions[i];
          const next = positions[i + 1];

          // Add current point to segment
          if (currentSegment.length === 0) {
            currentSegment.push(current.x, current.y);
          }

          // Check if next move is orbital (same coordinates)
          const isOrbital = current.x === next.x && current.y === next.y;

          if (isOrbital) {
            // Finish current segment and start new one
            if (currentSegment.length >= 4) { // At least 2 points
              lineSegments.push(currentSegment);
            }
            currentSegment = [];
          } else {
            // Add next point to continue segment
            currentSegment.push(next.x, next.y);
          }
        }

        // Add final segment
        if (currentSegment.length >= 4) {
          lineSegments.push(currentSegment);
        }

        const strokeWidth = Math.max(1, 2 / currentScale);
        const dotRadius = Math.max(1.5, 3 / currentScale);

        // Animated dash effect
        const dashLength = 8 / currentScale;
        const gapLength = 4 / currentScale;
        const dashOffset = (animationFrame * 0.5) % (dashLength + gapLength);

        return (
          <Group key={`tour-${tourId}`}>
            {/* Tour path lines (only non-orbital segments) */}
            {lineSegments.map((segment, segIdx) => (
              <Line
                key={`segment-${segIdx}`}
                points={segment}
                stroke={color}
                strokeWidth={strokeWidth}
                opacity={0.6}
                dash={[dashLength, gapLength]}
                dashOffset={dashOffset}
                listening={false}
                lineCap="round"
                lineJoin="round"
              />
            ))}

            {/* Waypoint markers (use display positions for visual clarity) */}
            {positions.map((pos, idx) => {
              const waypoint = waypoints.get(pos.symbol);
              if (!waypoint) return null;
              const displayPos = getWaypointPosition(waypoint);
              return (
                <Circle
                  key={`${tour.system}-${pos.symbol}-${idx}`}
                  x={displayPos.x}
                  y={displayPos.y}
                  radius={dotRadius}
                  fill={color}
                  opacity={0.8}
                  listening={false}
                />
              );
            })}

            {/* Start marker (larger circle, use display position) */}
            {positions.length > 0 && (() => {
              const waypoint = waypoints.get(positions[0].symbol);
              if (!waypoint) return null;
              const displayPos = getWaypointPosition(waypoint);
              return (
                <Circle
                  x={displayPos.x}
                  y={displayPos.y}
                  radius={dotRadius * 1.8}
                  fill={color}
                  opacity={0.5}
                  listening={false}
                />
              );
            })()}
          </Group>
        );
      })}
    </Group>
  );
});
