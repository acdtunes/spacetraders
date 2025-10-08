import type { ShipTrailPoint, Waypoint as WaypointType, TaggedShip } from '../types/spacetraders';

export const calculateShipRotation = (
  ship: TaggedShip,
  position: { x: number; y: number },
  waypoints: Map<string, WaypointType>,
  trail?: ShipTrailPoint[]
): number => {
  let travelAngleRad: number | null = null;

  if (trail && trail.length >= 2) {
    const previous = trail[trail.length - 2];
    const dxTrail = position.x - previous.x;
    const dyTrail = position.y - previous.y;
    if (Math.hypot(dxTrail, dyTrail) > 0.01) {
      travelAngleRad = Math.atan2(dyTrail, dxTrail);
    }
  }

  if (travelAngleRad === null) {
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination) {
      const dest = ship.nav.route.destination;
      if (typeof dest.x === 'number' && typeof dest.y === 'number') {
        travelAngleRad = Math.atan2(dest.y - position.y, dest.x - position.x);
      }
    } else if (ship.nav.status === 'IN_ORBIT') {
      const waypointSymbol = ship.nav.waypointSymbol;
      const waypoint = waypoints.get(waypointSymbol);
      if (waypoint) {
        const dx = position.x - waypoint.x;
        const dy = position.y - waypoint.y;
        const orbitalAngle = Math.atan2(dy, dx);
        travelAngleRad = orbitalAngle + Math.PI / 2;
      }
    }
  }

  if (travelAngleRad === null) {
    return 0;
  }

  return (travelAngleRad + Math.PI / 2) * (180 / Math.PI);
};
