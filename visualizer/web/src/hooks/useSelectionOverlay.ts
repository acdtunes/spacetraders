import { useMemo } from 'react';
import type { TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';

export interface SelectionOverlay {
  left: number;
  top: number;
  size: number;
  type: 'ship' | 'waypoint';
}

interface SelectionParams {
  selectedObject: { type: 'waypoint' | 'ship'; symbol: string; x: number; y: number } | null;
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  projectToScreen: (point: { x: number; y: number }) => { x: number; y: number } | null;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
  getShipPosition: (ship: TaggedShip) => { x: number; y: number } | null;
}

const DEFAULT_SIZE = 14;
const WAYPOINT_SIZE = 18;

export const useSelectionOverlay = ({
  selectedObject,
  ships,
  waypoints,
  projectToScreen,
  getWaypointPosition,
  getShipPosition,
}: SelectionParams): SelectionOverlay | null => {
  return useMemo(() => {
    if (!selectedObject) return null;

    let worldX = selectedObject.x;
    let worldY = selectedObject.y;

    if (selectedObject.type === 'ship') {
      const ship = ships.find((candidate) => candidate.symbol === selectedObject.symbol);
      if (!ship) return null;
      const position = getShipPosition(ship);
      if (!position) return null;
      worldX = position.x;
      worldY = position.y;
    } else if (selectedObject.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (!waypoint) return null;
      const displayPosition = getWaypointPosition(waypoint);
      worldX = displayPosition.x;
      worldY = displayPosition.y;
    }

    const screenPos = projectToScreen({ x: worldX, y: worldY });
    if (!screenPos) return null;

    return {
      left: screenPos.x,
      top: screenPos.y,
      size: selectedObject.type === 'waypoint' ? WAYPOINT_SIZE : DEFAULT_SIZE,
      type: selectedObject.type,
    };
  }, [selectedObject, ships, waypoints, projectToScreen, getWaypointPosition, getShipPosition]);
};
