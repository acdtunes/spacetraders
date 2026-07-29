// Per-system detail for the SYSTEM band, LIFTED from the old SystemDrilldown:
// the same /systems/:sym/waypoints fetch (getWaypoints — moved, not redesigned,
// cancelled-flag and all), the same store-polled freshness record its sensor
// line displayed, plus the existing gate-construction endpoint (getGateProgress,
// botPolling's swallow-on-failure pattern) for the waypoint flagged
// isUnderConstruction. Stale responses are ignored on symbol change; a null
// symbol fetches nothing and yields a null detail.
import { useEffect, useMemo, useState } from 'react';
import type { GateProgress, Waypoint } from '../types/spacetraders';
import type { SystemFreshnessRecord } from '../types/flows';
import { getWaypoints } from '../services/api/systems';
import { getGateProgress } from '../services/api/bot';
import { useFlowStore } from '../store/flowStore';

export interface SystemDetail {
  symbol: string;
  waypoints: Waypoint[];
  /** This system's solver-visibility record (store-polled), when known. */
  freshness: SystemFreshnessRecord | null;
  /** Construction bill for the in-system site, when one exists and resolves. */
  gate: GateProgress | null;
}

export function useSystemDetail(
  symbol: string | null,
): { detail: SystemDetail | null; loading: boolean; error: string | null } {
  const [waypoints, setWaypoints] = useState<Waypoint[] | null>(null);
  const [gate, setGate] = useState<GateProgress | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const freshnessResp = useFlowStore((s) => s.freshness);

  // Fetch this system's waypoints whenever the selection changes (moved from
  // SystemDrilldown; a failed fetch degrades to the empty chart, never throws —
  // but the failure is REPORTED via `error` so the page can say why the focused
  // system is dark instead of rendering a silent empty band).
  useEffect(() => {
    if (symbol == null) {
      setWaypoints(null);
      setGate(null);
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setWaypoints(null);
    setGate(null);
    setError(null);
    getWaypoints(symbol)
      .then((wps) => {
        if (cancelled) return;
        setWaypoints(wps);
        // Gate bill is supplementary — swallow its failure (botPolling's rule)
        // so a missing construction site never blanks the chart.
        const site = wps.find((w) => w.isUnderConstruction);
        if (site != null) {
          getGateProgress(site.symbol)
            .then((g) => { if (!cancelled) setGate(g); })
            .catch(() => { if (!cancelled) setGate(null); });
        }
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        setWaypoints([]);
        setError(e instanceof Error && e.message ? e.message : 'failed to load waypoints');
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [symbol]);

  const detail = useMemo<SystemDetail | null>(() => {
    if (symbol == null || waypoints == null) return null;
    return {
      symbol,
      waypoints,
      freshness: freshnessResp?.systems.find((s) => s.system === symbol) ?? null,
      gate,
    };
  }, [symbol, waypoints, gate, freshnessResp]);

  return { detail, loading, error };
}
