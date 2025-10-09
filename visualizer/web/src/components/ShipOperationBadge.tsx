import { memo } from 'react';
import { Text, Circle, Group } from 'react-konva';
import { getOperationEmoji, getOperationColor } from '../utils/shipOperations';
import { SHIP_LABEL_FONT_SIZE } from '../constants/shipLabel';
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

  // Make badge smaller - same size or smaller than ship name (font size 10)
  const badgeSize = Math.max(2.5, 8 / currentScale);
  const fontSize = Math.max(6, SHIP_LABEL_FONT_SIZE / currentScale);

  // Position badge at top-left corner of name label if available
  let offsetX = 4;
  let offsetY = 4;

  if (labelInfo) {
    // Position at top-left of the label
    offsetX = labelInfo.offsetX - (badgeSize * labelInfo.labelScale);
    offsetY = labelInfo.offsetY - (badgeSize * labelInfo.labelScale);
  }

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
