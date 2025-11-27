import { memo } from 'react';
import { Text, Circle, Group } from 'react-konva';
import type { ShipLabelInfo } from '../utils/shipDisplay';

interface ShipTaskBadgeProps {
  taskType: string | null;
  good: string | null;
  currentScale: number;
  labelInfo?: ShipLabelInfo | null;
}

const getTaskEmoji = (taskType: string | null): string => {
  if (!taskType) return '';
  switch (taskType.toUpperCase()) {
    case 'ACQUIRE':
      return '🛒'; // Buy raw material from export market
    case 'DELIVER':
      return '🚚'; // Deliver material to factory
    case 'COLLECT':
      return '📥'; // Buy produced good from factory
    case 'SELL':
      return '💰'; // Sell final product at demand market
    case 'LIQUIDATE':
      return '🔥'; // Sell orphaned cargo to recover investment
    default:
      return '📋';
  }
};

export const ShipTaskBadge = memo(function ShipTaskBadge({
  taskType,
  good,
  currentScale,
  labelInfo,
}: ShipTaskBadgeProps) {
  if (!taskType) {
    return null;
  }

  const emoji = getTaskEmoji(taskType);

  // Fixed screen size - does not scale with zoom
  const badgeSize = 6.0;
  const fontSize = 7.5;

  // Position badge side by side with operation badge
  // The operation badge is at labelInfo.offsetX/offsetY
  // We need to offset in world space, then let the scale handle screen size
  let offsetX = 4 / currentScale;
  let offsetY = 4 / currentScale;

  if (labelInfo) {
    // Position right next to the operation badge
    // Badge diameter is ~12px in screen space, so offset by 14px / currentScale in world space
    offsetX = labelInfo.offsetX + (14 / currentScale);
    offsetY = labelInfo.offsetY;
  }

  return (
    <Group x={offsetX} y={offsetY} scale={{ x: 1 / currentScale, y: 1 / currentScale }}>
      {/* Background circle */}
      <Circle radius={badgeSize} fill="#374151" opacity={0.9} />

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
