import type { LaneRecord, LiveFlow } from '../../types/flows';
import { NOIR } from '../../theme/noir';
import { systemOf } from './flowGeometry';

interface Props {
  systemSymbol: string;
  lanes: LaneRecord[];
  flows: LiveFlow[];
  onClose: () => void;
}

const money = (n: number) => n.toLocaleString('en-US');

// Drill-down: this system's local realized lanes (either endpoint in-system) and
// the flows currently resident/inbound, same grammar as the galaxy detail panel.
export function SystemDrilldown({ systemSymbol, lanes, flows, onClose }: Props) {
  const localLanes = lanes.filter((l) => systemOf(l.from) === systemSymbol || systemOf(l.to) === systemSymbol);
  const localFlows = flows.filter(
    (f) => f.shipNav?.systemSymbol === systemSymbol ||
      (f.currentLeg && (systemOf(f.currentLeg.from) === systemSymbol || systemOf(f.currentLeg.to) === systemSymbol)),
  );
  return (
    <div
      className="absolute inset-y-4 right-4 w-96 overflow-auto rounded-lg p-4 text-sm backdrop-blur"
      style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
    >
      <div className="flex items-center justify-between mb-3">
        <span className="font-mono" style={{ color: NOIR.accent }}>{systemSymbol}</span>
        <button onClick={onClose} className="text-xs px-2 py-1 rounded" style={{ color: NOIR.muted, border: `1px solid ${NOIR.dim}` }}>
          close
        </button>
      </div>

      <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Local realized lanes</div>
      {localLanes.length === 0 && <div className="text-xs" style={{ color: NOIR.dim }}>none in window</div>}
      {localLanes.map((l, i) => (
        <div key={i} className="flex justify-between text-xs font-mono mb-0.5">
          <span>{l.from} → {l.to}</span>
          <span style={{ color: l.realizedProfit >= 0 ? NOIR.good : NOIR.bad }}>{money(l.realizedProfit)}</span>
        </div>
      ))}

      <div className="text-xs mt-3 mb-1" style={{ color: NOIR.muted }}>Flows here</div>
      {localFlows.length === 0 && <div className="text-xs" style={{ color: NOIR.dim }}>none</div>}
      {localFlows.map((f) => (
        <div key={f.containerId} className="flex justify-between text-xs font-mono mb-0.5">
          <span>{f.ship}</span>
          <span style={{ color: NOIR.dim }}>{f.program}</span>
        </div>
      ))}
    </div>
  );
}
