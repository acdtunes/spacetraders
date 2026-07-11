import { memo } from 'react';
import { Stage, Layer, Group, Line, Arrow, Circle, Text } from 'react-konva';
import type { LaneRecord, LiveFlow, FlowProgram } from '../../types/flows';
import { NOIR, noirAlpha } from '../../theme/noir';
import { laneProfitColor, laneWidth, type Point } from './flowGeometry';
import {
  fitToViewport,
  applyFit,
  buildWaypointIndex,
  gateAnchor,
  resolveLaneSegment,
  residentFlows,
  hullWaypointInSystem,
  intentWaypointsInSystem,
  type WaypointPoint,
} from './drilldownGeometry';

interface Props {
  systemSymbol: string;
  waypoints: WaypointPoint[];
  lanes: LaneRecord[];
  flows: LiveFlow[];
  isHome: boolean;
  width: number;
  height: number;
}

const PROGRAM_COLOR: Record<FlowProgram, string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

const PADDING = 56;

// "X1-UQ16-FF5F" -> "FF5F" (the waypoint-local label).
const shortLabel = (symbol: string): string => symbol.split('-').slice(2).join('-') || symbol;

// The system drilldown, rendered TO SCALE: waypoints at their real intra-system
// x/y (fit-to-viewport, aspect preserved), realized lanes drawn between the true
// positions (intra solid, cross-system exit/entry dashed toward the gate), resident
// hulls at their actual waypoints, and the daemon intent as a dashed waypoint path.
// Geometry is pre-applied in screen space so stroke weights stay constant.
export const DrilldownScene = memo(function DrilldownScene({
  systemSymbol,
  waypoints,
  lanes,
  flows,
  isHome,
  width,
  height,
}: Props) {
  const t = fitToViewport(waypoints, width, height, PADDING);
  const wpIndex = buildWaypointIndex(waypoints);
  const gate = gateAnchor(waypoints);
  const gateWaypoint = waypoints.find((w) => w.type === 'JUMP_GATE') ?? null;
  const residents = residentFlows(flows, systemSymbol);

  return (
    <Stage width={width} height={height}>
      <Layer>
        {/* Realized trade routes between true waypoint positions. */}
        <Group listening={false}>
          {lanes.map((lane, i) => {
            const seg = resolveLaneSegment(lane, systemSymbol, wpIndex, gate);
            if (!seg) return null;
            const a = applyFit(seg.from, t);
            const b = applyFit(seg.to, t);
            const color = laneProfitColor(seg.profit);
            const w = laneWidth(seg.profit, 1);
            const crossSystem = seg.kind !== 'intra';
            return (
              <Arrow
                key={`lane-${i}-${lane.from}-${lane.to}`}
                points={[a.x, a.y, b.x, b.y]}
                stroke={color}
                fill={color}
                strokeWidth={w}
                pointerLength={Math.max(5, w * 2)}
                pointerWidth={Math.max(5, w * 2)}
                opacity={crossSystem ? 0.55 : 0.85}
                dash={crossSystem ? [8, 6] : undefined}
                lineCap="round"
                listening={false}
              />
            );
          })}
        </Group>

        {/* Daemon intent (dashed, waypoint granularity) for resident flows. Empty
            by construction when the feed is lost (no flows) — never fabricated. */}
        <Group listening={false}>
          {residents.map((flow) => {
            const anchors = intentWaypointsInSystem(flow, systemSymbol);
            if (anchors.length < 2) return null;
            const pts: number[] = [];
            for (const sym of anchors) {
              const wp = wpIndex.get(sym);
              if (!wp) continue;
              const s = applyFit(wp, t);
              pts.push(s.x, s.y);
            }
            if (pts.length < 4) return null;
            return (
              <Line
                key={`intent-${flow.containerId}`}
                points={pts}
                stroke={noirAlpha(NOIR.accentSoft, 0.7)}
                strokeWidth={1.4}
                dash={[5, 5]}
                lineCap="round"
                listening={false}
              />
            );
          })}
        </Group>

        {/* Waypoints to scale. The jump gate is distinct; the home gate is ringed. */}
        <Group listening={false}>
          {waypoints.map((wp) => {
            const p = applyFit(wp, t);
            const isGate = wp.type === 'JUMP_GATE';
            const r = isGate ? 5 : 3;
            const fill = isGate ? NOIR.star : noirAlpha(NOIR.nebulaCore, 0.95);
            return (
              <Group key={`wp-${wp.symbol}`} x={p.x} y={p.y}>
                {isGate && isHome && (
                  <Circle radius={r + 5} stroke={NOIR.star} strokeWidth={1.5} opacity={0.9} />
                )}
                <Circle radius={r} fill={fill} stroke={NOIR.accent} strokeWidth={0.75} />
                <Text
                  text={shortLabel(wp.symbol)}
                  fontSize={9}
                  fill={isGate ? NOIR.star : NOIR.dim}
                  x={r + 2}
                  y={-r - 1}
                  listening={false}
                />
              </Group>
            );
          })}
        </Group>

        {/* Resident hull glyphs at their ACTUAL waypoints. */}
        <Group listening={false}>
          {residents.map((flow) => {
            const wpSym = hullWaypointInSystem(flow, systemSymbol);
            if (!wpSym) return null;
            const wp = wpIndex.get(wpSym);
            if (!wp) return null;
            const p: Point = applyFit(wp, t);
            const color = PROGRAM_COLOR[flow.program];
            return (
              <Group key={`hull-${flow.containerId}`} x={p.x} y={p.y}>
                <Circle radius={6} stroke={color} strokeWidth={1} opacity={0.55} />
                <Circle radius={3.5} fill={color} />
                <Text text={flow.ship} fontSize={9} fill={NOIR.muted} x={8} y={-4} listening={false} />
              </Group>
            );
          })}
        </Group>

        {/* Home anchor label near the gate when this is the headquarters system. */}
        {isHome && gateWaypoint && (
          (() => {
            const p = applyFit(gateWaypoint, t);
            return (
              <Text
                text="HOME GATE"
                fontSize={9}
                fontStyle="bold"
                fill={NOIR.star}
                x={p.x + 8}
                y={p.y + 6}
                listening={false}
              />
            );
          })()
        )}
      </Layer>
    </Stage>
  );
});
