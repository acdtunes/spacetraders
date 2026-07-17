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

// Deterministic per-flow sideways nudge (in on-screen px) so two flows whose
// plans share a corridor render as parallel strands instead of one occluding
// the other (e.g. a relocation under a tour's brighter gradient line).
const PLAN_OFFSETS_PX = [-2.5, 2.5, -5, 5, 0];
function planOffsetPx(id: string): number {
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return PLAN_OFFSETS_PX[(h >>> 0) % PLAN_OFFSETS_PX.length];
}

// Translate a polyline along the normal of its overall first→last direction.
// Approximate for bent multi-system paths, exact for single edges — plenty at
// a 2-5px offset.
function nudgePoints(points: number[], offset: number): number[] {
  const n = points.length;
  const dx = points[n - 2] - points[0];
  const dy = points[n - 1] - points[1];
  const len = Math.hypot(dx, dy);
  if (len < 1e-6 || offset === 0) return points;
  const nx = (dy / len) * offset;
  const ny = (-dx / len) * offset;
  const out: number[] = new Array(n);
  for (let i = 0; i < n; i += 2) { out[i] = points[i] + nx; out[i + 1] = points[i + 1] + ny; }
  return out;
}

// Planned intent over the gate graph. Profitable transitions carry a
// directional gradient (bright toward the next stop); trade-less transitions
// (closed-tour return legs, placement relocations) render cool and dashed.
// A closed tour's final stop is its anchor — ringed so the loop reads.
export const FlowPlanPath = memo(function FlowPlanPath({ flow, adj, systemPos, scale }: Props) {
  const segments = planRoutePolylines(flow, adj, systemPos);
  if (segments.length === 0) return null;
  const u = 1 / Math.max(scale, 1e-6);
  const nudge = planOffsetPx(flow.containerId) * u;
  const relocation = flowIsRelocation(flow);
  const stops = buildStops(flow);
  const anchorSystem = flow.closed && stops.length > 0 ? stops[stops.length - 1].system : null;
  const anchorPos = anchorSystem ? systemPos.get(anchorSystem) : undefined;

  return (
    <Group listening={false}>
      {segments.map((seg, i) => {
        const cool = seg.deadhead || relocation;
        const pts = nudgePoints(seg.points, nudge);
        const last = pts.length;
        if (cool) {
          return (
            <Line
              key={`plan-${flow.containerId}-${i}`}
              points={pts}
              stroke={noirAlpha(NOIR.dim, 0.8)}
              strokeWidth={Math.max(0.5, 1.6 * u)}
              dash={[4 * u, 4 * u]}
              lineCap="round"
              opacity={Math.max(0.3, 0.7 - i * 0.08)}
              listening={false}
            />
          );
        }
        return (
          <Line
            key={`plan-${flow.containerId}-${i}`}
            points={pts}
            strokeLinearGradientStartPoint={{ x: pts[0], y: pts[1] }}
            strokeLinearGradientEndPoint={{ x: pts[last - 2], y: pts[last - 1] }}
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
