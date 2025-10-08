import { Circle, Group, Line, Image as KonvaImage } from 'react-konva';
import { useCachedImage } from '../hooks/useCachedImage';

interface WaypointSpriteProps {
  assetPath: string | null;
  x: number;
  y: number;
  radius: number;
  scale: number;
}

const MIN_WORLD_SIZE = 1.2;

export const WaypointSprite = ({ assetPath, x, y, radius, scale }: WaypointSpriteProps) => {
  const image = useCachedImage(assetPath);
  const size = Math.max(radius * 2, MIN_WORLD_SIZE);
  const half = size / 2;

  if (image && image.width > 0 && image.height > 0) {
    return (
      <KonvaImage
        image={image}
        x={x - half}
        y={y - half}
        width={size}
        height={size}
        listening={false}
      />
    );
  }

  const crossSize = Math.max(size * 0.75, 14 / Math.max(scale, 0.0001));
  const crossHalf = crossSize / 2;
  const strokeWidth = Math.max(2 / Math.max(scale, 0.0001), 0.8);

  return (
    <Group x={x} y={y} listening={false}>
      <Circle
        radius={size / 2}
        fill="#1f2937"
        stroke="#ef4444"
        strokeWidth={strokeWidth * 0.6}
        listening={false}
        opacity={0.4}
      />
      <Line
        points={[-crossHalf, -crossHalf, crossHalf, crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
      <Line
        points={[-crossHalf, crossHalf, crossHalf, -crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
    </Group>
  );
};
