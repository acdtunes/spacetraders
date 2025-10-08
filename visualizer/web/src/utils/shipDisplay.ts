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
      const waypoint = waypoints.get(ship.nav.waypointSymbol);
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

const formatShipType = (role: string | null | undefined) => {
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

  const screenPos = context.projectToScreen(position);
  if (!screenPos) return null;

  const labelTargetScreen = {
    x: screenPos.x + SHIP_LABEL_SCREEN_OFFSET_X,
    y: screenPos.y - SHIP_LABEL_SCREEN_OFFSET_Y,
  };

  const labelWorldPos = context.projectToWorld(labelTargetScreen);
  if (!labelWorldPos) return null;

  return {
    labelText,
    labelWidth,
    labelHeight,
    labelScale,
    offsetX: labelWorldPos.x - position.x,
    offsetY: labelWorldPos.y - position.y,
  };
};
