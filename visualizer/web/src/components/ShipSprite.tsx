import { Circle, Group, Line, Image as KonvaImage } from 'react-konva';
import { useCachedImage } from '../hooks/useCachedImage';
import { usePrefersReducedMotion } from '../hooks/usePrefersReducedMotion';
import { NOIR } from '../theme/noir';
import { enginePulse } from '../utils/spriteGlow';

interface ShipSpriteProps {
  assetPath: string | null;
  size: number;
  inTransit: boolean;
  frameTimestamp: number;
}

export const ShipSprite = ({ assetPath, size, inTransit, frameTimestamp }: ShipSpriteProps) => {
  const image = useCachedImage(assetPath);
  const reducedMotion = usePrefersReducedMotion();

  // Sprite art points "up" (nose at local -y, stern at local +y) — the rotation
  // group adds +90deg (see calculateShipRotation), so the engine sits astern at +y.
  const engineGlow = inTransit ? (
    <Circle
      x={0}
      y={size * 0.5}
      radius={size * 0.3}
      fill={NOIR.accentSoft}
      opacity={enginePulse(frameTimestamp, reducedMotion)}
      shadowColor={NOIR.accent}
      shadowBlur={size * 1.8}
      shadowOpacity={0.9}
      perfectDrawEnabled={false}
      shadowForStrokeEnabled={false}
      listening={false}
    />
  ) : null;

  if (image && image.width > 0 && image.height > 0) {
    return (
      <>
        {engineGlow}
        <KonvaImage
          image={image}
          x={-size / 2}
          y={-size / 2}
          width={size}
          height={size}
          listening={false}
        />
      </>
    );
  }

  const crossSize = Math.max(size * 0.65, 6);
  const crossHalf = crossSize / 2;
  const strokeWidth = Math.max(size * 0.08, 0.8);

  return (
    <Group listening={false}>
      <Line
        points={[-crossHalf, -crossHalf, crossHalf, crossHalf]}
        stroke={NOIR.bad}
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
      <Line
        points={[-crossHalf, crossHalf, crossHalf, -crossHalf]}
        stroke={NOIR.bad}
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
    </Group>
  );
};
