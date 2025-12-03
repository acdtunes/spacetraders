import { memo } from 'react';
import { Rect, Group } from 'react-konva';

interface StorageShipIndicatorProps {
  /** Current animation frame timestamp for smooth animation */
  frameTimestamp: number;
  /** Current zoom scale to maintain consistent visual size */
  currentScale: number;
  /** Whether the storage ship is actively receiving cargo */
  isActive: boolean;
}

/**
 * Animated indicator showing storage ship buffering activity.
 * Displays stacked bars that pulse when actively receiving cargo.
 */
export const StorageShipIndicator = memo(function StorageShipIndicator({
  frameTimestamp,
  currentScale,
  isActive,
}: StorageShipIndicatorProps) {
  if (!isActive) {
    return null;
  }

  // Animation parameters
  const PULSE_PERIOD_MS = 1500;
  const NUM_BARS = 3;
  const BAR_WIDTH = 12;
  const BAR_HEIGHT = 3;
  const BAR_GAP = 2;

  // Calculate animation progress (0 to 1)
  const cycleProgress = (frameTimestamp % PULSE_PERIOD_MS) / PULSE_PERIOD_MS;

  // Generate stacked bars with staggered animation
  const bars = [];
  for (let i = 0; i < NUM_BARS; i++) {
    // Stagger each bar's phase
    const barPhase = (cycleProgress + i * 0.2) % 1;

    // Pulse opacity using sine wave
    const opacity = 0.4 + 0.4 * Math.sin(barPhase * Math.PI * 2);

    // Scale-independent size
    const scaledWidth = BAR_WIDTH / currentScale;
    const scaledHeight = BAR_HEIGHT / currentScale;
    const scaledGap = BAR_GAP / currentScale;

    // Position bars vertically stacked above the ship
    const yOffset = -(i + 1) * (scaledHeight + scaledGap) - 8 / currentScale;

    bars.push(
      <Rect
        key={i}
        x={-scaledWidth / 2}
        y={yOffset}
        width={scaledWidth}
        height={scaledHeight}
        fill="#7C3AED" // violet-600 matching gas-storage color
        cornerRadius={1 / currentScale}
        opacity={opacity}
        listening={false}
      />
    );
  }

  return <Group>{bars}</Group>;
});
