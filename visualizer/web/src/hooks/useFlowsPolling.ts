import { useEffect } from 'react';
import { useFlowStore } from '../store/flowStore';
import { getFlowsLive, getFlowLanes, getFlowTopology } from '../services/api/flows';

const LIVE_INTERVAL_MS = 5000;
const LANES_INTERVAL_MS = 30000;

export function useFlowsPolling() {
  const window = useFlowStore((s) => s.window);
  const setTopology = useFlowStore((s) => s.setTopology);
  const setLanes = useFlowStore((s) => s.setLanes);
  const setLive = useFlowStore((s) => s.setLive);
  const setError = useFlowStore((s) => s.setError);

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
}
