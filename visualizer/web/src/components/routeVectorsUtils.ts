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

  // Return the exact waypoint position (not orbit radius)
  return { x: destX, y: destY };
};
