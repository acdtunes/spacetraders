import { useEffect, useState } from 'react';
import { useFlowStore } from '../store/flowStore';
import { useFlowsPolling } from '../hooks/useFlowsPolling';
import FlowGalaxyScene from '../components/flows/FlowGalaxyScene';
import { FlowDetailPanel } from '../components/flows/FlowDetailPanel';
import { TourRoster } from '../components/flows/TourRoster';
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
  const topology = useFlowStore((s) => s.topology);
  const lastPlanAt = useFlowStore((s) => s.lastPlanAt);
  const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
  const selectFlow = useFlowStore((s) => s.selectFlow);
  const hoveredFlowId = useFlowStore((s) => s.hoveredFlowId);
  const requestFocus = useFlowStore((s) => s.requestFocus);
  const layerToggles = useFlowStore((s) => s.layerToggles);
  const toggleLayer = useFlowStore((s) => s.toggleLayer);
  const drilldownSystem = useFlowStore((s) => s.drilldownSystem);
  const closeDrilldown = useFlowStore((s) => s.closeDrilldown);
  const error = useFlowStore((s) => s.error);
  const freshness = useFlowStore((s) => s.freshness);

  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  const flows = live?.flows ?? [];
  const selectedFlow = flows.find((f) => f.containerId === selectedFlowId) ?? null;
  const hoveredFlow = flows.find((f) => f.containerId === hoveredFlowId) ?? null;

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

      {/* Layer toggles */}
      <div className="absolute bottom-4 left-44 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
        {(['lanes', 'paths', 'ships', 'freshness'] as const).map((k) => (
          <button
            key={k}
            onClick={() => toggleLayer(k)}
            className="px-3 py-1 text-xs rounded capitalize"
            style={{
              background: layerToggles[k] ? NOIR.accent : 'transparent',
              color: layerToggles[k] ? NOIR.bg0 : NOIR.muted,
            }}
          >
            {k}
          </button>
        ))}
      </div>

      <FlowDetailPanel flow={hoveredFlow ?? selectedFlow} />
      <TourRoster
        flows={flows}
        lanes={lanes}
        selectedFlowId={selectedFlowId}
        onRowClick={(id) => { selectFlow(id); requestFocus(id); }}
      />
      {drilldownSystem && (
        <SystemDrilldown
          systemSymbol={drilldownSystem}
          lanes={lanes?.lanes ?? []}
          flows={flows}
          homeSystem={topology?.homeSystem ?? null}
          feedLost={live?.feedLost ?? false}
          selectedFlowId={selectedFlowId}
          onSelectFlow={selectFlow}
          onClose={closeDrilldown}
          freshness={freshness?.systems.find((s) => s.system === drilldownSystem) ?? null}
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
