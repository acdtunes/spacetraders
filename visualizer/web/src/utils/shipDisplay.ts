import type { ShipTrailPoint, TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';
import {
  SHIP_LABEL_FONT_SIZE,
  SHIP_LABEL_MIN_WIDTH,
  SHIP_LABEL_PADDING_X,
  SHIP_LABEL_PADDING_Y,
  SHIP_LABEL_SCREEN_OFFSET_X,
  SHIP_LABEL_SCREEN_OFFSET_Y,
} from '../constants/shipLabel';

type Point = { x: number; y: number };

export const calculateShipRotation = (
  ship: TaggedShip,
  position: Point,
  waypoints: Map<string, WaypointType>,
  trail?: ShipTrailPoint[],
  resolveWaypointPosition?: (waypoint: WaypointType) => Point
): number => {
  let travelAngleRad: number | null = null;

  const getWaypointCenter = (waypoint: WaypointType): Point =>
    resolveWaypointPosition ? resolveWaypointPosition(waypoint) : { x: waypoint.x, y: waypoint.y };

  const resolveRoutePoint = (
    routePoint?: { symbol?: string; x?: number; y?: number }
  ): Point | null => {
    if (!routePoint) {
      return null;
    }

    if (routePoint.symbol) {
      const waypoint = waypoints.get(routePoint.symbol);
      if (waypoint) {
        return getWaypointCenter(waypoint);
      }
    }

    if (typeof routePoint.x === 'number' && typeof routePoint.y === 'number') {
      return { x: routePoint.x, y: routePoint.y };
    }

    return null;
  };

  // For IN_TRANSIT, use the trail to determine actual movement direction
  // This accounts for curves during entry/exit phases
  if (ship.nav.status === 'IN_TRANSIT' && trail && trail.length >= 2) {
    const previous = trail[trail.length - 2];
    const dxTrail = position.x - previous.x;
    const dyTrail = position.y - previous.y;
    if (Math.hypot(dxTrail, dyTrail) > 0.01) {
      travelAngleRad = Math.atan2(dyTrail, dxTrail);
    }
  }

  // Fallback to route direction if no trail available
  if (travelAngleRad === null && ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.origin && ship.nav.route?.destination) {
    const originPosition = resolveRoutePoint(ship.nav.route.origin);
    const destinationPosition = resolveRoutePoint(ship.nav.route.destination);

    if (originPosition && destinationPosition) {
      const dxRoute = destinationPosition.x - originPosition.x;
      const dyRoute = destinationPosition.y - originPosition.y;
      if (Math.hypot(dxRoute, dyRoute) > 0.01) {
        travelAngleRad = Math.atan2(dyRoute, dxRoute);
      }
    }
  }

  if (travelAngleRad === null) {
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination) {
      const originPosition = resolveRoutePoint(ship.nav.route.origin);
      const destinationPosition = resolveRoutePoint(ship.nav.route.destination);

      if (originPosition && destinationPosition) {
        const dxRoute = destinationPosition.x - originPosition.x;
        const dyRoute = destinationPosition.y - originPosition.y;
        if (Math.hypot(dxRoute, dyRoute) > 0.01) {
          travelAngleRad = Math.atan2(dyRoute, dxRoute);
        }
      }

      if (travelAngleRad === null && destinationPosition) {
        travelAngleRad = Math.atan2(
          destinationPosition.y - position.y,
          destinationPosition.x - position.x
        );
      }
    } else if (ship.nav.status === 'IN_ORBIT') {
      const waypoint = waypoints.get(ship.nav.waypointSymbol);
      if (waypoint) {
        const center = getWaypointCenter(waypoint);
        const dx = position.x - center.x;
        const dy = position.y - center.y;
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

export interface ShipLabelContext {
  currentScale: number;
  projectToScreen: (point: Point) => Point | null;
  projectToWorld: (point: Point) => Point | null;
}

export interface ShipLabelInfo {
  labelText: string;
  labelWidth: number;
  labelHeight: number;
  labelScale: number;
  offsetX: number;
  offsetY: number;
}

export const formatShipType = (role: string | null | undefined) => {
  if (!role) return 'Unknown';
  return role
    .split('_')
    .map((segment) => segment.charAt(0) + segment.slice(1).toLowerCase())
    .join(' ');
};

export const getShipLabelInfo = (
  ship: TaggedShip,
  position: Point,
  context: ShipLabelContext
): ShipLabelInfo | null => {
  const shipNumber = ship.symbol.split('-').pop() ?? ship.symbol;
  const labelText = `${formatShipType(ship.registration.role)} ${shipNumber}`;
  const labelHeight = SHIP_LABEL_FONT_SIZE + SHIP_LABEL_PADDING_Y * 2;
  const estimatedTextWidth = labelText.length * (SHIP_LABEL_FONT_SIZE * 0.6);
  const labelWidth = Math.max(
    SHIP_LABEL_MIN_WIDTH,
    estimatedTextWidth + SHIP_LABEL_PADDING_X * 2
  );
  const labelScale = 1 / (context.currentScale || 1);

  // Use fixed world-space offsets that scale inversely with zoom
  // This keeps labels at consistent distance from ships at all zoom levels
  const offsetX = SHIP_LABEL_SCREEN_OFFSET_X / context.currentScale;
  const offsetY = -SHIP_LABEL_SCREEN_OFFSET_Y / context.currentScale;

  return {
    labelText,
    labelWidth,
    labelHeight,
    labelScale,
    offsetX,
    offsetY,
  };
};
