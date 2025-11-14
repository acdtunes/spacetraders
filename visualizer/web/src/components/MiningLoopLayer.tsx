import { memo } from 'react';
import { Group, Line, Circle, Text } from 'react-konva';
import type { ShipAssignment, Waypoint as WaypointType } from '../types/spacetraders';

interface MiningLoopLayerProps {
  assignments: Map<string, ShipAssignment>;
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
}

interface MiningRoute {
  asteroid: string;
  market: string;
  shipSymbol: string;
}

/**
 * Extract mining routes from assignments
 */
function getMiningRoutes(assignments: Map<string, ShipAssignment>): MiningRoute[] {
  const routes: MiningRoute[] = [];

  assignments.forEach((assignment, shipSymbol) => {
    if (assignment.operation !== 'mine' || assignment.status !== 'active') return;

    // Extract asteroid and market from metadata
    const metadata = assignment.metadata;
    if (!metadata) return;

    // Metadata structure varies, try different patterns
    const asteroid = metadata.asteroid || metadata.mining_waypoint;
    const market = metadata.market || metadata.sell_waypoint;

    if (asteroid && market) {
      routes.push({
        asteroid: typeof asteroid === 'string' ? asteroid : asteroid.toString(),
        market: typeof market === 'string' ? market : market.toString(),
        shipSymbol,
      });
    }
  });

  return routes;
}

export const MiningLoopLayer = memo(function MiningLoopLayer({
  assignments,
  waypoints,
  currentScale,
  animationFrame,
}: MiningLoopLayerProps) {
  const routes = getMiningRoutes(assignments);

  if (routes.length === 0) return null;

  const miningColor = '#F59E0B'; // Amber for mining operations

  return (
    <Group listening={false}>
      {routes.map((route, index) => {
        const asteroidWaypoint = waypoints.get(route.asteroid);
        const marketWaypoint = waypoints.get(route.market);

        if (!asteroidWaypoint || !marketWaypoint) return null;

        const x1 = asteroidWaypoint.x;
        const y1 = asteroidWaypoint.y;
        const x2 = marketWaypoint.x;
        const y2 = marketWaypoint.y;

        const midX = (x1 + x2) / 2;
        const midY = (y1 + y2) / 2;

        const strokeWidth = Math.max(1.2, 2.5 / currentScale);
        const dotRadius = Math.max(1.5, 3 / currentScale);
        const fontSize = Math.max(8, 14 / currentScale);

        // Animated bidirectional dash
        const dashLength = 6 / currentScale;
        const gapLength = 3 / currentScale;
        const dashOffset = (animationFrame * 0.6) % (dashLength + gapLength);

        return (
          <Group key={`mining-${index}-${route.shipSymbol}`}>
            {/* Mining loop line */}
            <Line
              points={[x1, y1, x2, y2]}
              stroke={miningColor}
              strokeWidth={strokeWidth}
              opacity={0.6}
              dash={[dashLength, gapLength]}
              dashOffset={dashOffset}
              listening={false}
              lineCap="round"
            />

            {/* Return path (slightly offset for visual clarity) */}
            <Line
              points={[x2, y2, x1, y1]}
              stroke={miningColor}
              strokeWidth={strokeWidth * 0.7}
              opacity={0.3}
              dash={[dashLength * 0.7, gapLength * 0.7]}
              dashOffset={-dashOffset}
              listening={false}
              lineCap="round"
            />

            {/* Asteroid marker */}
            <Circle
              x={x1}
              y={y1}
              radius={dotRadius * 1.5}
              fill={miningColor}
              stroke="#92400E"
              strokeWidth={strokeWidth * 0.5}
              opacity={0.7}
              listening={false}
            />

            {/* Market marker */}
            <Circle
              x={x2}
              y={y2}
              radius={dotRadius * 1.5}
              fill={miningColor}
              stroke="#92400E"
              strokeWidth={strokeWidth * 0.5}
              opacity={0.7}
              listening={false}
            />

            {/* Mining icon at asteroid */}
            {currentScale > 0.4 && (
              <Text
                text="⛏️"
                x={x1}
                y={y1 - fontSize * 1.2}
                fontSize={fontSize}
                align="center"
                offsetX={fontSize / 2}
                listening={false}
              />
            )}

            {/* Market/sell icon */}
            {currentScale > 0.4 && (
              <Text
                text="💰"
                x={x2}
                y={y2 - fontSize * 1.2}
                fontSize={fontSize}
                align="center"
                offsetX={fontSize / 2}
                listening={false}
              />
            )}

            {/* Route label at midpoint */}
            {currentScale > 0.3 && (
              <Group x={midX} y={midY + 6 / currentScale}>
                <Circle
                  radius={Math.max(5, 8 / currentScale)}
                  fill="rgba(17, 24, 39, 0.85)"
                  listening={false}
                />
                <Text
                  text="⚒"
                  fontSize={fontSize * 0.9}
                  fill={miningColor}
                  align="center"
                  verticalAlign="middle"
                  offsetX={fontSize * 0.4}
                  offsetY={fontSize * 0.4}
                  listening={false}
                />
              </Group>
            )}
          </Group>
        );
      })}
    </Group>
  );
});
