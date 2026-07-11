import { memo } from 'react';
import { Group, Line, Arrow } from 'react-konva';
import type { LanesResponse } from '../../types/flows';
import {
  laneEndpoints,
  laneProfitColor,
  laneWidth,
  offsetSegmentRight,
  pointAlong,
  laneDashPhase,
  type Point,
} from './flowGeometry';

interface Props {
  lanes: LanesResponse | null;
  systemPos: Map<string, Point>;
  scale: number;
  nowMs: number;
}

// Realized directed lanes. Each lane is nudged onto its right-hand normal so a
// pair realized in BOTH directions renders as two parallel lanes rather than one
// ambiguous line; a marching dash crawls toward the destination and a mid-lane
// arrowhead fixes the direction unambiguously in a still frame. Profit sets color
// and thickness (the TradeRouteLayer idiom).
export const FlowLaneLayer = memo(function FlowLaneLayer({ lanes, systemPos, scale, nowMs }: Props) {
  if (!lanes) return null;
  const offsetPx = 3.5 / scale;
  const dashPhase = laneDashPhase(nowMs, scale);
  return (
    <Group listening={false}>
      {lanes.lanes.map((lane, i) => {
        const ep = laneEndpoints(lane, systemPos);
        if (!ep) return null;
        const seg = offsetSegmentRight(ep.from, ep.to, offsetPx);
        const color = laneProfitColor(lane.realizedProfit);
        const width = laneWidth(lane.realizedProfit, scale);
        const dash = 10 / scale;
        const gap = 6 / scale;
        // Arrowhead a little short of the destination node, pointing toward it.
        const tail = pointAlong(seg.from, seg.to, 0.5);
        const head = pointAlong(seg.from, seg.to, 0.68);
        return (
          <Group key={`lane-${i}-${lane.from}-${lane.to}`} listening={false}>
            <Line
              points={[seg.from.x, seg.from.y, seg.to.x, seg.to.y]}
              stroke={color}
              strokeWidth={width}
              opacity={0.75}
              dash={[dash, gap]}
              dashOffset={dashPhase}
              lineCap="round"
              listening={false}
            />
            <Arrow
              points={[tail.x, tail.y, head.x, head.y]}
              stroke={color}
              fill={color}
              strokeWidth={width}
              pointerLength={Math.max(4, 7 / scale)}
              pointerWidth={Math.max(4, 7 / scale)}
              opacity={0.95}
              listening={false}
            />
          </Group>
        );
      })}
    </Group>
  );
});
