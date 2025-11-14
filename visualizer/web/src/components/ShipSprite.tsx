import { Group, Line, Image as KonvaImage } from 'react-konva';
import { useCachedImage } from '../hooks/useCachedImage';

interface ShipSpriteProps {
  assetPath: string | null;
  size: number;
}

export const ShipSprite = ({ assetPath, size }: ShipSpriteProps) => {
  const image = useCachedImage(assetPath);

  if (image && image.width > 0 && image.height > 0) {
    return (
      <KonvaImage
        image={image}
        x={-size / 2}
        y={-size / 2}
        width={size}
        height={size}
        listening={false}
      />
    );
  }

  const crossSize = Math.max(size * 0.65, 6);
  const crossHalf = crossSize / 2;
  const strokeWidth = Math.max(size * 0.08, 0.8);

  return (
    <Group listening={false}>
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
