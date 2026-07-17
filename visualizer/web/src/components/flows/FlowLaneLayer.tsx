import { memo } from 'react';
import { Group, Line, Arrow } from 'react-konva';
import type Konva from 'konva';
import type { SystemLaneRecord } from '../../types/flows';
import { noirAlpha } from '../../theme/noir';
import {
  laneEndpoints,
  laneProfitColor,
  laneWidth,
  offsetSegmentRight,
  pointAlong,
  laneDashPhase,
  partitionLanes,
  LANE_EMPHASIS_N,
  LANE_FLOOR_PCT,
  type Point,
} from './flowGeometry';

interface Props {
  records: SystemLaneRecord[] | null;
  systemPos: Map<string, Point>;
  scale: number;
  nowMs: number;
  // Hover over an artery's widened hit target. key = "from→to" (null on leave);
  // x/y are CLIENT coords (container rect + stage pointer) for the tooltip card.
  onLaneHover?: (key: string | null, x: number, y: number) => void;
}

// Realized directed lanes, decluttered at fleet scale: only the top-N lanes by
// |profit| (arteries) carry the full treatment — marching dash, mid-lane
// arrowhead, and an invisible widened hover target. The remaining floor-passing
// lanes render as faint solid capillaries (texture, not signal); sub-floor lanes
// are dropped entirely. Each lane is still nudged onto its right-hand normal so
// a pair realized in BOTH directions renders as two parallel strands.
export const FlowLaneLayer = memo(function FlowLaneLayer({ records, systemPos, scale, nowMs, onLaneHover }: Props) {
  if (!records) return null;
  const { arteries, capillaries } = partitionLanes(records, LANE_EMPHASIS_N, LANE_FLOOR_PCT);
  const offsetPx = 3.5 / scale;
  const dashPhase = laneDashPhase(nowMs, scale);

  const hoverAt = (key: string, e: Konva.KonvaEventObject<MouseEvent>) => {
    if (!onLaneHover) return;
    const stage = e.target.getStage();
    if (!stage) return;
    const pointer = stage.getPointerPosition();
    if (!pointer) return;
    const rect = stage.container().getBoundingClientRect();
    onLaneHover(key, rect.left + pointer.x, rect.top + pointer.y);
  };

  return (
    <Group>
      {capillaries.map((lane, i) => {
        const ep = laneEndpoints(lane, systemPos);
        if (!ep) return null;
        const seg = offsetSegmentRight(ep.from, ep.to, offsetPx);
        return (
          <Line
            key={`cap-${i}-${lane.from}-${lane.to}`}
            points={[seg.from.x, seg.from.y, seg.to.x, seg.to.y]}
            stroke={noirAlpha(laneProfitColor(lane.realizedProfit), 0.25)}
            strokeWidth={Math.max(0.3, 0.5 / scale)}
            lineCap="round"
            listening={false}
          />
        );
      })}

      {arteries.map((lane, i) => {
        const ep = laneEndpoints(lane, systemPos);
        if (!ep) return null;
        const seg = offsetSegmentRight(ep.from, ep.to, offsetPx);
        const color = laneProfitColor(lane.realizedProfit);
        const width = laneWidth(lane.realizedProfit, scale);
        const dash = 10 / scale;
        const gap = 6 / scale;
        const key = `${lane.from}→${lane.to}`;
        // Arrowhead a little short of the destination node, pointing toward it.
        const tail = pointAlong(seg.from, seg.to, 0.5);
        const head = pointAlong(seg.from, seg.to, 0.68);
        return (
          <Group key={`lane-${i}-${lane.from}-${lane.to}`}>
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
            {/* Invisible widened hit target — the artery itself is a hairline. */}
            <Line
              points={[seg.from.x, seg.from.y, seg.to.x, seg.to.y]}
              stroke="#000"
              strokeWidth={10 / scale}
              opacity={0}
              listening
              onMouseEnter={(e) => hoverAt(key, e)}
              onMouseMove={(e) => hoverAt(key, e)}
              onMouseLeave={() => onLaneHover?.(null, 0, 0)}
            />
          </Group>
        );
      })}
    </Group>
  );
});
