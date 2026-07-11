import { memo } from 'react';
import { Group, Line } from 'react-konva';
import type { LanesResponse } from '../../types/flows';
import { laneEndpoints, laneProfitColor, laneWidth, type Point } from './flowGeometry';

interface Props {
  lanes: LanesResponse | null;
  systemPos: Map<string, Point>;
  scale: number;
  dashOffset: number;
}

// Gate/realized lanes: profit-colored, thickness by log(profit), animated dash
// for a subtle flow direction cue (mirrors TradeRouteLayer).
export const FlowLaneLayer = memo(function FlowLaneLayer({ lanes, systemPos, scale, dashOffset }: Props) {
  if (!lanes) return null;
  return (
    <Group listening={false}>
      {lanes.lanes.map((lane, i) => {
        const ep = laneEndpoints(lane, systemPos);
        if (!ep) return null;
        const color = laneProfitColor(lane.realizedProfit);
        const width = laneWidth(lane.realizedProfit, scale);
        const dash = 10 / scale;
        const gap = 6 / scale;
        return (
          <Line
            key={`lane-${i}-${lane.from}-${lane.to}`}
            points={[ep.from.x, ep.from.y, ep.to.x, ep.to.y]}
            stroke={color}
            strokeWidth={width}
            opacity={0.75}
            dash={[dash, gap]}
            dashOffset={dashOffset / scale}
            lineCap="round"
            listening={false}
          />
        );
      })}
    </Group>
  );
});
