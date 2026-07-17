import { memo } from 'react';
import { Group, Circle, Rect } from 'react-konva';
import type { SystemFreshnessRecord } from '../../types/flows';
import type { Point } from './flowGeometry';
import { freshnessColor, haloAlpha } from './freshness';
import { NOIR } from '../../theme/noir';

interface Props {
  records: SystemFreshnessRecord[];
  systemPos: Map<string, Point>;
  scale: number;
  nowMs: number;
  degraded: boolean; // >=5 missed polls: honest 50% dim
}

// The sensor picture: a radial halo per sensed system (color+alpha ramp =
// solver visibility) and a diamond scout-post tick (solid manned / hollow
// unmanned / pulsing hollow relay). Systems absent from records stay dark.
export const FreshnessLayer = memo(function FreshnessLayer({ records, systemPos, scale, nowMs, degraded }: Props) {
  const u = 1 / Math.max(scale, 1e-6);
  const dim = degraded ? 0.5 : 1;
  return (
    <Group listening={false}>
      {records.map((r) => {
        const p = systemPos.get(r.system);
        if (!p) return null;
        const color = freshnessColor(r.freshnessPct);
        const radius = 26 * u;
        const markerSize = 3.2 * u;
        const relayPulse = 0.45 + 0.35 * Math.sin((nowMs / 1200) * Math.PI * 2);
        const post = r.scoutPost;
        return (
          <Group key={`fresh-${r.system}`} x={p.x} y={p.y} listening={false}>
            {r.totalListings > 0 && (
              <Circle
                radius={radius}
                fillRadialGradientStartPoint={{ x: 0, y: 0 }}
                fillRadialGradientEndPoint={{ x: 0, y: 0 }}
                fillRadialGradientStartRadius={0}
                fillRadialGradientEndRadius={radius}
                fillRadialGradientColorStops={[0, color, 1, 'rgba(0,0,0,0)']}
                opacity={haloAlpha(r.freshnessPct) * dim}
                listening={false}
              />
            )}
            {post && (
              <Rect
                x={9 * u}
                y={-9 * u}
                width={markerSize}
                height={markerSize}
                rotation={45}
                fill={post.status === 'manned' ? NOIR.accent : undefined}
                stroke={post.status === 'unmanned' ? NOIR.dim : NOIR.accent}
                strokeWidth={0.7 * u}
                opacity={(post.status === 'relay' ? relayPulse : 0.95) * dim}
                listening={false}
              />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
