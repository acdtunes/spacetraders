import { memo } from 'react';
import { Circle, Group, Arc } from 'react-konva';

interface SiphonActivityIndicatorProps {
  /** Current animation frame timestamp for smooth animation */
  frameTimestamp: number;
  /** Current zoom scale to maintain consistent visual size */
  currentScale: number;
  /** Whether the ship is actively siphoning (docked at gas giant) */
  isActive: boolean;
}

/**
 * Animated indicator showing gas siphoning activity.
 * Displays pulsing rings that expand outward when ship is actively siphoning.
 */
export const SiphonActivityIndicator = memo(function SiphonActivityIndicator({
  frameTimestamp,
  currentScale,
  isActive,
}: SiphonActivityIndicatorProps) {
  if (!isActive) {
    return null;
  }

  // Animation parameters
  const PULSE_PERIOD_MS = 2000; // One full pulse cycle
  const NUM_RINGS = 3;
  const MAX_RADIUS = 20;
  const MIN_RADIUS = 4;

  // Calculate animation progress (0 to 1)
  const cycleProgress = (frameTimestamp % PULSE_PERIOD_MS) / PULSE_PERIOD_MS;

  // Generate staggered rings
  const rings = [];
  for (let i = 0; i < NUM_RINGS; i++) {
    // Stagger each ring's phase
    const ringPhase = (cycleProgress + i / NUM_RINGS) % 1;

    // Ring expands from MIN_RADIUS to MAX_RADIUS
    const radius = MIN_RADIUS + ringPhase * (MAX_RADIUS - MIN_RADIUS);

    // Fade out as ring expands (quadratic easing)
    const opacity = (1 - ringPhase) * (1 - ringPhase) * 0.6;

    // Scale-independent size
    const scaledRadius = radius / currentScale;

    rings.push(
      <Circle
        key={i}
        radius={scaledRadius}
        stroke="#A78BFA" // violet-400 matching gas operation color
        strokeWidth={1.5 / currentScale}
        opacity={opacity}
        listening={false}
      />
    );
  }

  // Add rotating arc segments for visual interest
  const ARC_PERIOD_MS = 3000;
  const arcRotation = ((frameTimestamp % ARC_PERIOD_MS) / ARC_PERIOD_MS) * 360;

  return (
    <Group>
      {rings}
      {/* Inner rotating arc segments */}
      <Group rotation={arcRotation}>
        <Arc
          innerRadius={2 / currentScale}
          outerRadius={4 / currentScale}
          angle={60}
          rotation={0}
          fill="#A78BFA"
          opacity={0.5}
          listening={false}
        />
        <Arc
          innerRadius={2 / currentScale}
          outerRadius={4 / currentScale}
          angle={60}
          rotation={120}
          fill="#A78BFA"
          opacity={0.5}
          listening={false}
        />
        <Arc
          innerRadius={2 / currentScale}
          outerRadius={4 / currentScale}
          angle={60}
          rotation={240}
          fill="#A78BFA"
          opacity={0.5}
          listening={false}
        />
      </Group>
    </Group>
  );
});
