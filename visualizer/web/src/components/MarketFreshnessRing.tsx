import { memo } from 'react';
import { Circle } from 'react-konva';

interface MarketFreshnessRingProps {
  x: number;
  y: number;
  radius: number;
  lastUpdated: string | null;
  currentScale: number;
}

/**
 * Get color based on data age in hours
 */
function getFreshnessColor(hoursOld: number): string {
  if (hoursOld < 1) return '#10B981'; // green - Fresh (< 1 hour)
  if (hoursOld < 4) return '#F59E0B'; // amber - Moderate (1-4 hours)
  if (hoursOld < 12) return '#F97316'; // orange - Stale (4-12 hours)
  return '#EF4444'; // red - Very stale (> 12 hours)
}

/**
 * Calculate age in hours from ISO timestamp
 */
function getDataAge(lastUpdated: string): number {
  const now = Date.now();
  const updated = new Date(lastUpdated).getTime();
  return (now - updated) / (1000 * 60 * 60); // Convert to hours
}

export const MarketFreshnessRing = memo(function MarketFreshnessRing({
  x,
  y,
  radius,
  lastUpdated,
  currentScale,
}: MarketFreshnessRingProps) {
  if (!lastUpdated) {
    // No data - show gray ring
    return (
      <Circle
        x={x}
        y={y}
        radius={radius + 2}
        stroke="#6B7280"
        strokeWidth={Math.max(1.5, 3 / currentScale)}
        opacity={0.4}
        listening={false}
      />
    );
  }

  const age = getDataAge(lastUpdated);
  const color = getFreshnessColor(age);
  const strokeWidth = Math.max(1.5, 3 / currentScale);

  return (
    <Circle
      x={x}
      y={y}
      radius={radius + 2}
      stroke={color}
      strokeWidth={strokeWidth}
      opacity={0.7}
      listening={false}
    />
  );
});
