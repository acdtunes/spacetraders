import { useMemo } from 'react';
import type { Waypoint as WaypointType } from '../types/spacetraders';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';

type LineConfig = { points: number[]; stroke: string; strokeWidth: number; opacity: number };
type LabelConfig = { text: string; x: number; y: number };

export interface GridLines {
  vertical: LineConfig[];
  horizontal: LineConfig[];
  labels: LabelConfig[];
}

export const useGridLines = (waypoints: Map<string, WaypointType>, scale: number): GridLines => {
  return useMemo(() => {
    if (waypoints.size === 0) return { vertical: [], horizontal: [], labels: [] };

    const waypointArray = Array.from(waypoints.values());
    let minX = Infinity;
    let maxX = -Infinity;
    let minY = Infinity;
    let maxY = -Infinity;

    waypointArray.forEach((wp) => {
      minX = Math.min(minX, wp.x);
      maxX = Math.max(maxX, wp.x);
      minY = Math.min(minY, wp.y);
      maxY = Math.max(maxY, wp.y);
    });

    const currentScale = scale || 1;
    const targetSpacing = VIEWPORT_CONSTANTS.GRID_TARGET_SPACING;
    const worldSpacing = targetSpacing / currentScale;

    const magnitude = Math.pow(10, Math.floor(Math.log10(worldSpacing)));
    let gridSpacing = magnitude;

    if (worldSpacing / magnitude >= 5) {
      gridSpacing = magnitude * 5;
    } else if (worldSpacing / magnitude >= 2) {
      gridSpacing = magnitude * 2;
    }

    const labelMultiplier = VIEWPORT_CONSTANTS.GRID_LABEL_MULTIPLIER;
    const labelSpacing = gridSpacing * labelMultiplier;

    const padding = gridSpacing * 2;
    minX = Math.floor((minX - padding) / gridSpacing) * gridSpacing;
    maxX = Math.ceil((maxX + padding) / gridSpacing) * gridSpacing;
    minY = Math.floor((minY - padding) / gridSpacing) * gridSpacing;
    maxY = Math.ceil((maxY + padding) / gridSpacing) * gridSpacing;

    const vertical: LineConfig[] = [];
    const horizontal: LineConfig[] = [];
    const labels: LabelConfig[] = [];

    for (let x = minX; x <= maxX; x += gridSpacing) {
      vertical.push({
        points: [x, minY, x, maxY],
        stroke: x === 0 ? '#444444' : '#222222',
        strokeWidth: x === 0 ? 1.5 : 0.5,
        opacity: x === 0 ? 0.5 : 0.2,
      });
      if (x % labelSpacing === 0) {
        labels.push({ text: x.toString(), x: x + 2, y: 5 });
      }
    }

    for (let y = minY; y <= maxY; y += gridSpacing) {
      horizontal.push({
        points: [minX, y, maxX, y],
        stroke: y === 0 ? '#444444' : '#222222',
        strokeWidth: y === 0 ? 1.5 : 0.5,
        opacity: y === 0 ? 0.5 : 0.2,
      });
      if (y % labelSpacing === 0 && y !== 0) {
        labels.push({ text: y.toString(), x: 5, y: y + 2 });
      }
    }

    return { vertical, horizontal, labels };
  }, [waypoints, scale]);
};
