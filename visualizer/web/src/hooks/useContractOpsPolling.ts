import { useEffect } from 'react';
import { useContractOpsStore } from '../store/contractOpsStore';
import { getContractOpsLive, getContractOpsTopology } from '../services/api/contractOps';

const LIVE_INTERVAL_MS = 5000;
const TOPOLOGY_INTERVAL_MS = 120_000; // depot CLI edits are rare; refresh lazily

export function useContractOpsPolling() {
  const setTopology = useContractOpsStore((s) => s.setTopology);
  const setLive = useContractOpsStore((s) => s.setLive);
  const setError = useContractOpsStore((s) => s.setError);

  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getContractOpsTopology()
        .then((t) => { if (!cancelled) setTopology(t); })
        .catch((e) => { if (!cancelled) setError(e?.message ?? 'topology failed'); });
    };
    tick();
    const id = setInterval(tick, TOPOLOGY_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [setTopology, setError]);

  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      getContractOpsLive()
        .then((l) => { if (!cancelled) setLive(l); })
        .catch((e) => { if (!cancelled) setError(e?.message ?? 'live feed failed'); });
    };
    tick();
    const id = setInterval(tick, LIVE_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, [setLive, setError]);
}
