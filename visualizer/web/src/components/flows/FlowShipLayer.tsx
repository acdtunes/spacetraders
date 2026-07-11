import { memo } from 'react';
import { Group, Circle, Text } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import { projectFlowShip, type Point } from './flowGeometry';
import { NOIR } from '../../theme/noir';

interface Props {
  flows: LiveFlow[];
  systemPos: Map<string, Point>;
  nowMs: number;
  scale: number;
  selectedFlowId: string | null;
  onSelect: (containerId: string) => void;
}

const PROGRAM_COLOR: Record<LiveFlow['program'], string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

// Hull glyphs glide along their current leg (interpolated per tick). Clicking a
// hull selects its flow for the detail panel.
export const FlowShipLayer = memo(function FlowShipLayer({
  flows, systemPos, nowMs, scale, selectedFlowId, onSelect,
}: Props) {
  return (
    <Group>
      {flows.map((flow) => {
        const pos = projectFlowShip(flow, systemPos, nowMs);
        if (!pos) return null;
        const color = PROGRAM_COLOR[flow.program];
        const selected = flow.containerId === selectedFlowId;
        const r = Math.max(2, 4 / scale);
        return (
          <Group key={`ship-${flow.containerId}`} x={pos.x} y={pos.y}>
            {selected && (
              <Circle radius={r + 3 / scale} stroke={NOIR.ink} strokeWidth={1 / scale} opacity={0.9} />
            )}
            <Circle
              radius={r}
              fill={color}
              onMouseEnter={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
              onMouseLeave={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
              onClick={() => onSelect(flow.containerId)}
              onTouchStart={() => onSelect(flow.containerId)}
            />
            {scale > 0.4 && (
              <Text
                text={flow.ship}
                fontSize={Math.max(6, 9 / scale)}
                fill={NOIR.muted}
                x={r + 2 / scale}
                y={-r}
                listening={false}
              />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
