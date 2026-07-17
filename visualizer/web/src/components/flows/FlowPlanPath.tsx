import { memo } from 'react';
import { Group, Line, Circle } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import type { Point } from './flowGeometry';
import { planRoutePolylines, buildStops, flowIsRelocation, type Adjacency } from './flowMotion';
import { NOIR, noirAlpha } from '../../theme/noir';

interface Props {
  flow: LiveFlow;
  adj: Adjacency;
  systemPos: Map<string, Point>;
  scale: number;
}

// Planned intent over the gate graph. Profitable transitions carry a
// directional gradient (bright toward the next stop); trade-less transitions
// (closed-tour return legs, placement relocations) render cool and dashed.
// A closed tour's final stop is its anchor — ringed so the loop reads.
export const FlowPlanPath = memo(function FlowPlanPath({ flow, adj, systemPos, scale }: Props) {
  const segments = planRoutePolylines(flow, adj, systemPos);
  if (segments.length === 0) return null;
  const u = 1 / Math.max(scale, 1e-6);
  const relocation = flowIsRelocation(flow);
  const stops = buildStops(flow);
  const anchorSystem = flow.closed && stops.length > 0 ? stops[stops.length - 1].system : null;
  const anchorPos = anchorSystem ? systemPos.get(anchorSystem) : undefined;

  return (
    <Group listening={false}>
      {segments.map((seg, i) => {
        const cool = seg.deadhead || relocation;
        const last = seg.points.length;
        if (cool) {
          return (
            <Line
              key={`plan-${flow.containerId}-${i}`}
              points={seg.points}
              stroke={noirAlpha(NOIR.dim, 0.6)}
              strokeWidth={Math.max(0.5, 1.1 * u)}
              dash={[3 * u, 5 * u]}
              lineCap="round"
              opacity={Math.max(0.25, 0.55 - i * 0.08)}
              listening={false}
            />
          );
        }
        return (
          <Line
            key={`plan-${flow.containerId}-${i}`}
            points={seg.points}
            strokeLinearGradientStartPoint={{ x: seg.points[0], y: seg.points[1] }}
            strokeLinearGradientEndPoint={{ x: seg.points[last - 2], y: seg.points[last - 1] }}
            strokeLinearGradientColorStops={[0, noirAlpha(NOIR.accentSoft, 0.12), 1, noirAlpha(NOIR.accentSoft, 0.75)]}
            strokeWidth={Math.max(0.5, 1.4 * u)}
            lineCap="round"
            opacity={Math.max(0.25, 0.7 - i * 0.08)}
            listening={false}
          />
        );
      })}

      {flow.remainingHops.map((hop, i) => {
        const p = systemPos.get(hop.system);
        if (!p) return null;
        const dead = hop.tranches.length === 0;
        return (
          <Circle
            key={`hop-${flow.containerId}-${i}`}
            x={p.x}
            y={p.y}
            radius={Math.max(1.5, 3 * u)}
            fill={noirAlpha(dead ? NOIR.dim : NOIR.accentSoft, dead ? 0.35 : 0.5)}
            listening={false}
          />
        );
      })}

      {anchorPos && (
        <Circle
          x={anchorPos.x}
          y={anchorPos.y}
          radius={10 * u}
          stroke={NOIR.warn}
          strokeWidth={1 * u}
          dash={[2.5 * u, 2.5 * u]}
          opacity={0.85}
          listening={false}
        />
      )}
    </Group>
  );
});
