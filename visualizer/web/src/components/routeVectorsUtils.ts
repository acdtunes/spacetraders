import type { TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';
import { Waypoint } from '../domain';
import { CANVAS_CONSTANTS } from '../constants/canvas';
import type { Position } from '../domain/ship';

export const getRouteEndpoint = (
  ship: TaggedShip,
  currentPosition: Position,
  waypoints: Map<string, WaypointType>,
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number }
): Position | null => {
  const route = ship.nav.route;
  if (!route?.destination) return null;

  const destSymbol = route.destination.symbol;
  const destWaypoint = destSymbol ? waypoints.get(destSymbol) : null;

  // Use adjusted display position if waypoint exists, otherwise use route coordinates
  let destX: number;
  let destY: number;

  if (destWaypoint) {
    const displayPos = getWaypointPosition(destWaypoint);
    destX = displayPos.x;
    destY = displayPos.y;
  } else {
    destX = typeof route.destination.x === 'number' ? route.destination.x : 0;
    destY = typeof route.destination.y === 'number' ? route.destination.y : 0;

    if (destX === 0 && destY === 0) {
      return null;
    }
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
