import { memo } from 'react';
import { Text, Circle, Group } from 'react-konva';
import { getOperationEmoji, getOperationColor } from '../utils/shipOperations';
import type { OperationType } from '../types/spacetraders';
import type { ShipLabelInfo } from '../utils/shipDisplay';

interface ShipOperationBadgeProps {
  operationType: OperationType | null;
  currentScale: number;
  labelInfo?: ShipLabelInfo | null;
}

export const ShipOperationBadge = memo(function ShipOperationBadge({
  operationType,
  currentScale,
  labelInfo,
}: ShipOperationBadgeProps) {
  if (!operationType || operationType === 'idle') {
    return null;
  }

  const emoji = getOperationEmoji(operationType);
  if (!emoji) return null;

  const color = getOperationColor(operationType);

  // Fixed screen size - does not scale with zoom
  const badgeSize = 6.0;
  const fontSize = 7.5;

  // Position badge at top-left corner of name label if available
  let offsetX = 4 / currentScale;
  let offsetY = 4 / currentScale;

  if (labelInfo) {
    // Position exactly at top-left corner of the label box
    offsetX = labelInfo.offsetX;
    offsetY = labelInfo.offsetY;
  }

  return (
    <Group x={offsetX} y={offsetY} scale={{ x: 1 / currentScale, y: 1 / currentScale }}>
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
