import { useMemo } from 'react';
import { Group, Line, Arrow } from 'react-konva';
import type { TaggedShip, FlightMode, Waypoint as WaypointType } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import type { Position } from '../domain/ship';
import { hashString } from '../utils/hash';
import { getRouteEndpoint } from './routeVectorsUtils';

const ROUTE_ARROW_SPEED = 0.008;
const ROUTE_ARROW_SEGMENT_LENGTH = 14;

const ROUTE_COLORS: Record<FlightMode, { line: string; arrow: string }> = {
  DRIFT: { line: 'rgba(148, 163, 184, 0.35)', arrow: '#f1f5f9' },
  CRUISE: { line: 'rgba(59, 130, 246, 0.4)', arrow: '#93c5fd' },
  BURN: { line: 'rgba(248, 113, 113, 0.45)', arrow: '#fca5a5' },
  STEALTH: { line: 'rgba(129, 140, 248, 0.4)', arrow: '#c7d2fe' },
};

export interface RouteVectorsProps {
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
  frameTimestamp: number;
  getShipRenderPosition: (ship: TaggedShip, target: Position, timestamp: number) => Position;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
}

export function RouteVectors({
  ships,
  waypoints,
  currentScale,
  animationFrame,
  frameTimestamp,
  getShipRenderPosition,
  getWaypointPosition,
}: RouteVectorsProps) {
  const activeShips = useMemo(() => {
    return ships.filter((ship) => ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination);
  }, [ships]);

  if (activeShips.length === 0) {
    return null;
  }

  return (
    <Group listening={false}>
      {activeShips.map((ship) => {
        const arrivalTime = ship.nav.route?.arrival ? new Date(ship.nav.route.arrival).getTime() : null;
        if (arrivalTime && frameTimestamp >= arrivalTime) {
          return null;
        }

        const targetPosition = Ship.getPosition(ship, waypoints, {
          waypointPositionResolver: getWaypointPosition,
        });
        if (targetPosition.x === 0 && targetPosition.y === 0) return null;

        const renderPosition = getShipRenderPosition(ship, targetPosition, frameTimestamp);
        const endpoint = getRouteEndpoint(ship, targetPosition, waypoints, getWaypointPosition);
        if (!endpoint) return null;

        const dx = endpoint.x - renderPosition.x;
        const dy = endpoint.y - renderPosition.y;
        const length = Math.hypot(dx, dy);
        if (length < 6) return null;

        const unitX = dx / length;
        const unitY = dy / length;

        const phaseSeed = (hashString(ship.symbol) % 100) / 100;
        const arrowProgress = (((animationFrame * ROUTE_ARROW_SPEED) + phaseSeed) % 1 + 1) % 1;
        const arrowHeadDistance = Math.max(4, Math.min(length, arrowProgress * length));
        const segmentLength = Math.min(ROUTE_ARROW_SEGMENT_LENGTH, length * 0.35);
        const arrowTailDistance = Math.max(0, arrowHeadDistance - segmentLength);

        const tailX = renderPosition.x + unitX * arrowTailDistance;
        const tailY = renderPosition.y + unitY * arrowTailDistance;
        const headX = renderPosition.x + unitX * arrowHeadDistance;
        const headY = renderPosition.y + unitY * arrowHeadDistance;

        const colors = ROUTE_COLORS[ship.nav.flightMode] ?? ROUTE_COLORS.CRUISE;
        const strokeWidth = Math.max(0.8 / currentScale, 0.4);
        const dashOffset = -((animationFrame * 1.2) % 1000);

        return (
          <Group key={`route-${ship.symbol}`} listening={false}>
            <Line
              points={[renderPosition.x, renderPosition.y, endpoint.x, endpoint.y]}
              stroke={colors.line}
              strokeWidth={strokeWidth}
              dash={[12 / currentScale, 10 / currentScale]}
              dashOffset={dashOffset}
              lineCap="round"
              lineJoin="round"
              listening={false}
            />
            <Arrow
              points={[tailX, tailY, headX, headY]}
              stroke={colors.arrow}
              fill={colors.arrow}
              strokeWidth={strokeWidth * 1.4}
              pointerLength={8 / currentScale}
              pointerWidth={6 / currentScale}
              listening={false}
            />
          </Group>
        );
      })}
    </Group>
  );
}
