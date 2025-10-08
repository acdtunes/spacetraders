import { useEffect, useState } from 'react';
import type { Waypoint as WaypointType } from '../types/spacetraders';

interface TooltipAnchor {
  symbol: string;
  worldX: number;
  worldY: number;
}

interface UseWaypointTooltipAnchorParams {
  selectedObject: { type: 'waypoint' | 'ship'; symbol: string } | null;
  waypoints: Map<string, WaypointType>;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
}

export const useWaypointTooltipAnchor = ({
  selectedObject,
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
    if (selectedObject?.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (waypoint) {
        showForWaypoint(waypoint);
        return;
      }
    }
    clearAnchor();
  }, [selectedObject, waypoints, getWaypointPosition]);

  return { anchor, showForWaypoint, clearAnchor };
};
