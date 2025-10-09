import { useMemo } from 'react';
import type { TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';

export interface SelectionOverlay {
  left: number;
  top: number;
  size: number;
  type: 'ship' | 'waypoint';
}

interface SelectionParams {
  selectedShip: TaggedShip | null;
  selectedWaypoint: WaypointType | null;
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  projectToScreen: (point: { x: number; y: number }) => { x: number; y: number } | null;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
  getShipPosition: (ship: TaggedShip) => { x: number; y: number } | null;
  frameTimestamp: number;
}

const DEFAULT_SIZE = 14;
const WAYPOINT_SIZE = 18;

export const useSelectionOverlay = ({
  selectedShip,
  selectedWaypoint,
  ships,
  waypoints,
  projectToScreen,
  getWaypointPosition,
  getShipPosition,
  frameTimestamp,
}: SelectionParams): SelectionOverlay[] => {
  return useMemo(() => {
    const overlays: SelectionOverlay[] = [];

    // Add ship selection overlay
    if (selectedShip) {
      const position = getShipPosition(selectedShip);
      if (position) {
        const screenPos = projectToScreen(position);
        if (screenPos) {
          overlays.push({
            left: screenPos.x,
            top: screenPos.y,
            size: DEFAULT_SIZE,
            type: 'ship',
          });
        }
      }
    }

    // Add waypoint selection overlay
    if (selectedWaypoint) {
      const displayPosition = getWaypointPosition(selectedWaypoint);
      const screenPos = projectToScreen(displayPosition);
      if (screenPos) {
        overlays.push({
          left: screenPos.x,
          top: screenPos.y,
          size: WAYPOINT_SIZE,
          type: 'waypoint',
        });
      }
    }

    return overlays;
  }, [selectedShip, selectedWaypoint, ships, waypoints, projectToScreen, getWaypointPosition, getShipPosition, frameTimestamp]);
};
