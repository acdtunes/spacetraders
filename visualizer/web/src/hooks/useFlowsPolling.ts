import { useEffect } from 'react';
import { useFlowStore } from '../store/flowStore';
import { getFlowsLive, getFlowLanes, getFlowTopology, getFlowFreshness, getFlowFills } from '../services/api/flows';

const LIVE_INTERVAL_MS = 5000;
const LANES_INTERVAL_MS = 30000;
const FRESHNESS_INTERVAL_MS = 60000;
const FILLS_INTERVAL_MS = 15000;

export function useFlowsPolling() {
  const window = useFlowStore((s) => s.window);
  const setTopology = useFlowStore((s) => s.setTopology);
  const setLanes = useFlowStore((s) => s.setLanes);
  const setLive = useFlowStore((s) => s.setLive);
  const setError = useFlowStore((s) => s.setError);
  const setFreshness = useFlowStore((s) => s.setFreshness);
  const freshnessPollFailed = useFlowStore((s) => s.freshnessPollFailed);
  const setFills = useFlowStore((s) => s.setFills);

  // Topology once per mount.
  useEffect(() => {
    let cancelled = false;
    getFlowTopology()
      .then((t) => { if (!cancelled) setTopology(t); })
      .catch((e) => { if (!cancelled) setError(e?.message ?? 'topology failed'); });
    return () => { cancelled = true; };
  }, [setTopology, setError]);

  // Live feed every 5s.
  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getFlowsLive()
        .then((l) => { if (!cancelled) setLive(l); })
        .catch((e) => { if (!cancelled) setError(e?.message ?? 'live feed failed'); });
    };
    tick();
    const id = setInterval(tick, LIVE_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [setLive, setError]);

  // Lanes every 30s and immediately on window change.
  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getFlowLanes(window)
        .then((l) => { if (!cancelled) setLanes(l); })
        .catch((e) => { if (!cancelled) setError(e?.message ?? 'lanes failed'); });
    };
    tick();
    const id = setInterval(tick, LANES_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [window, setLanes, setError]);

  // Freshness every 60s. Failures do NOT surface through setError — they bump
  // the missed-poll counter so the halo layer dims honestly (spec §5).
  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getFlowFreshness()
        .then((f) => { if (!cancelled) setFreshness(f); })
        .catch(() => { if (!cancelled) freshnessPollFailed(); });
    };
    tick();
    const id = setInterval(tick, FRESHNESS_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [setFreshness, freshnessPollFailed]);

  // Fills every 15s — the ambient ticker. Purely decorative, so a failed poll is
  // silently skipped: no setError, no counter, no honest-degradation signalling.
  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getFlowFills()
        .then((f) => { if (!cancelled) setFills(f); })
        .catch(() => { /* silent skip — the ticker just misses this beat */ });
    };
    tick();
    const id = setInterval(tick, FILLS_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [setFills]);
}
