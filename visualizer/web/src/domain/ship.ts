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

  if (typeof point.x === 'number' && typeof point.y === 'number') {
    return { x: point.x, y: point.y };
  }

  if (point.symbol) {
    const waypoint = waypoints.get(point.symbol);
    if (waypoint) {
      return resolveWaypointPosition(waypoint, options);
    }
  }
  return null;
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
    options?: ShipPositionOptions,
    orbitRadiusOverride?: number
  ): Position {
    const waypoint = waypoints.get(ship.nav.waypointSymbol);
    if (!waypoint) return { x: 0, y: 0 };
    const center = resolveWaypointPosition(waypoint, options);

    const waypointRadius = Waypoint.getRadius(waypoint);
    const orbitDistance = Waypoint.getOrbitDistance(waypoint);

    const orbitRadius = orbitRadiusOverride ?? waypointRadius + orbitDistance;
    const orbitPeriod = CANVAS_CONSTANTS.ORBIT_PERIOD;
    const route = ship.nav.route;
    const now = Date.now();

    let angle: number | null = null;

    const normalizeAngle = (value: number) => {
      let angleValue = value;
      while (angleValue <= -Math.PI) angleValue += Math.PI * 2;
      while (angleValue > Math.PI) angleValue -= Math.PI * 2;
      return angleValue;
    };

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
            const incomingAngle = Math.atan2(dy, dx);
            const topAngle = -Math.PI / 2;
            const bottomAngle = Math.PI / 2;
            const deltaTop = Math.abs(normalizeAngle(incomingAngle - topAngle));
            const deltaBottom = Math.abs(normalizeAngle(incomingAngle - bottomAngle));
            const entryAngle = deltaTop <= deltaBottom ? topAngle : bottomAngle;
            const elapsedSinceArrival = Math.max(0, now - arrivalTime);
            const phase = ((elapsedSinceArrival % orbitPeriod) / orbitPeriod) * Math.PI * 2;
            angle = entryAngle + phase;
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

    const progress = (now - departureTime) / Math.max(arrivalTime - departureTime, 1);
    const clampedProgress = Math.max(0, Math.min(1, progress));

    const dx = destinationPosition.x - originPosition.x;
    const dy = destinationPosition.y - originPosition.y;
    const totalDistance = Math.hypot(dx, dy);

    if (totalDistance === 0) {
      return { x: originPosition.x, y: originPosition.y };
    }

    const destinationSymbol = ship.nav.route.destination.symbol;
    if (destinationSymbol) {
      const destinationWaypoint = waypoints.get(destinationSymbol);
      if (destinationWaypoint) {
        const waypointRadius = Waypoint.getRadius(destinationWaypoint);
        const orbitDistance = Waypoint.getOrbitDistance(destinationWaypoint);
        const orbitRadius = Math.max(0, waypointRadius + orbitDistance);

        const normalizeAngle = (value: number) => {
          let angleValue = value;
          while (angleValue <= -Math.PI) angleValue += Math.PI * 2;
          while (angleValue > Math.PI) angleValue -= Math.PI * 2;
          return angleValue;
        };

        const incomingAngle = Math.atan2(dy, dx);
        const entryAngle = (() => {
          const radiusThreshold = orbitRadius * 1.5;
          if (totalDistance <= radiusThreshold) {
            const midAngle = incomingAngle + Math.PI;
            return normalizeAngle(midAngle);
          }

          const topAngle = -Math.PI / 2;
          const bottomAngle = Math.PI / 2;
          const deltaTop = Math.abs(normalizeAngle(incomingAngle - topAngle));
          const deltaBottom = Math.abs(normalizeAngle(incomingAngle - bottomAngle));
          return deltaTop <= deltaBottom ? topAngle : bottomAngle;
        })();

        const entryPoint = {
          x: destinationPosition.x + Math.cos(entryAngle) * orbitRadius,
          y: destinationPosition.y + Math.sin(entryAngle) * orbitRadius,
        };

        if (clampedProgress >= 1) {
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
            options,
            orbitRadius
          );
        }

        const travelDistance = totalDistance * clampedProgress;
        const curveDistance = Math.min(Math.max(orbitRadius * 1.2, 10), totalDistance);
        const distanceBeforeCurve = Math.max(totalDistance - curveDistance, 0);

        if (travelDistance <= distanceBeforeCurve || curveDistance <= 0) {
          const ratio = travelDistance / totalDistance;
          return {
            x: originPosition.x + dx * ratio,
            y: originPosition.y + dy * ratio,
          };
        }

        const t = (travelDistance - distanceBeforeCurve) / curveDistance;
        const startPoint = {
          x: originPosition.x + (dx * distanceBeforeCurve) / totalDistance,
          y: originPosition.y + (dy * distanceBeforeCurve) / totalDistance,
        };

        const lineDirection = (() => {
          const length = Math.hypot(dx, dy);
          if (length === 0) return { x: 0, y: 0 };
          return { x: dx / length, y: dy / length };
        })();

        const orbitTangent = {
          x: -Math.sin(entryAngle),
          y: Math.cos(entryAngle),
        };

        const controlPoint = {
          x:
            startPoint.x +
            lineDirection.x * (curveDistance * 0.5) -
            orbitTangent.x * (curveDistance * 0.2),
          y:
            startPoint.y +
            lineDirection.y * (curveDistance * 0.5) -
            orbitTangent.y * (curveDistance * 0.2),
        };

        const oneMinusT = 1 - t;
        return {
          x: oneMinusT * oneMinusT * startPoint.x + 2 * oneMinusT * t * controlPoint.x + t * t * entryPoint.x,
          y: oneMinusT * oneMinusT * startPoint.y + 2 * oneMinusT * t * controlPoint.y + t * t * entryPoint.y,
        };
      }
    }

    const travelledDistance = totalDistance * clampedProgress;
    if (totalDistance === 0) {
      return originPosition;
    }
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
