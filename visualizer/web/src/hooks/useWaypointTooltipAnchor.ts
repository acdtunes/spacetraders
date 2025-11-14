import { useEffect, useState } from 'react';
import type { Waypoint as WaypointType } from '../types/spacetraders';

interface TooltipAnchor {
  symbol: string;
  worldX: number;
  worldY: number;
}

interface UseWaypointTooltipAnchorParams {
  selectedObject: { type: 'waypoint' | 'ship'; symbol: string } | null;
  selectedWaypoint: WaypointType | null;
  waypoints: Map<string, WaypointType>;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
}

export const useWaypointTooltipAnchor = ({
  selectedObject,
  selectedWaypoint,
  waypoints,
  getWaypointPosition,
}: UseWaypointTooltipAnchorParams) => {
  const [anchor, setAnchor] = useState<TooltipAnchor | null>(null);

  const showForWaypoint = (waypoint: WaypointType) => {
    const position = getWaypointPosition(waypoint);
    setAnchor({ symbol: waypoint.symbol, worldX: position.x, worldY: position.y });
  };

  const clearAnchor = () => setAnchor(null);

  useEffect(() => {
    // Use selectedWaypoint from store if available
    if (selectedWaypoint) {
      showForWaypoint(selectedWaypoint);
      return;
    }

    // Fallback to selectedObject for backwards compatibility
    if (selectedObject?.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (waypoint) {
        showForWaypoint(waypoint);
        return;
      }
    }

    clearAnchor();
  }, [selectedObject, selectedWaypoint, waypoints, getWaypointPosition]);

  return { anchor, showForWaypoint, clearAnchor };
};
