import { useEffect, useRef, useState } from 'react';
import type { LaneRecord, LiveFlow, SystemFreshnessRecord } from '../../types/flows';
import type { Waypoint } from '../../types/spacetraders';
import { NOIR } from '../../theme/noir';
import { freshnessColor } from './freshness';
import { getWaypoints } from '../../services/api/systems';
import { useRafClock } from '../../hooks/useRafClock';
import { classifyLaneForSystem, residentFlows } from './drilldownGeometry';
import { DrilldownHeader } from './DrilldownHeader';
import { DrilldownScene } from './DrilldownScene';

interface Props {
  systemSymbol: string;
  lanes: LaneRecord[];
  flows: LiveFlow[];
  homeSystem: string | null;
  feedLost: boolean;
  selectedFlowId: string | null;
  onSelectFlow: (containerId: string) => void;
  onClose: () => void;
  freshness?: SystemFreshnessRecord | null;
}

// System drilldown v2: fetches the clicked system's waypoints (real intra-system
// x/y via the existing /systems/:sym/waypoints endpoint) and renders them TO SCALE
// with the actual realized trade routes, resident hulls, and daemon intent. The
// fetch/compose lives here; the geometry is pure (drilldownGeometry) and the canvas
// is DrilldownScene (Konva). The HOME badge shows when this is the headquarters.
export function SystemDrilldown({ systemSymbol, lanes, flows, homeSystem, feedLost, selectedFlowId, onSelectFlow, onClose, freshness }: Props) {
  const [waypoints, setWaypoints] = useState<Waypoint[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [dims, setDims] = useState({ w: 0, h: 0 });
  const nowMs = useRafClock();

  // Fetch this system's waypoints whenever the selection changes.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setWaypoints([]);
    getWaypoints(systemSymbol)
      .then((wps) => { if (!cancelled) setWaypoints(wps); })
      .catch((e) => { if (!cancelled) setError(e?.message ?? 'failed to load waypoints'); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [systemSymbol]);

  // Measure the scene body so the Konva stage fills it (and tracks resizes).
  useEffect(() => {
    const measure = () => {
      const el = bodyRef.current;
      if (el) setDims({ w: el.clientWidth, h: el.clientHeight });
    };
    measure();
    window.addEventListener('resize', measure);
    return () => window.removeEventListener('resize', measure);
  }, []);

  const isHome = homeSystem !== null && homeSystem === systemSymbol;
  const laneCount = lanes.filter((l) => classifyLaneForSystem(l, systemSymbol) !== 'external').length;
  const hullCount = residentFlows(flows, systemSymbol).length;
  const hasScene = !loading && !error && waypoints.length > 0 && dims.w > 0 && dims.h > 0;

  return (
    <div className="absolute inset-4 rounded-lg overflow-hidden flex flex-col shadow-2xl"
      style={{ background: NOIR.bg0, border: `1px solid ${NOIR.nebulaCore}` }}
    >
      <DrilldownHeader
        systemSymbol={systemSymbol}
        isHome={isHome}
        waypointCount={waypoints.length}
        laneCount={laneCount}
        hullCount={hullCount}
        loading={loading}
        error={error}
        feedLost={feedLost}
        onClose={onClose}
      />

      {/* Sensor line: solver visibility for this system (freshness endpoint). */}
      {freshness && (
        <div className="px-4 pb-1 text-xs" style={{ color: NOIR.muted, background: NOIR.bg0 }}>
          Sensor: <span style={{ color: freshnessColor(freshness.freshnessPct) }}>{freshness.freshnessPct}%</span> fresh
          {' '}({freshness.freshListings}/{freshness.totalListings} listings
          {freshness.freshestAt ? `, freshest ${Math.round((Date.now() - Date.parse(freshness.freshestAt)) / 60000)}m ago` : ''})
          {freshness.scoutPost ? ` · post: ${freshness.scoutPost.status}${freshness.scoutPost.hull ? ` (${freshness.scoutPost.hull})` : ''}` : ''}
        </div>
      )}

      <div ref={bodyRef} className="relative flex-1" style={{ background: NOIR.bg0 }}>
        {hasScene && (
          <DrilldownScene
            systemSymbol={systemSymbol}
            waypoints={waypoints}
            lanes={lanes}
            flows={flows}
            isHome={isHome}
            width={dims.w}
            height={dims.h}
            nowMs={nowMs}
            selectedFlowId={selectedFlowId}
            onSelectFlow={onSelectFlow}
          />
        )}
        {loading && (
          <div className="absolute inset-0 flex items-center justify-center text-sm" style={{ color: NOIR.muted }}>
            Charting {systemSymbol} waypoints…
          </div>
        )}
        {!loading && error && (
          <div className="absolute inset-0 flex items-center justify-center text-sm" style={{ color: NOIR.bad }}>
            {error}
          </div>
        )}
        {!loading && !error && waypoints.length === 0 && (
          <div className="absolute inset-0 flex items-center justify-center text-sm" style={{ color: NOIR.dim }}>
            No charted waypoints in {systemSymbol}
          </div>
        )}
      </div>
    </div>
  );
}
