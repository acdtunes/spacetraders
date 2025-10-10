import type { Ship as ShipType, Waypoint as WaypointType } from '../types/spacetraders';
import { CANVAS_CONSTANTS } from '../constants/canvas';
import { Waypoint } from './waypoint';

export interface Position {
  x: number;
  y: number;
}

export interface ShipPositionOptions {
  waypointPositionResolver?: (waypoint: WaypointType) => Position;
}

type RoutePoint = {
  symbol?: string;
  x?: number;
  y?: number;
} | null | undefined;

const resolveWaypointPosition = (
  waypoint: WaypointType,
  options?: ShipPositionOptions
): Position => {
  if (options?.waypointPositionResolver) {
    return options.waypointPositionResolver(waypoint);
  }
  return { x: waypoint.x, y: waypoint.y };
};

const resolveRoutePointPosition = (
  point: RoutePoint,
  waypoints: Map<string, WaypointType>,
  options?: ShipPositionOptions
): Position | null => {
  if (!point) {
    return null;
  }

  if (point.symbol) {
    const waypoint = waypoints.get(point.symbol);
    if (waypoint) {
      return resolveWaypointPosition(waypoint, options);
    }
  }

  const x = typeof point.x === 'number' ? point.x : null;
  const y = typeof point.y === 'number' ? point.y : null;

  if (x === null || y === null) {
    return null;
  }

  return { x, y };
};

/**
 * Ship domain logic - encapsulates all ship-related business rules
 */
export const Ship = {
  /**
   * Calculate ship position based on navigation status
   * - IN_ORBIT: Calculate orbital position around waypoint
   * - DOCKED: Position at waypoint
   * - IN_TRANSIT: Interpolate between origin and destination
   */
  getPosition(
    ship: ShipType,
    waypoints: Map<string, WaypointType>,
    options?: ShipPositionOptions
  ): Position {
    if (ship.nav.status === 'IN_ORBIT') {
      return this.calculateOrbitPosition(ship, waypoints, options);
    }

    if (ship.nav.status !== 'IN_TRANSIT') {
      return this.getDockedPosition(ship, waypoints, options);
    }

    return this.interpolateTransitPosition(ship, waypoints, options);
  },

  /**
   * Calculate orbital position around waypoint
   */
  calculateOrbitPosition(
    ship: ShipType,
    waypoints: Map<string, WaypointType>,
    options?: ShipPositionOptions
  ): Position {
    const waypoint = waypoints.get(ship.nav.waypointSymbol);
    if (!waypoint) return { x: 0, y: 0 };
    const center = resolveWaypointPosition(waypoint, options);

    const waypointRadius = Waypoint.getRadius(waypoint);
    const orbitDistance = waypoint.type.includes('ASTEROID')
      ? CANVAS_CONSTANTS.ORBIT_DISTANCE_ASTEROID
      : CANVAS_CONSTANTS.ORBIT_DISTANCE_DEFAULT;

    const orbitRadius = waypointRadius + orbitDistance;
    const orbitPeriod = CANVAS_CONSTANTS.ORBIT_PERIOD;
    const route = ship.nav.route;
    const now = Date.now();

    let angle: number | null = null;

    if (route && route.origin && route.destination) {
      const origin = route.origin;
      const destination = route.destination;
      const hasCoordinates =
        typeof origin.x === 'number' &&
        typeof origin.y === 'number' &&
        typeof destination.x === 'number' &&
        typeof destination.y === 'number';

      const originPosition = resolveRoutePointPosition(origin, waypoints, options);
      const destinationPosition = resolveRoutePointPosition(destination, waypoints, options);

      if (hasCoordinates && originPosition && destinationPosition) {
        const arrivalTime = new Date(route.arrival).getTime();
        if (!Number.isNaN(arrivalTime) && arrivalTime <= now) {
          const dx = destinationPosition.x - originPosition.x;
          const dy = destinationPosition.y - originPosition.y;
          const length = Math.hypot(dx, dy);
          if (length > 0.0001) {
            const arrivalAngle = Math.atan2(dy, dx) + Math.PI; // facing back along incoming vector
            const elapsedSinceArrival = Math.max(0, now - arrivalTime);
            const phase = ((elapsedSinceArrival % orbitPeriod) / orbitPeriod) * Math.PI * 2;
            angle = arrivalAngle + phase;
          }
        }
      }
    }

    if (angle === null) {
      angle = (now % orbitPeriod) / orbitPeriod * Math.PI * 2;
    }

    return {
      x: center.x + Math.cos(angle) * orbitRadius,
      y: center.y + Math.sin(angle) * orbitRadius,
    };
  },

  /**
   * Get docked ship position (at waypoint)
   */
  getDockedPosition(
    ship: ShipType,
    waypoints: Map<string, WaypointType>,
    options?: ShipPositionOptions
  ): Position {
    const waypoint = waypoints.get(ship.nav.waypointSymbol);
    if (!waypoint) return { x: 0, y: 0 };
    const center = resolveWaypointPosition(waypoint, options);
    const baseRadius = Waypoint.getRadius(waypoint);

    // Deterministic offset around the waypoint so docked ships don't overlap the center.
    const hash = Array.from(ship.symbol).reduce((acc, char) => acc * 31 + char.charCodeAt(0), 7);
    const angle = (Math.abs(hash) % 360) * (Math.PI / 180);
    const ring = baseRadius + 4 + ((Math.abs(hash) % 4) * 1.2);

    return {
      x: center.x + Math.cos(angle) * ring,
      y: center.y + Math.sin(angle) * ring,
    };
  },

  /**
   * Interpolate position for ship in transit
   */
  interpolateTransitPosition(
    ship: ShipType,
    waypoints: Map<string, WaypointType>,
    options?: ShipPositionOptions
  ): Position {
    if (!ship.nav.route?.destination) {
      return { x: 0, y: 0 };
    }

    const originPosition = resolveRoutePointPosition(ship.nav.route.origin, waypoints, options);
    const destinationPosition = resolveRoutePointPosition(ship.nav.route.destination, waypoints, options);
    if (!originPosition || !destinationPosition) {
      return { x: 0, y: 0 };
    }

    const departureTime = new Date(ship.nav.route.departureTime).getTime();
    const arrivalTime = new Date(ship.nav.route.arrival).getTime();
    const now = Date.now();

    const progress = (now - departureTime) / (arrivalTime - departureTime);
    const clampedProgress = Math.max(0, Math.min(1, progress));

    if (clampedProgress >= 1 && ship.nav.route.destination.symbol) {
      const destinationSymbol = ship.nav.route.destination.symbol;
      const destinationWaypoint = waypoints.get(destinationSymbol);

      if (destinationWaypoint) {
        return this.calculateOrbitPosition(
          {
            ...ship,
            nav: {
              ...ship.nav,
              status: 'IN_ORBIT',
              waypointSymbol: destinationSymbol,
            },
          } as ShipType,
          waypoints,
          options
        );
      }
    }

    const dx = destinationPosition.x - originPosition.x;
    const dy = destinationPosition.y - originPosition.y;
    const totalDistance = Math.hypot(dx, dy);

    if (totalDistance === 0) {
      return { x: originPosition.x, y: originPosition.y };
    }

    let maxTravelDistance = totalDistance;
    const destinationSymbol = ship.nav.route.destination.symbol;
    if (destinationSymbol) {
      const destinationWaypoint = waypoints.get(destinationSymbol);
      if (destinationWaypoint) {
        const waypointRadius = Waypoint.getRadius(destinationWaypoint);
        const orbitDistance = destinationWaypoint.type.includes('ASTEROID')
          ? CANVAS_CONSTANTS.ORBIT_DISTANCE_ASTEROID
          : CANVAS_CONSTANTS.ORBIT_DISTANCE_DEFAULT;
        const orbitRadius = waypointRadius + orbitDistance;
        if (orbitRadius > 0 && totalDistance > orbitRadius) {
          maxTravelDistance = Math.max(totalDistance - orbitRadius, 0);
        }
      }
    }

    const travelledDistance = Math.min(totalDistance * clampedProgress, maxTravelDistance);
    const ratio = travelledDistance / totalDistance;

    return {
      x: originPosition.x + dx * ratio,
      y: originPosition.y + dy * ratio,
    };
  },

  /**
   * Check if ship is currently in transit
   */
  isInTransit(ship: ShipType): boolean {
    return ship.nav.status === 'IN_TRANSIT';
  },

  /**
   * Check if ship is in orbit
   */
  isInOrbit(ship: ShipType): boolean {
    return ship.nav.status === 'IN_ORBIT';
  },

  /**
   * Check if ship is docked
   */
  isDocked(ship: ShipType): boolean {
    return ship.nav.status === 'DOCKED';
  },

  /**
   * Get ship display color
   */
  getDisplayColor(ship: ShipType & { agentColor?: string }): string {
    return ship.agentColor ?? '#ff6b6b';
  },

  /**
   * Get ship display color as hex number
   */
  getDisplayColorHex(ship: ShipType & { agentColor?: string }): number {
    const color = this.getDisplayColor(ship);
    return parseInt(color.replace('#', ''), 16);
  },

  /**
   * Calculate rotation angle for ship based on movement direction
   */
  getRotationAngle(ship: ShipType, waypoints: Map<string, WaypointType>): number {
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination && ship.nav.route?.origin) {
      const origin = ship.nav.route.origin;
      const dest = ship.nav.route.destination;
      const dx = dest.x - origin.x;
      const dy = dest.y - origin.y;
      return Math.atan2(dy, dx);
    }

    if (ship.nav.status === 'IN_ORBIT') {
      const position = this.calculateOrbitPosition(ship, waypoints);
      const waypoint = waypoints.get(ship.nav.waypointSymbol);
      if (waypoint) {
        const dx = position.x - waypoint.x;
        const dy = position.y - waypoint.y;
        return Math.atan2(dy, dx) + Math.PI / 2;
      }
    }

    return 0;
  },
};
