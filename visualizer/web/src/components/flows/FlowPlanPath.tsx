import { memo } from 'react';
import { Group, Line, Circle } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import { planPathPoints, systemOf, type Point } from './flowGeometry';
import { NOIR, noirAlpha } from '../../theme/noir';

interface Props {
  flow: LiveFlow;
  systemPos: Map<string, Point>;
  scale: number;
}

// The uniquely-daemon-provided intent: remaining planned hops as a dimming dashed
// path with a marker per hop. Rendered only when the flow HAS a plan (never
// synthesized) — the caller passes only flows with remainingHops.
export const FlowPlanPath = memo(function FlowPlanPath({ flow, systemPos, scale }: Props) {
  const segments = planPathPoints(flow, systemPos);
  if (segments.length === 0) return null;
  const dash = 6 / scale;
  return (
    <Group listening={false}>
      {segments.map((seg, i) => (
        <Line
          key={`plan-${flow.containerId}-${i}`}
          points={seg}
          stroke={NOIR.accentSoft}
          strokeWidth={Math.max(0.5, 1.4 / scale)}
          opacity={0.55 - i * 0.08}
          dash={[dash, dash]}
          lineCap="round"
          listening={false}
        />
      ))}
      {flow.remainingHops.map((hop, i) => {
        const p = systemPos.get(systemOf(hop.waypoint));
        if (!p) return null;
        return (
          <Circle
            key={`hop-${flow.containerId}-${i}`}
            x={p.x}
            y={p.y}
            radius={Math.max(1.5, 3 / scale)}
            fill={noirAlpha(NOIR.accentSoft, 0.5)}
            listening={false}
          />
        );
      })}
    </Group>
  );
});
