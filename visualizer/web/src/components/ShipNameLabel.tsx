import { Group, Label, Tag, Text } from 'react-konva';
import { SHIP_LABEL_FONT_SIZE, SHIP_LABEL_PADDING_X, SHIP_LABEL_PADDING_Y } from '../constants/shipLabel';

interface ShipNameLabelProps {
  labelText: string;
  labelWidth: number;
  labelHeight: number;
  labelScale: number;
  offsetX: number;
  offsetY: number;
}

export const ShipNameLabel = ({
  labelText,
  labelWidth,
  labelHeight,
  labelScale,
  offsetX,
  offsetY,
}: ShipNameLabelProps) => (
  <Group listening={false} x={offsetX} y={offsetY}>
    <Group scale={{ x: labelScale, y: labelScale }} listening={false}>
      <Label>
        <Tag
          width={labelWidth + 12}
          height={labelHeight}
          fill="transparent"
          cornerRadius={3}
        />
        <Text
          x={SHIP_LABEL_PADDING_X}
          y={SHIP_LABEL_PADDING_Y / 1.5}
          width={labelWidth + 12 - SHIP_LABEL_PADDING_X * 2}
          height={labelHeight - SHIP_LABEL_PADDING_Y}
          fontSize={SHIP_LABEL_FONT_SIZE}
          fontFamily="'Inter', 'SF Pro Display', -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Helvetica Neue', Arial, sans-serif"
          fontStyle="600"
          fill="#ffcc66"
          shadowColor="#ff9933"
          shadowBlur={3}
          shadowOpacity={0.5}
          align="center"
          text={labelText}
        />
      </Label>
    </Group>
  </Group>
);
