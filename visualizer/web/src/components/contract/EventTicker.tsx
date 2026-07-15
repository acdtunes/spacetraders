import { NOIR, noirAlpha } from '../../theme/noir';
import type { OpsEvent } from '../../types/contractOps';

function relTime(iso: string, nowMs: number): string {
  const s = Math.max(0, Math.floor((nowMs - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h`;
}

function line(e: OpsEvent): { text: string; color: string } {
  switch (e.kind) {
    case 'stocking':
      return { text: `+${e.units} ${e.good} → warehouse ${e.waypoint} (${e.shipSymbol})`, color: NOIR.good };
    case 'withdrawal':
      return { text: `−${e.units} ${e.good} withdrawn @ ${e.waypoint}${e.contractId ? ' · contract' : ''} (${e.shipSymbol})`, color: NOIR.accent };
    case 'transaction': {
      const amount = e.amount ?? 0;
      const sign = amount >= 0 ? '+' : '−';
      return {
        text: `${sign}${Math.abs(amount).toLocaleString()} cr · ${e.description ?? ''}`.trim(),
        color: amount >= 0 ? NOIR.good : NOIR.warn,
      };
    }
  }
}

export function EventTicker({ events, nowMs }: { events: OpsEvent[]; nowMs: number }) {
  if (events.length === 0) return null;
  return (
    <div
      className="absolute bottom-4 right-4 rounded-lg p-3 w-[380px] max-w-[calc(100vw-2rem)] max-h-56 overflow-y-auto"
      style={{ background: noirAlpha(NOIR.panel, 0.94), border: `1px solid ${noirAlpha(NOIR.dim, 0.25)}` }}
    >
      <div className="text-[10px] font-mono tracking-[0.2em] mb-2" style={{ color: NOIR.muted }}>
        FLOW · LAST {Math.min(events.length, 8)} EVENTS
      </div>
      <div className="flex flex-col gap-1.5">
        {events.slice(0, 8).map((e, i) => {
          const l = line(e);
          return (
            <div key={`${e.at}-${i}`} className="flex items-baseline gap-2 text-[11px] font-mono">
              <span className="shrink-0 w-8 text-right" style={{ color: NOIR.dim }}>{relTime(e.at, nowMs)}</span>
              <span style={{ color: l.color }} className="truncate" title={l.text}>{l.text}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
