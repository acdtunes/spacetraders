import type { TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';
import { Waypoint } from '../domain';
import { CANVAS_CONSTANTS } from '../constants/canvas';
import type { Position } from '../domain/ship';

export const getRouteEndpoint = (
  ship: TaggedShip,
  currentPosition: Position,
  waypoints: Map<string, WaypointType>
): Position | null => {
  const route = ship.nav.route;
  if (!route?.destination) return null;

  const destSymbol = route.destination.symbol;
  const destWaypoint = destSymbol ? waypoints.get(destSymbol) : null;
  const destX = typeof route.destination.x === 'number' ? route.destination.x : destWaypoint?.x;
  const destY = typeof route.destination.y === 'number' ? route.destination.y : destWaypoint?.y;

  if (typeof destX !== 'number' || typeof destY !== 'number') {
    return null;
  }

  let targetX = destX;
  let targetY = destY;

  if (destWaypoint) {
    const waypointRadius = Waypoint.getRadius(destWaypoint);
    const orbitDistance = destWaypoint.type.includes('ASTEROID')
      ? CANVAS_CONSTANTS.ORBIT_DISTANCE_ASTEROID
      : CANVAS_CONSTANTS.ORBIT_DISTANCE_DEFAULT;
    const orbitRadius = waypointRadius + orbitDistance;

    const dxOrbit = destX - currentPosition.x;
    const dyOrbit = destY - currentPosition.y;
    const totalDistance = Math.hypot(dxOrbit, dyOrbit);

    if (orbitRadius > 0 && totalDistance > orbitRadius + 0.5) {
      const ratio = (totalDistance - orbitRadius) / totalDistance;
      targetX = currentPosition.x + dxOrbit * ratio;
      targetY = currentPosition.y + dyOrbit * ratio;
    }
  }

  return { x: targetX, y: targetY };
};
