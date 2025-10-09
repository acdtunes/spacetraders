import { memo, useMemo } from 'react';
import { Group, Circle, Rect } from 'react-konva';

interface StarfieldBackgroundProps {
  viewportBounds: { x: number; y: number; width: number; height: number; scale: number };
  currentScale: number;
}

/**
 * Generate a deterministic pseudo-random number based on seed
 */
function seededRandom(seed: number): number {
  const x = Math.sin(seed) * 10000;
  return x - Math.floor(x);
}

export const StarfieldBackground = memo(function StarfieldBackground({
  viewportBounds,
  currentScale,
}: StarfieldBackgroundProps) {
  const stars = useMemo(() => {
    const starList: Array<{ x: number; y: number; size: number; brightness: number; color: string }> = [];

    // Calculate visible area with padding
    const padding = 500;
    const minX = viewportBounds.x - padding;
    const maxX = viewportBounds.x + (viewportBounds.width / currentScale) + padding;
    const minY = viewportBounds.y - padding;
    const maxY = viewportBounds.y + (viewportBounds.height / currentScale) + padding;

    // Grid-based star generation for consistency
    const gridSize = 50;
    const startGridX = Math.floor(minX / gridSize);
    const endGridX = Math.ceil(maxX / gridSize);
    const startGridY = Math.floor(minY / gridSize);
    const endGridY = Math.ceil(maxY / gridSize);

    for (let gx = startGridX; gx <= endGridX; gx++) {
      for (let gy = startGridY; gy <= endGridY; gy++) {
        const seed = gx * 73856093 ^ gy * 19349663;

        // 3-5 stars per grid cell
        const numStars = Math.floor(seededRandom(seed) * 3) + 3;

        for (let i = 0; i < numStars; i++) {
          const starSeed = seed + i * 997;

          const x = gx * gridSize + seededRandom(starSeed) * gridSize;
          const y = gy * gridSize + seededRandom(starSeed + 1) * gridSize;

          // Random size (most small, some medium, few large)
          const sizeRoll = seededRandom(starSeed + 2);
          let size: number;
          if (sizeRoll < 0.7) {
            size = 0.5 + seededRandom(starSeed + 3) * 0.5; // Small stars
          } else if (sizeRoll < 0.9) {
            size = 1.0 + seededRandom(starSeed + 3) * 1.0; // Medium stars
          } else {
            size = 2.0 + seededRandom(starSeed + 3) * 1.5; // Large stars
          }

          // Brightness variation
          const brightness = 0.4 + seededRandom(starSeed + 4) * 0.6;

          // Star colors (mostly white, some colored)
          const colorRoll = seededRandom(starSeed + 5);
          let color: string;
          if (colorRoll < 0.7) {
            color = '#ffffff'; // White stars
          } else if (colorRoll < 0.8) {
            color = '#a8d8ea'; // Blue stars
          } else if (colorRoll < 0.9) {
            color = '#ffd89b'; // Yellow/orange stars
          } else {
            color = '#ffb3ba'; // Red stars
          }

          starList.push({ x, y, size, brightness, color });
        }
      }
    }

    return starList;
  }, [viewportBounds.x, viewportBounds.y, viewportBounds.width, viewportBounds.height, currentScale]);

  const nebulae = useMemo(() => {
    const nebulaeList: Array<{ x: number; y: number; size: number; color: string; opacity: number }> = [];

    // Calculate visible area with padding
    const padding = 1000;
    const minX = viewportBounds.x - padding;
    const maxX = viewportBounds.x + (viewportBounds.width / currentScale) + padding;
    const minY = viewportBounds.y - padding;
    const maxY = viewportBounds.y + (viewportBounds.height / currentScale) + padding;

    // Grid-based nebula generation (less dense than stars)
    const gridSize = 300;
    const startGridX = Math.floor(minX / gridSize);
    const endGridX = Math.ceil(maxX / gridSize);
    const startGridY = Math.floor(minY / gridSize);
    const endGridY = Math.ceil(maxY / gridSize);

    for (let gx = startGridX; gx <= endGridX; gx++) {
      for (let gy = startGridY; gy <= endGridY; gy++) {
        const seed = gx * 83492791 ^ gy * 28411639;

        // 20% chance of nebula in each grid cell
        if (seededRandom(seed) < 0.2) {
          const x = gx * gridSize + seededRandom(seed + 1) * gridSize;
          const y = gy * gridSize + seededRandom(seed + 2) * gridSize;
          const size = 80 + seededRandom(seed + 3) * 150;

          // Nebula colors
          const colorRoll = seededRandom(seed + 4);
          let color: string;
          if (colorRoll < 0.25) {
            color = '#ff6b9d'; // Pink nebula
          } else if (colorRoll < 0.5) {
            color = '#4a90e2'; // Blue nebula
          } else if (colorRoll < 0.75) {
            color = '#9b59b6'; // Purple nebula
          } else {
            color = '#e74c3c'; // Red nebula
          }

          const opacity = 0.08 + seededRandom(seed + 5) * 0.12;

          nebulaeList.push({ x, y, size, color, opacity });
        }
      }
    }

    return nebulaeList;
  }, [viewportBounds.x, viewportBounds.y, viewportBounds.width, viewportBounds.height, currentScale]);

  return (
    <Group listening={false}>
      {/* Nebulae - render first (background) */}
      {nebulae.map((nebula, i) => (
        <Circle
          key={`nebula-${i}`}
          x={nebula.x}
          y={nebula.y}
          radius={nebula.size}
          fill={nebula.color}
          opacity={nebula.opacity}
          listening={false}
        />
      ))}

      {/* Stars - render on top of nebulae */}
      {stars.map((star, i) => (
        <Circle
          key={`star-${i}`}
          x={star.x}
          y={star.y}
          radius={star.size / currentScale}
          fill={star.color}
          opacity={star.brightness}
          listening={false}
        />
      ))}
    </Group>
  );
});
