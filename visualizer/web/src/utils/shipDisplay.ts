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
import {
  SHIP_LABEL_FONT_SIZE,
  SHIP_LABEL_MIN_WIDTH,
  SHIP_LABEL_PADDING_X,
  SHIP_LABEL_PADDING_Y,
  SHIP_LABEL_SCREEN_OFFSET_X,
  SHIP_LABEL_SCREEN_OFFSET_Y,
} from '../constants/shipLabel';

interface LabelContext {
  currentScale: number;
  projectToScreen: (point: { x: number; y: number }) => { x: number; y: number } | null;
  projectToWorld: (point: { x: number; y: number }) => { x: number; y: number } | null;
}

export const getShipLabelInfo = (
  ship: TaggedShip,
  position: { x: number; y: number },
  context: LabelContext
) => {
  const shipNumber = ship.symbol.split('-').pop() ?? ship.symbol;
  const shipTypeRaw = ship.registration.role || 'UNKNOWN';
  const shipType = shipTypeRaw
    .split('_')
    .map((part: string) => part.charAt(0) + part.slice(1).toLowerCase())
    .join(' ');
  const labelText = `${shipType} ${shipNumber}`;
  const labelHeight = SHIP_LABEL_FONT_SIZE + SHIP_LABEL_PADDING_Y * 2;
  const estimatedTextWidth = labelText.length * (SHIP_LABEL_FONT_SIZE * 0.6);
  const labelWidth = Math.max(
    SHIP_LABEL_MIN_WIDTH,
    estimatedTextWidth + SHIP_LABEL_PADDING_X * 2
  );
  const labelScale = 1 / (context.currentScale || 1);

  const screenPos = context.projectToScreen(position);
  if (!screenPos) {
    return null;
  }

  const labelTargetScreen = {
    x: screenPos.x + SHIP_LABEL_SCREEN_OFFSET_X,
    y: screenPos.y - SHIP_LABEL_SCREEN_OFFSET_Y,
  };

  const labelWorldPos = context.projectToWorld(labelTargetScreen);
  if (!labelWorldPos) {
    return null;
  }

  return {
    labelText,
    labelWidth,
    labelHeight,
    labelScale,
    offsetX: labelWorldPos.x - position.x,
    offsetY: labelWorldPos.y - position.y,
  };
};
