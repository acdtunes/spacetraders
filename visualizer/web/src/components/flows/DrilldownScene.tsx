import { memo } from 'react';
import { Stage, Layer, Group, Line, Arrow, Circle, Text } from 'react-konva';
import type { LaneRecord, LiveFlow, FlowProgram } from '../../types/flows';
import { NOIR, noirAlpha } from '../../theme/noir';
import { laneProfitColor, laneWidth, offsetSegmentRight, laneDashPhase } from './flowGeometry';
import {
  fitToViewport,
  applyFit,
  buildWaypointIndex,
  gateAnchor,
  resolveLaneSegment,
  residentFlows,
  intentWaypointsInSystem,
  tourRoutePathInSystem,
  interpolateHullInSystem,
  type StopKind,
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
  nowMs: number;
  selectedFlowId: string | null;
  onSelectFlow: (containerId: string) => void;
}

const PROGRAM_COLOR: Record<FlowProgram, string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

// Buy vs sell must be distinguishable at each stop: acquiring is accent-blue,
// realizing is good-green, a stop that does both is star-gold.
const STOP_COLOR: Record<StopKind, string> = {
  buy: NOIR.accent,
  sell: NOIR.good,
  mixed: NOIR.star,
  none: NOIR.muted,
};

const PADDING = 56;
const LANE_OFFSET_PX = 4;

// "X1-UQ16-FF5F" -> "FF5F" (the waypoint-local label).
const shortLabel = (symbol: string): string => symbol.split('-').slice(2).join('-') || symbol;

// The system drilldown, rendered TO SCALE: waypoints at their real intra-system
// x/y (fit-to-viewport, aspect preserved), realized lanes drawn between the true
// positions as marching directional arrows, the SELECTED flow's actual ordered
// tour route (numbered stops, buy/sell-colored) as a connected path, and resident
// hulls at their real-time interpolated positions. Geometry is pre-applied in
// screen space so stroke weights stay constant.
export const DrilldownScene = memo(function DrilldownScene({
  systemSymbol,
  waypoints,
  lanes,
  flows,
  isHome,
  width,
  height,
  nowMs,
  selectedFlowId,
  onSelectFlow,
}: Props) {
  const t = fitToViewport(waypoints, width, height, PADDING);
  const wpIndex = buildWaypointIndex(waypoints);
  const gate = gateAnchor(waypoints);
  const gateWaypoint = waypoints.find((w) => w.type === 'JUMP_GATE') ?? null;
  const residents = residentFlows(flows, systemSymbol);
  const dashPhase = laneDashPhase(nowMs, 1);

  const selectedFlow = residents.find((f) => f.containerId === selectedFlowId) ?? null;
  const routeAnchors = selectedFlow ? tourRoutePathInSystem(selectedFlow, systemSymbol, wpIndex, gate) : [];
  const routePts: number[] = [];
  for (const a of routeAnchors) {
    const s = applyFit(a.point, t);
    routePts.push(s.x, s.y);
  }

  return (
    <Stage width={width} height={height}>
      <Layer>
        {/* Realized trade routes between true waypoint positions — marching arrows
            whose heads (and dash crawl) fix the direction of value flow. */}
        <Group listening={false}>
          {lanes.map((lane, i) => {
            const seg = resolveLaneSegment(lane, systemSymbol, wpIndex, gate);
            if (!seg) return null;
            // Nudge onto the right-hand normal (screen space) so a lane realized
            // in both directions renders as two parallel arrows, not one overlap.
            const off = offsetSegmentRight(applyFit(seg.from, t), applyFit(seg.to, t), LANE_OFFSET_PX);
            const a = off.from;
            const b = off.to;
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
                dash={crossSystem ? [8, 6] : [10, 6]}
                dashOffset={dashPhase}
                lineCap="round"
                listening={false}
              />
            );
          })}
        </Group>

        {/* Ambient daemon intent (dashed) for resident flows OTHER than the one
            selected — the selected flow gets the richer numbered route below.
            Empty by construction on feed loss (no flows) — never fabricated. */}
        <Group listening={false}>
          {residents.filter((f) => f.containerId !== selectedFlowId).map((flow) => {
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
                stroke={noirAlpha(NOIR.accentSoft, 0.5)}
                strokeWidth={1.2}
                dash={[5, 5]}
                lineCap="round"
                listening={false}
              />
            );
          })}
        </Group>

        {/* The SELECTED flow's actual ordered tour route: a connected marching path
            through its in-system stops, each numbered in tour order and colored by
            buy/sell intent, ending at the gate when the route leaves the system. */}
        {routeAnchors.length >= 2 && (
          <Group listening={false}>
            <Line
              points={routePts}
              stroke={noirAlpha(NOIR.ink, 0.85)}
              strokeWidth={2}
              dash={[9, 6]}
              dashOffset={dashPhase}
              lineCap="round"
              lineJoin="round"
              listening={false}
            />
          </Group>
        )}

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

        {/* Numbered, buy/sell-colored stop markers for the selected route, drawn
            over the waypoints so the tour order reads clearly. */}
        <Group listening={false}>
          {routeAnchors.map((a) => {
            const p = applyFit(a.point, t);
            if (a.kind === 'depart') {
              // Where the ship set out from — a hollow origin dot, unnumbered.
              return (
                <Circle key="route-depart" x={p.x} y={p.y} radius={5} stroke={NOIR.muted} strokeWidth={1.25} listening={false} />
              );
            }
            if (a.kind === 'entry' || a.kind === 'exit') {
              // A gate transit: the route enters/leaves the system here.
              return (
                <Group key={`route-transit-${a.index}`} x={p.x} y={p.y} listening={false}>
                  <Circle radius={7} stroke={NOIR.muted} strokeWidth={1.25} dash={[3, 3]} />
                  <Text text={`${a.index}`} fontSize={9} fill={NOIR.muted} x={-3} y={-4.5} listening={false} />
                </Group>
              );
            }
            const color = STOP_COLOR[a.kind];
            return (
              <Group key={`route-stop-${a.index}`} x={p.x} y={p.y} listening={false}>
                <Circle radius={9} fill={noirAlpha(color, 0.9)} stroke={NOIR.bg0} strokeWidth={1.5} />
                <Text
                  text={`${a.index}`}
                  fontSize={11}
                  fontStyle="bold"
                  fill={NOIR.bg0}
                  width={18}
                  height={14}
                  offsetX={9}
                  offsetY={7}
                  align="center"
                  verticalAlign="middle"
                  listening={false}
                />
              </Group>
            );
          })}
        </Group>

        {/* Resident hulls at their real-time interpolated positions (in-flight hulls
            glide along the current leg; docked/orbiting hulls sit at their waypoint).
            Clicking a hull selects its flow, revealing that flow's numbered route. */}
        <Group>
          {residents.map((flow) => {
            const pos = interpolateHullInSystem(flow, systemSymbol, wpIndex, gate, nowMs);
            if (!pos) return null;
            const p = applyFit(pos, t);
            const color = PROGRAM_COLOR[flow.program];
            const selected = flow.containerId === selectedFlowId;
            return (
              <Group key={`hull-${flow.containerId}`} x={p.x} y={p.y}>
                {selected && <Circle radius={9} stroke={NOIR.ink} strokeWidth={1.5} opacity={0.95} />}
                <Circle radius={6} stroke={color} strokeWidth={1} opacity={0.55} />
                <Circle
                  radius={3.5}
                  fill={color}
                  onMouseEnter={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
                  onMouseLeave={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
                  onClick={() => onSelectFlow(flow.containerId)}
                  onTouchStart={() => onSelectFlow(flow.containerId)}
                />
                <Text text={flow.ship} fontSize={9} fill={NOIR.muted} x={8} y={-4} listening={false} />
              </Group>
            );
          })}
        </Group>

        {/* Buy/sell legend, shown only while a numbered route is on screen. */}
        {routeAnchors.length >= 2 && (
          <Group x={14} y={14} listening={false}>
            <Circle x={0} y={0} radius={5} fill={STOP_COLOR.buy} />
            <Text text="buy" fontSize={11} fill={NOIR.muted} x={9} y={-5} listening={false} />
            <Circle x={44} y={0} radius={5} fill={STOP_COLOR.sell} />
            <Text text="sell" fontSize={11} fill={NOIR.muted} x={53} y={-5} listening={false} />
          </Group>
        )}

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
