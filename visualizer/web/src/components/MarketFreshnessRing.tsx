import { memo } from 'react';
import { Circle, Group } from 'react-konva';

interface MarketFreshnessRingProps {
  x: number;
  y: number;
  radius: number;
  lastUpdated: string | null;
  currentScale: number;
  animationFrame?: number;
}

/**
 * Get color based on data age in hours
 * Using custom color palette that transitions from green → yellow → orange → red
 */
function getFreshnessColor(hoursOld: number): string {
  if (hoursOld < 0.25) return '#7AE622'; // sgbus-green - Very fresh (< 15 min)
  if (hoursOld < 0.5) return '#90C01C'; // apple-green - Fresh (15-30 min)
  if (hoursOld < 1) return '#A59917'; // old-gold - Recent (30-60 min)
  if (hoursOld < 2) return '#BB7311'; // tigers-eye - Acceptable (1-2 hours)
  if (hoursOld < 3) return '#D14D0B'; // syracuse-red-orange - Moderate (2-3 hours)
  if (hoursOld < 4) return '#E62606'; // chili-red - Stale (3-4 hours)
  return '#FC0000'; // off-red - Extremely stale (> 4 hours)
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
  animationFrame = 0,
}: MarketFreshnessRingProps) {
  // Calculate pulse animation
  const pulsePhase = (animationFrame % 60) / 60;
  const pulseScale = 1 + Math.sin(pulsePhase * Math.PI * 2) * 0.3;

  if (!lastUpdated) {
    // No data - show gray ring (no pulse for unknown data)
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
  const baseRadius = radius + 2;
  const strokeWidth = Math.max(1.5, 3 / currentScale);

  // Pulse intensity varies smoothly with age - fresher data pulses more intensely
  const pulseIntensity =
    age < 0.25 ? 1.0 :     // < 15 min: maximum pulse
    age < 0.5 ? 0.95 :     // 15-30 min: very strong
    age < 1 ? 0.85 :       // 30-60 min: strong
    age < 2 ? 0.70 :       // 1-2 hours: good
    age < 3 ? 0.50 :       // 2-3 hours: moderate
    age < 4 ? 0.30 :       // 3-4 hours: weak
    0.15;                  // > 4 hours: minimal pulse (extremely stale)

  return (
    <Group listening={false}>
      {/* Outer pulsing glow ring */}
      <Circle
        x={x}
        y={y}
        radius={baseRadius * pulseScale * 1.2}
        fill={color}
        opacity={pulseIntensity * 0.15}
        listening={false}
      />

      {/* Main ring with pulsing opacity */}
      <Circle
        x={x}
        y={y}
        radius={baseRadius}
        stroke={color}
        strokeWidth={strokeWidth}
        opacity={0.6 + pulseIntensity * 0.2 * Math.sin(pulsePhase * Math.PI * 2)}
        listening={false}
      />

      {/* Inner glow */}
      <Circle
        x={x}
        y={y}
        radius={baseRadius * 0.8}
        fill={color}
        opacity={pulseIntensity * 0.1}
        listening={false}
      />
    </Group>
  );
});
