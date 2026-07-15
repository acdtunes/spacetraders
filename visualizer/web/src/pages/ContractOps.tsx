import { useEffect, useState } from 'react';
import { useContractOpsStore } from '../store/contractOpsStore';
import { useContractOpsPolling } from '../hooks/useContractOpsPolling';
import ContractOpsScene from '../components/contract/ContractOpsScene';
import { ContractCard } from '../components/contract/ContractCard';
import { PassStepper } from '../components/contract/PassStepper';
import { EventTicker } from '../components/contract/EventTicker';
import { ShipCard } from '../components/contract/ShipCard';
import { NOIR } from '../theme/noir';

export function ContractOps() {
  useContractOpsPolling();
  const live = useContractOpsStore((s) => s.live);
  const pass = useContractOpsStore((s) => s.pass);
  const selectedShip = useContractOpsStore((s) => s.selectedShip);
  const selectShip = useContractOpsStore((s) => s.selectShip);
  const error = useContractOpsStore((s) => s.error);

  // 1s wall clock for DOM countdowns/relative times (the canvas has its own
  // RAF clock; panels don't need frame-rate updates).
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  const selected = live?.ships.find((s) => s.symbol === selectedShip) ?? null;

  return (
    <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
      <ContractOpsScene />

      <ContractCard live={live} nowMs={nowMs} />
      <PassStepper />
      {pass >= 3 && live && <EventTicker events={live.events} nowMs={nowMs} />}
      {pass >= 2 && selected && <ShipCard ship={selected} nowMs={nowMs} onClose={() => selectShip(null)} />}

      {error && (
        <div className="absolute bottom-16 left-4 px-3 py-1.5 rounded text-xs" style={{ background: NOIR.panel, color: NOIR.bad }}>
          {error}
        </div>
      )}
    </div>
  );
}
