import { memo } from 'react';
import { Text, Circle, Group } from 'react-konva';
import { getOperationEmoji, getOperationColor } from '../utils/shipOperations';
import type { OperationType } from '../types/spacetraders';

interface ShipOperationBadgeProps {
  operationType: OperationType | null;
  currentScale: number;
}

export const ShipOperationBadge = memo(function ShipOperationBadge({
  operationType,
  currentScale,
}: ShipOperationBadgeProps) {
  if (!operationType || operationType === 'idle') {
    return null;
  }

  const emoji = getOperationEmoji(operationType);
  if (!emoji) return null;

  const color = getOperationColor(operationType);
  const badgeSize = Math.max(3, 12 / currentScale);
  const fontSize = Math.max(8, 16 / currentScale);

  // Position badge to bottom-right of ship
  const offsetX = 4;
  const offsetY = 4;

  return (
    <Group x={offsetX} y={offsetY}>
      {/* Background circle */}
      <Circle radius={badgeSize} fill={color} opacity={0.9} />

      {/* Emoji */}
      <Text
        text={emoji}
        fontSize={fontSize}
        fill="white"
        align="center"
        verticalAlign="middle"
        offsetX={fontSize / 2}
        offsetY={fontSize / 2}
      />
    </Group>
  );
});
