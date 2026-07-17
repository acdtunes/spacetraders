import { memo } from 'react';
import { Group, Line, Arc, Ring, Circle, Text } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import type { Point } from './flowGeometry';
import { projectFlowMotion, type Adjacency } from './flowMotion';
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
  flows, adj, systemGates, systemPos, nowMs, scale, selectedFlowId, onSelect, onHover,
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
        return (
          <Group key={`ship-${flow.containerId}`} x={m.x} y={m.y}>
            {/* rotated body: wedge + trail */}
            <Group rotation={rotationDeg} listening={false}>
              <Line points={[-14 * u, 0, -5 * u, 0]} stroke={noirAlpha(color, 0.35)} strokeWidth={1.6 * u} lineCap="round" listening={false} />
              <Line points={[-22 * u, 0, -14 * u, 0]} stroke={noirAlpha(color, 0.15)} strokeWidth={1 * u} lineCap="round" listening={false} />
              <Line
                points={[6 * u, 0, -4 * u, 3.5 * u, -4 * u, -3.5 * u]}
                closed
                fill={color}
                stroke={noirAlpha(NOIR.ink, 0.5)}
                strokeWidth={0.4 * u}
                listening={false}
              />
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
