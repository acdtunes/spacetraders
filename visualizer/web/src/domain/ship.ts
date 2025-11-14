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

    // Deterministic random position inside the waypoint (not on border)
    const hash = Array.from(ship.symbol).reduce((acc, char) => acc * 31 + char.charCodeAt(0), 7);
    const angle = (Math.abs(hash) % 360) * (Math.PI / 180);
    // Use a second hash component for distance - position inside waypoint (0 to 70% of radius)
    const distanceHash = Array.from(ship.symbol).reduce((acc, char) => acc * 37 + char.charCodeAt(0), 13);
    const distanceFactor = (Math.abs(distanceHash) % 100) / 100; // 0.0 to 1.0
    const ring = baseRadius * distanceFactor * 0.7; // Random position inside, up to 70% of radius

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

    // Orbital exit phase: Handle transition from orbit to straight-line travel
    const originSymbol = ship.nav.route.origin?.symbol;
    if (originSymbol) {
      const originWaypoint = waypoints.get(originSymbol);
      if (originWaypoint) {
        const waypointRadius = Waypoint.getRadius(originWaypoint);
        const orbitDistance = Waypoint.getOrbitDistance(originWaypoint);
        const orbitRadius = Math.max(0, waypointRadius + orbitDistance);
        const orbitPeriod = CANVAS_CONSTANTS.ORBIT_PERIOD;

        // Define the exit sequence distances
        const orbitalContinuationDistance = orbitRadius * 0.3; // Continue orbiting briefly
        const spiralDistance = Math.min(Math.max(orbitRadius * 1.5, 15), totalDistance * 0.25);
        const totalExitDistance = orbitalContinuationDistance + spiralDistance;
        const travelDistance = totalDistance * clampedProgress;

        // Only apply orbital exit if we're still in the exit zone
        if (travelDistance <= totalExitDistance && totalExitDistance > 0) {
          const normalizeAngle = (value: number) => {
            let angleValue = value;
            while (angleValue <= -Math.PI) angleValue += Math.PI * 2;
            while (angleValue > Math.PI) angleValue -= Math.PI * 2;
            return angleValue;
          };

          // Calculate route direction
          const routeAngle = Math.atan2(dy, dx);

          // Calculate initial orbital angle at departure time
          const initialAngle = (departureTime % orbitPeriod) / orbitPeriod * Math.PI * 2;

          // For clockwise orbit, velocity at position angle θ points at θ + π/2
          // We want velocity = routeAngle, so: θ + π/2 = routeAngle
          // Therefore: θ = routeAngle - π/2
          const targetExitAngle = normalizeAngle(routeAngle - Math.PI / 2);

          // Calculate clockwise distance from current position to exit angle
          const getClockwiseDistance = (from: number, to: number) => {
            let delta = to - from;
            while (delta < 0) delta += Math.PI * 2;
            while (delta > Math.PI * 2) delta -= Math.PI * 2;
            return delta;
          };

          const rotationAmount = getClockwiseDistance(initialAngle, targetExitAngle);
          const exitAngle = targetExitAngle;

          // Phase 1: Continue orbiting until perpendicular to route
          if (travelDistance <= orbitalContinuationDistance) {
            // Progress through orbital continuation (0 to 1)
            const orbitProgress = travelDistance / orbitalContinuationDistance;

            // Continue rotating clockwise from initial angle (angle increases = clockwise in screen coords)
            const currentAngle = normalizeAngle(initialAngle + rotationAmount * orbitProgress);

            return {
              x: originPosition.x + Math.cos(currentAngle) * orbitRadius,
              y: originPosition.y + Math.sin(currentAngle) * orbitRadius,
            };
          }

          // Phase 2: Spiral outward in S-curve from perpendicular position
          const spiralProgress = (travelDistance - orbitalContinuationDistance) / spiralDistance;

          // Starting point: orbital position at perpendicular angle
          const exitPoint = {
            x: originPosition.x + Math.cos(exitAngle) * orbitRadius,
            y: originPosition.y + Math.sin(exitAngle) * orbitRadius,
          };

          // End point: aligned with route vector
          const straightLineStart = {
            x: originPosition.x + (dx / totalDistance) * totalExitDistance,
            y: originPosition.y + (dy / totalDistance) * totalExitDistance,
          };

          // Cubic bezier for C1 continuity (continuous velocity)
          // P0 = exitPoint, P3 = straightLineStart
          // Tangent at start and end both = routeAngle
          const exitTangent = { x: Math.cos(routeAngle), y: Math.sin(routeAngle) };

          // Control points positioned to create smooth tangents
          const controlDist = spiralDistance * 0.4;
          const P1 = {
            x: exitPoint.x + exitTangent.x * controlDist,
            y: exitPoint.y + exitTangent.y * controlDist,
          };
          const P2 = {
            x: straightLineStart.x - exitTangent.x * controlDist,
            y: straightLineStart.y - exitTangent.y * controlDist,
          };

          // Cubic bezier: B(t) = (1-t)³P0 + 3(1-t)²t·P1 + 3(1-t)t²·P2 + t³·P3
          const t = spiralProgress;
          const oneMinusT = 1 - t;
          const t2 = t * t;
          const t3 = t2 * t;
          const oneMinusT2 = oneMinusT * oneMinusT;
          const oneMinusT3 = oneMinusT2 * oneMinusT;

          return {
            x: oneMinusT3 * exitPoint.x +
               3 * oneMinusT2 * t * P1.x +
               3 * oneMinusT * t2 * P2.x +
               t3 * straightLineStart.x,
            y: oneMinusT3 * exitPoint.y +
               3 * oneMinusT2 * t * P1.y +
               3 * oneMinusT * t2 * P2.y +
               t3 * straightLineStart.y,
          };
        }
      }
    }

    // Orbital entry phase: Handle transition from straight-line travel to orbit
    const destinationSymbol = ship.nav.route.destination.symbol;
    if (destinationSymbol) {
      const destinationWaypoint = waypoints.get(destinationSymbol);
      if (destinationWaypoint) {
        const waypointRadius = Waypoint.getRadius(destinationWaypoint);
        const orbitDistance = Waypoint.getOrbitDistance(destinationWaypoint);
        const orbitRadius = Math.max(0, waypointRadius + orbitDistance);
        const orbitPeriod = CANVAS_CONSTANTS.ORBIT_PERIOD;

        const normalizeAngle = (value: number) => {
          let angleValue = value;
          while (angleValue <= -Math.PI) angleValue += Math.PI * 2;
          while (angleValue > Math.PI) angleValue -= Math.PI * 2;
          return angleValue;
        };

        // Calculate incoming angle from travel direction
        const incomingAngle = Math.atan2(dy, dx);

        // Calculate entry angle (must match IN_ORBIT logic at lines 127-135)
        // Ships enter from either top (-π/2) or bottom (π/2) based on incoming direction
        const topAngle = -Math.PI / 2;
        const bottomAngle = Math.PI / 2;
        const deltaTop = Math.abs(normalizeAngle(incomingAngle - topAngle));
        const deltaBottom = Math.abs(normalizeAngle(incomingAngle - bottomAngle));
        const entryAngle = deltaTop <= deltaBottom ? topAngle : bottomAngle;

        // At arrival, ship will be at entryAngle (matches IN_ORBIT calculation)
        const arrivalAngle = entryAngle;
        const arrivalPoint = {
          x: destinationPosition.x + Math.cos(arrivalAngle) * orbitRadius,
          y: destinationPosition.y + Math.sin(arrivalAngle) * orbitRadius,
        };

        if (clampedProgress >= 1) {
          return arrivalPoint;
        }

        // Define the entry curve distance - just one smooth curve into orbit
        const entryCurveDistance = Math.min(Math.max(orbitRadius * 2, 20), totalDistance * 0.35);
        const travelDistance = totalDistance * clampedProgress;
        const distanceBeforeCurve = Math.max(totalDistance - entryCurveDistance, 0);

        // Still in straight-line travel phase
        if (travelDistance <= distanceBeforeCurve || entryCurveDistance <= 0) {
          const ratio = travelDistance / totalDistance;
          return {
            x: originPosition.x + dx * ratio,
            y: originPosition.y + dy * ratio,
          };
        }

        // In the entry curve zone - smoothly curve to arrival position on orbit
        const curveProgress = (travelDistance - distanceBeforeCurve) / entryCurveDistance;
        const t = curveProgress;

        // Starting point: where the curve begins (on the straight-line path)
        const curveStart = {
          x: originPosition.x + (dx * distanceBeforeCurve) / totalDistance,
          y: originPosition.y + (dy * distanceBeforeCurve) / totalDistance,
        };

        // For clockwise orbit at position arrivalAngle, velocity points at arrivalAngle + π/2
        const arrivalVelocityAngle = arrivalAngle + Math.PI / 2;

        // Cubic bezier for C1 continuity (continuous velocity)
        // P0 = curveStart, P3 = arrivalPoint
        // Tangent at start = incomingAngle, tangent at end = arrivalVelocityAngle
        const startTangent = { x: Math.cos(incomingAngle), y: Math.sin(incomingAngle) };
        const endTangent = { x: Math.cos(arrivalVelocityAngle), y: Math.sin(arrivalVelocityAngle) };

        // Control points positioned to create smooth tangents
        const controlDist = entryCurveDistance * 0.4;
        const P1 = {
          x: curveStart.x + startTangent.x * controlDist,
          y: curveStart.y + startTangent.y * controlDist,
        };
        const P2 = {
          x: arrivalPoint.x - endTangent.x * controlDist,
          y: arrivalPoint.y - endTangent.y * controlDist,
        };

        // Cubic bezier: B(t) = (1-t)³P0 + 3(1-t)²t·P1 + 3(1-t)t²·P2 + t³·P3
        const t2 = t * t;
        const t3 = t2 * t;
        const oneMinusT = 1 - t;
        const oneMinusT2 = oneMinusT * oneMinusT;
        const oneMinusT3 = oneMinusT2 * oneMinusT;

        return {
          x: oneMinusT3 * curveStart.x +
             3 * oneMinusT2 * t * P1.x +
             3 * oneMinusT * t2 * P2.x +
             t3 * arrivalPoint.x,
          y: oneMinusT3 * curveStart.y +
             3 * oneMinusT2 * t * P1.y +
             3 * oneMinusT * t2 * P2.y +
             t3 * arrivalPoint.y,
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
  getRotationAngle(ship: ShipType, waypoints: Map<string, WaypointType>, options?: ShipPositionOptions): number {
    if (ship.nav.status === 'IN_TRANSIT') {
      // Calculate actual velocity direction by comparing current position to position slightly ahead
      const currentPos = this.getPosition(ship, waypoints, options);

      // Sample a tiny bit ahead in time to get velocity direction
      const timeDelta = 50; // 50ms ahead
      const futureShip = {
        ...ship,
        nav: {
          ...ship.nav,
          route: {
            ...ship.nav.route,
            departureTime: new Date(new Date(ship.nav.route.departureTime).getTime() - timeDelta).toISOString(),
          }
        }
      } as ShipType;

      const futurePos = this.getPosition(futureShip, waypoints, options);

      const dx = futurePos.x - currentPos.x;
      const dy = futurePos.y - currentPos.y;

      // If there's movement, use that direction; otherwise fall back to route direction
      if (Math.abs(dx) > 0.001 || Math.abs(dy) > 0.001) {
        return Math.atan2(dy, dx);
      }

      // Fallback to route direction
      if (ship.nav.route?.destination && ship.nav.route?.origin) {
        const odx = ship.nav.route.destination.x - ship.nav.route.origin.x;
        const ody = ship.nav.route.destination.y - ship.nav.route.origin.y;
        return Math.atan2(ody, odx);
      }
    }

    if (ship.nav.status === 'IN_ORBIT') {
      const position = this.calculateOrbitPosition(ship, waypoints, options);
      const waypoint = waypoints.get(ship.nav.waypointSymbol);
      if (waypoint) {
        const center = options?.waypointPositionResolver ? options.waypointPositionResolver(waypoint) : { x: waypoint.x, y: waypoint.y };
        const dx = position.x - center.x;
        const dy = position.y - center.y;
        return Math.atan2(dy, dx) + Math.PI / 2;
      }
    }

    return 0;
  },
};
