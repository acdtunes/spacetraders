import { useState } from 'react';
import type { LiveFlow, LanesResponse } from '../../types/flows';
import { flowIsRelocation } from './flowMotion';
import { ringSpec } from './profitRing';
import { NOIR, noirAlpha } from '../../theme/noir';

const money = (n: number) => Math.round(n).toLocaleString('en-US');
const WINDOW_HOURS: Record<string, number> = { '1h': 1, '6h': 6, '24h': 24 };
const legEta = (iso: string) => {
  const ms = Date.parse(iso) - Date.now();
  if (Number.isNaN(ms)) return '—';
  if (ms <= 0) return 'arrived';
  const secs = Math.floor(ms / 1000);
  return `${String(Math.floor(secs / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
};

interface Props {
  flows: LiveFlow[];
  lanes: LanesResponse | null;
  selectedFlowId: string | null;
  onRowClick: (containerId: string) => void;
}

function routeSummary(flow: LiveFlow): string {
  const systems: string[] = [];
  const push = (s: string) => { if (systems[systems.length - 1] !== s) systems.push(s); };
  if (flow.shipNav?.systemSymbol) push(flow.shipNav.systemSymbol);
  for (const h of flow.remainingHops) push(h.system);
  if (systems.length <= 1) return systems[0] ?? '—';
  if (flow.closed) return `${systems[0]} ⟳ (${systems.length} sys)`;
  return `${systems[0]} → ${systems[systems.length - 1]} (${systems.length} sys)`;
}

function badge(flow: LiveFlow): string {
  if (flowIsRelocation(flow)) return 'relocation';
  if (flow.program === 'tour') return flow.closed ? 'closed loop' : 'tour';
  return flow.program;
}

// Right-side roster: every active flow with projected vs realized, sorted by
// projected $/hr. Header carries fleet totals + the window's realized $/hr.
export function TourRoster({ flows, lanes, selectedFlowId, onRowClick }: Props) {
  const [collapsed, setCollapsed] = useState(false);
  const sorted = [...flows].sort((a, b) => (b.projected?.ratePerHour ?? 0) - (a.projected?.ratePerHour ?? 0));
  const totalProjected = flows.reduce((s, f) => s + (f.projected?.profit ?? 0), 0);
  const totalRealized = flows.reduce((s, f) => s + (f.realized?.net ?? 0), 0);
  const windowProfit = (lanes?.lanes ?? []).reduce((s, l) => s + l.realizedProfit, 0);
  const windowRate = lanes ? windowProfit / (WINDOW_HOURS[lanes.window] ?? 6) : 0;

  return (
    <div
      className="absolute top-4 right-4 w-72 max-h-[82vh] overflow-auto rounded-lg text-xs backdrop-blur"
      style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
    >
      <button
        className="w-full flex items-center justify-between px-3 py-2"
        style={{ color: NOIR.accent }}
        onClick={() => setCollapsed((c) => !c)}
      >
        <span className="uppercase tracking-wide">Active tours ({flows.length})</span>
        <span>{collapsed ? '▸' : '▾'}</span>
      </button>

      {!collapsed && (
        <>
          <div className="px-3 pb-2 grid grid-cols-2 gap-x-2" style={{ color: NOIR.muted }}>
            <span>Σ projected</span><span className="text-right" style={{ color: NOIR.ink }}>{money(totalProjected)}</span>
            <span>Σ realized</span><span className="text-right" style={{ color: totalRealized >= 0 ? NOIR.good : NOIR.bad }}>{money(totalRealized)}</span>
            <span>window $/hr</span><span className="text-right" style={{ color: NOIR.dim }}>{money(windowRate)}</span>
          </div>

          {sorted.map((f) => {
            const ring = ringSpec(f.realized?.net, f.projected?.profit ?? null);
            const fillPct = Math.round(ring.fill * 100);
            const selected = f.containerId === selectedFlowId;
            return (
              <div
                key={f.containerId}
                className="px-3 py-2 cursor-pointer border-t"
                style={{ borderColor: noirAlpha(NOIR.nebulaCore, 0.5), background: selected ? noirAlpha(NOIR.nebulaCore, 0.35) : 'transparent' }}
                onClick={() => onRowClick(f.containerId)}
              >
                <div className="flex justify-between">
                  <span className="font-mono" style={{ color: NOIR.ink }}>{f.ship}</span>
                  <span style={{ color: NOIR.accentSoft }}>{badge(f)}</span>
                </div>
                <div className="font-mono truncate" style={{ color: NOIR.dim }}>{routeSummary(f)}</div>
                {f.currentLeg && (
                  <div style={{ color: NOIR.warn }}>leg → {f.currentLeg.to} · ETA {legEta(f.currentLeg.arrivesAt)}</div>
                )}
                <div className="flex justify-between mt-1">
                  <span style={{ color: NOIR.muted }}>proj {f.projected ? money(f.projected.profit) : '—'}</span>
                  <span style={{ color: (f.realized?.net ?? 0) >= 0 ? NOIR.good : NOIR.bad }}>real {money(f.realized?.net ?? 0)}</span>
                </div>
                <div className="h-1 mt-1 rounded" style={{ background: noirAlpha(NOIR.ink, 0.12) }}>
                  <div className="h-1 rounded" style={{ width: `${fillPct}%`, background: ring.underGlow ?? ring.color }} />
                </div>
                {f.projected && (
                  <div className="text-right" style={{ color: NOIR.dim }}>{money(f.projected.ratePerHour)}/hr</div>
                )}
              </div>
            );
          })}
        </>
      )}
    </div>
  );
}
