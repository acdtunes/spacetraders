import { useEffect, useState } from 'react';
import { useFlowStore } from '../store/flowStore';
import { useFlowsPolling } from '../hooks/useFlowsPolling';
import FlowGalaxyScene from '../components/flows/FlowGalaxyScene';
import { FlowDetailPanel } from '../components/flows/FlowDetailPanel';
import { SystemDrilldown } from '../components/flows/SystemDrilldown';
import { FeedLostChip } from '../components/flows/FeedLostChip';
import { NOIR } from '../theme/noir';
import type { FlowWindow } from '../types/flows';

const WINDOWS: FlowWindow[] = ['1h', '6h', '24h'];

export function TradeFlowsView() {
  useFlowsPolling();
  const window = useFlowStore((s) => s.window);
  const setWindow = useFlowStore((s) => s.setWindow);
  const live = useFlowStore((s) => s.live);
  const lanes = useFlowStore((s) => s.lanes);
  const lastPlanAt = useFlowStore((s) => s.lastPlanAt);
  const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
  const drilldownSystem = useFlowStore((s) => s.drilldownSystem);
  const closeDrilldown = useFlowStore((s) => s.closeDrilldown);
  const error = useFlowStore((s) => s.error);

  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  const flows = live?.flows ?? [];
  const selectedFlow = flows.find((f) => f.containerId === selectedFlowId) ?? null;

  return (
    <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
      <FlowGalaxyScene />

      {/* Window switch */}
      <div className="absolute bottom-4 left-4 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
        {WINDOWS.map((w) => (
          <button
            key={w}
            onClick={() => setWindow(w)}
            className="px-3 py-1 text-xs rounded"
            style={{
              background: window === w ? NOIR.accent : 'transparent',
              color: window === w ? NOIR.bg0 : NOIR.muted,
            }}
          >
            {w}
          </button>
        ))}
      </div>

      <FlowDetailPanel flow={selectedFlow} />
      {drilldownSystem && (
        <SystemDrilldown
          systemSymbol={drilldownSystem}
          lanes={lanes?.lanes ?? []}
          flows={flows}
          onClose={closeDrilldown}
        />
      )}
      <FeedLostChip feedLost={live?.feedLost ?? false} lastPlanAt={lastPlanAt} nowMs={nowMs} />

      {error && (
        <div className="absolute bottom-4 right-4 px-3 py-1.5 rounded text-xs" style={{ background: NOIR.panel, color: NOIR.bad }}>
          {error}
        </div>
      )}
    </div>
  );
}
