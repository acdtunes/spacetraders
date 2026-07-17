import type { LiveFlow } from '../../types/flows';
import { NOIR } from '../../theme/noir';

const money = (n: number) => n.toLocaleString('en-US');
const eta = (iso: string) => {
  const ms = Date.parse(iso) - Date.now();
  if (Number.isNaN(ms)) return '—';
  if (ms <= 0) return 'arrived';
  const secs = Math.floor(ms / 1000);
  return `${String(Math.floor(secs / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
};

interface Props {
  flow: LiveFlow | null;
}

// Glass side panel: program, tour id, current leg + ETA, cargo aboard, remaining
// hops with tranches, projected P&L. Renders only from what the flow actually
// carries (no intent invented for feed-lost hulls — those are never selected).
export function FlowDetailPanel({ flow }: Props) {
  if (!flow) return null;
  return (
    <div
      className="absolute top-4 left-4 w-80 max-h-[80vh] overflow-auto rounded-lg p-4 text-sm backdrop-blur"
      style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
    >
      <div className="flex items-center justify-between mb-2">
        <span className="uppercase tracking-wide text-xs" style={{ color: NOIR.accent }}>{flow.program}</span>
        <span className="font-mono" style={{ color: NOIR.ink }}>{flow.ship}</span>
      </div>
      {flow.tourId && (
        <div className="text-xs mb-2 font-mono truncate" style={{ color: NOIR.dim }}>{flow.tourId}</div>
      )}

      {flow.currentLeg && (
        <div className="mb-3">
          <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Current leg</div>
          <div className="font-mono text-xs">
            {flow.currentLeg.from} → {flow.currentLeg.to}
          </div>
          <div className="text-xs" style={{ color: NOIR.warn }}>ETA {eta(flow.currentLeg.arrivesAt)}</div>
        </div>
      )}

      {flow.cargo.length > 0 && (
        <div className="mb-3">
          <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Cargo</div>
          {flow.cargo.map((c, i) => (
            <div key={i} className="flex justify-between text-xs font-mono">
              <span>{c.good}</span>
              <span style={{ color: NOIR.dim }}>{c.units}</span>
            </div>
          ))}
        </div>
      )}

      {flow.remainingHops.length > 0 && (
        <div className="mb-3">
          <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Remaining hops</div>
          {flow.remainingHops.map((hop, i) => (
            <div key={i} className="mb-1">
              <div className="font-mono text-xs" style={{ color: NOIR.accentSoft }}>{hop.waypoint}</div>
              {hop.tranches.map((tr, j) => (
                <div key={j} className="flex justify-between text-xs font-mono pl-2">
                  <span>{tr.isBuy ? 'buy' : 'sell'} {tr.good}</span>
                  <span style={{ color: NOIR.dim }}>{tr.units} @ {money(tr.expectedUnitPrice)}</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}

      <div className="pt-2 border-t" style={{ borderColor: NOIR.nebulaCore }}>
        <div className="flex justify-between text-xs">
          <span style={{ color: NOIR.muted }}>Realized so far (net, incl. fuel)</span>
          <span style={{ color: flow.realized.net >= 0 ? NOIR.good : NOIR.bad }}>{money(flow.realized.net)}</span>
        </div>
        {flow.realized.lastEventAt && (
          <div className="text-xs text-right" style={{ color: NOIR.dim }}>last fill {eta(flow.realized.lastEventAt) === 'arrived' ? 'just now' : new Date(flow.realized.lastEventAt).toLocaleTimeString()}</div>
        )}
      </div>

      {flow.projected && (
        <div className="pt-2 border-t" style={{ borderColor: NOIR.nebulaCore }}>
          <div className="flex justify-between text-xs">
            <span style={{ color: NOIR.muted }}>Projected profit</span>
            <span style={{ color: flow.projected.profit >= 0 ? NOIR.good : NOIR.bad }}>{money(flow.projected.profit)}</span>
          </div>
          <div className="flex justify-between text-xs">
            <span style={{ color: NOIR.muted }}>Rate / hr</span>
            <span style={{ color: NOIR.dim }}>{money(flow.projected.ratePerHour)}</span>
          </div>
        </div>
      )}
    </div>
  );
}
