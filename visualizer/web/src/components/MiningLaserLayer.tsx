import { memo } from 'react';
import { Group, Line } from 'react-konva';
import type { TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import { Waypoint } from '../domain';

const MINING_WAYPOINT_TYPES = new Set<WaypointType['type']>([
  'ASTEROID',
  'ASTEROID_FIELD',
  'ENGINEERED_ASTEROID',
  'ASTEROID_BASE',
]);

interface Point {
  x: number;
  y: number;
}

export interface MiningLaserLayerProps {
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  animationFrame: number;
  frameTimestamp: number;
  getShipRenderPosition: (ship: TaggedShip, target: Point, timestamp: number) => Point;
  getWaypointPosition: (waypoint: WaypointType) => Point;
}

export const MiningLaserLayer = memo(function MiningLaserLayer({
  ships,
  waypoints,
  animationFrame,
  frameTimestamp,
  getShipRenderPosition,
  getWaypointPosition,
}: MiningLaserLayerProps) {
  const time = animationFrame / 60; // Convert to seconds

  return (
    <>
      {ships.map((ship) => {
        if (!ship.cooldown || ship.cooldown.remainingSeconds <= 0) return null;
        if (ship.nav.status !== 'IN_ORBIT') return null;

        const waypoint = waypoints.get(ship.nav.waypointSymbol);
        if (!waypoint) return null;
        if (!MINING_WAYPOINT_TYPES.has(waypoint.type)) return null;

        const waypointCenter = getWaypointPosition(waypoint);
        const targetPosition = Ship.getPosition(ship, waypoints, {
          waypointPositionResolver: getWaypointPosition,
        });
        const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);

        return (
          <Group key={`laser-${ship.symbol}`}>
            {[0, 1].map((beamIndex) => {
              const phase = (time * 3 + beamIndex * 0.7) % 1;
              const alpha = 0.5 + Math.sin(phase * Math.PI * 2) * 0.4;
              const angle = Math.atan2(waypointCenter.y - position.y, waypointCenter.x - position.x);
              const directionX = Math.cos(angle);
              const directionY = Math.sin(angle);
              const angleOffset = beamIndex === 0 ? -0.12 : 0.12;
              const beamAngle = angle + angleOffset;
              const beamDirX = Math.cos(beamAngle);
              const beamDirY = Math.sin(beamAngle);
              const surfaceRadius = Math.max(Waypoint.getRadius(waypoint) - 1, 0);
              const centerOffsetX = position.x - waypointCenter.x;
              const centerOffsetY = position.y - waypointCenter.y;
              const b = 2 * (beamDirX * centerOffsetX + beamDirY * centerOffsetY);
              const c = centerOffsetX * centerOffsetX + centerOffsetY * centerOffsetY - surfaceRadius * surfaceRadius;
              const discriminant = b * b - 4 * c;
              let beamEndX = waypointCenter.x - directionX * surfaceRadius;
              let beamEndY = waypointCenter.y - directionY * surfaceRadius;

              if (discriminant >= 0) {
                const sqrtDisc = Math.sqrt(discriminant);
                const t1 = (-b - sqrtDisc) / 2;
                const t2 = (-b + sqrtDisc) / 2;
                const t = [t1, t2]
                  .filter((value) => value > 0)
                  .sort((aVal, bVal) => aVal - bVal)[0];

                if (typeof t === 'number') {
                  beamEndX = position.x + beamDirX * t;
                  beamEndY = position.y + beamDirY * t;
                }
              }

              return (
                <Line
                  key={beamIndex}
                  points={[position.x, position.y, beamEndX, beamEndY]}
                  stroke="#ff0000"
                  strokeWidth={0.08}
                  opacity={alpha}
                  listening={false}
                />
              );
            })}
          </Group>
        );
      })}
    </>
  );
});
