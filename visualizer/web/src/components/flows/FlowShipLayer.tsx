import { memo } from 'react';
import { Group, Line, Arc, Ring, Circle, Rect, Text } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import type { Point } from './flowGeometry';
import {
  projectFlowMotion, isHeavyHauler, scheduleDriftSeconds, hashShip,
  DRIFT_AMBER_SECONDS, DRIFT_RED_SECONDS, type Adjacency,
} from './flowMotion';
import { ringSpec } from './profitRing';
import { NOIR, noirAlpha } from '../../theme/noir';

interface Props {
  flows: LiveFlow[];
  adj: Adjacency;
  systemGates: Map<string, string>;
  systemPos: Map<string, Point>;
  nowMs: number;
  scale: number;
  selectedFlowId: string | null;
  onSelect: (containerId: string) => void;
  onHover: (containerId: string | null) => void;
  opacityById?: Map<string, number>; // enter/exit fades + feed-loss dim (default: 1)
}

const PROGRAM_COLOR: Record<LiveFlow['program'], string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

// Oriented hull glyphs: a wedge nosing along the travel bearing with a fading
// comet trail, wrapped in an unrotated progress ring (realized ÷ projected).
// Position/bearing come from the nav-grounded motion model; the raf clock
// makes glides continuous between the 5s polls.
export const FlowShipLayer = memo(function FlowShipLayer({
  flows, adj, systemGates, systemPos, nowMs, scale, selectedFlowId, onSelect, onHover, opacityById,
}: Props) {
  return (
    <Group>
      {flows.map((flow) => {
        const m = projectFlowMotion(flow, adj, systemGates, systemPos, nowMs, scale);
        if (!m) return null;
        const color = PROGRAM_COLOR[flow.program];
        const selected = flow.containerId === selectedFlowId;
        const u = 1 / Math.max(scale, 1e-6); // 1 on-screen px in stage units
        const r = Math.max(2, 4 * u);
        const ring = ringSpec(flow.realized?.net, flow.projected?.profit ?? null);
        const rotationDeg = (m.bearingRad * 180) / Math.PI;
        const pulse = 0.4 + 0.25 * Math.sin(nowMs / 300);
        const heavy = isHeavyHauler(flow.shipNav);
        const gliding = m.mode === 'glide';
        const flicker = 0.5 + 0.5 * Math.sin(nowMs / 90 + (hashShip(flow.containerId) % 7));
        const flameLen = (heavy ? 7 : 5) * u * (0.7 + 0.3 * flicker);
        const engineGlow = gliding ? 0.9 : 0.25;
        const drift = scheduleDriftSeconds(flow, nowMs);
        return (
          <Group key={`ship-${flow.containerId}`} x={m.x} y={m.y} opacity={opacityById?.get(flow.containerId) ?? 1}>
            {/* rotated body: hull silhouette + trail */}
            <Group rotation={rotationDeg} listening={false} opacity={gliding ? 1 : 0.85}>
              {/* Comet trail: solid + bright enough to read over the dashed
                  lane beneath (the lane is the same hue — an 0.35-alpha trail
                  vanished into it on screen). */}
              <Line points={[-18 * u, 0, -5 * u, 0]} stroke={noirAlpha(color, 0.65)} strokeWidth={2.2 * u} lineCap="round" listening={false} />
              <Line points={[-30 * u, 0, -18 * u, 0]} stroke={noirAlpha(color, 0.3)} strokeWidth={1.3 * u} lineCap="round" listening={false} />
              {/* hull silhouette: heavy = broad twin-nacelle freighter, light = slender dart */}
              {heavy ? (
                <>
                  <Line points={[7 * u, 0, 2 * u, 4.5 * u, -6 * u, 4.5 * u, -8 * u, 2 * u, -8 * u, -2 * u, -6 * u, -4.5 * u, 2 * u, -4.5 * u]} closed fill={noirAlpha(NOIR.ink, 0.92)} stroke={noirAlpha(NOIR.dim, 0.8)} strokeWidth={0.4 * u} listening={false} />
                  <Line points={[-1 * u, -3.2 * u, -1 * u, 3.2 * u]} stroke={noirAlpha(NOIR.dim, 0.9)} strokeWidth={0.5 * u} listening={false} />
                  <Line points={[-4.5 * u, -3.2 * u, -4.5 * u, 3.2 * u]} stroke={noirAlpha(NOIR.dim, 0.9)} strokeWidth={0.5 * u} listening={false} />
                  <Line points={[5.5 * u, -1.2 * u, 1 * u, -1.2 * u]} stroke={color} strokeWidth={0.8 * u} lineCap="round" listening={false} />
                  <Rect x={-9.5 * u} y={-3.4 * u} width={2 * u} height={2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.5 * u} listening={false} />
                  <Rect x={-9.5 * u} y={1.4 * u} width={2 * u} height={2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.5 * u} listening={false} />
                </>
              ) : (
                <>
                  <Line points={[8 * u, 0, 1 * u, 2.6 * u, -5 * u, 1.8 * u, -5 * u, -1.8 * u, 1 * u, -2.6 * u]} closed fill={noirAlpha(NOIR.ink, 0.92)} stroke={noirAlpha(NOIR.dim, 0.8)} strokeWidth={0.4 * u} listening={false} />
                  <Circle x={4.2 * u} y={0} radius={0.9 * u} fill={noirAlpha(NOIR.accentSoft, 0.9)} listening={false} />
                  <Line points={[3 * u, 0, -3.5 * u, 0]} stroke={color} strokeWidth={0.7 * u} lineCap="round" listening={false} />
                  <Rect x={-6.5 * u} y={-1.1 * u} width={1.6 * u} height={2.2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.4 * u} listening={false} />
                </>
              )}
              {gliding && (
                <Line
                  points={[heavy ? -9.5 * u : -6.5 * u, 0, (heavy ? -9.5 : -6.5) * u - flameLen, 0]}
                  stroke={noirAlpha(NOIR.warn, 0.5 + 0.3 * flicker)}
                  strokeWidth={(heavy ? 2.4 : 1.6) * u}
                  lineCap="round"
                  listening={false}
                />
              )}
            </Group>

            {/* unrotated dress: ring, under-glow, selection, overshoot pulse */}
            <Ring innerRadius={r + 2.5 * u} outerRadius={r + 4 * u} fill={noirAlpha(NOIR.ink, 0.14)} listening={false} />
            {ring.underGlow && (
              <Ring innerRadius={r + 2.5 * u} outerRadius={r + 4 * u} fill={noirAlpha(ring.underGlow, 0.4)} listening={false} />
            )}
            {ring.fill > 0 && (
              <Arc
                innerRadius={r + 2.5 * u}
                outerRadius={r + 4 * u}
                angle={ring.fill * 360}
                rotation={-90}
                fill={ring.color}
                listening={false}
              />
            )}
            {ring.overshoot && (
              <Ring innerRadius={r + 5 * u} outerRadius={r + 6 * u} fill={noirAlpha(NOIR.star, pulse)} listening={false} />
            )}
            {selected && <Ring innerRadius={r + 7 * u} outerRadius={r + 7.8 * u} fill={noirAlpha(NOIR.ink, 0.9)} listening={false} />}
            {/* schedule-drift tick: amber behind plan, red badly behind (top of ring) */}
            {drift !== null && drift > DRIFT_AMBER_SECONDS && (
              <Arc innerRadius={r + 4.6 * u} outerRadius={r + 6 * u} angle={26} rotation={-103} fill={drift > DRIFT_RED_SECONDS ? NOIR.bad : NOIR.warn} listening={false} />
            )}

            {/* hit target */}
            <Circle
              radius={r + 6 * u}
              opacity={0}
              onMouseEnter={(e) => {
                const c = e.target.getStage()?.container();
                if (c) c.style.cursor = 'pointer';
                onHover(flow.containerId);
              }}
              onMouseLeave={(e) => {
                const c = e.target.getStage()?.container();
                if (c) c.style.cursor = 'default';
                onHover(null);
              }}
              onClick={() => onSelect(flow.containerId)}
              onTouchStart={() => onSelect(flow.containerId)}
            />
            {scale > 0.4 && (
              <Text text={flow.ship} fontSize={Math.max(6, 9 * u)} fill={NOIR.muted} x={r + 6 * u} y={-r} listening={false} />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
