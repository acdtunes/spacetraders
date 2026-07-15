import { NOIR, noirAlpha } from '../../theme/noir';
import { ROLE_COLORS } from './ContractOpsScene';
import type { OpsShip } from '../../types/contractOps';

function eta(arrivalTime: string | null, nowMs: number): string | null {
  if (!arrivalTime) return null;
  const s = Math.floor((Date.parse(arrivalTime) - nowMs) / 1000);
  if (s <= 0) return 'arriving';
  const m = Math.floor(s / 60);
  return m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
}

export function ShipCard({ ship, nowMs, onClose }: { ship: OpsShip; nowMs: number; onClose: () => void }) {
  const color = ROLE_COLORS[ship.role];
  const arriving = eta(ship.arrivalTime, nowMs);
  return (
    <div
      className="absolute top-4 right-4 rounded-lg p-4 w-[280px] max-w-[calc(100vw-2rem)]"
      style={{ background: noirAlpha(NOIR.panel, 0.94), border: `1px solid ${noirAlpha(color, 0.45)}` }}
    >
      <div className="flex items-center justify-between mb-2">
        <span className="font-mono text-sm" style={{ color: NOIR.ink }}>{ship.symbol}</span>
        <button onClick={onClose} className="text-xs px-1.5 rounded" style={{ color: NOIR.muted }} aria-label="Close ship card">
          ✕
        </button>
      </div>
      <div className="flex items-center gap-2 mb-3">
        <span className="px-2 py-0.5 rounded text-[10px] font-mono uppercase tracking-wider" style={{ background: noirAlpha(color, 0.18), color }}>
          {ship.role}
        </span>
        {ship.registrationRole && (
          <span className="text-[10px] font-mono uppercase" style={{ color: NOIR.dim }}>{ship.registrationRole}</span>
        )}
        <span className="text-[11px] font-mono" style={{ color: NOIR.muted }}>
          {ship.navStatus === 'IN_TRANSIT' ? `→ ${ship.waypoint}${arriving ? ` · ${arriving}` : ''}` : `${ship.navStatus} · ${ship.waypoint}`}
        </span>
      </div>

      <div className="mb-2">
        <div className="flex justify-between text-[10px] font-mono mb-1" style={{ color: NOIR.dim }}>
          <span>CARGO</span>
          <span>{ship.cargoUnits}/{ship.cargoCapacity}u</span>
        </div>
        <div className="h-1.5 rounded-full overflow-hidden" style={{ background: noirAlpha(NOIR.dim, 0.25) }}>
          <div
            className="h-full rounded-full"
            style={{
              width: `${ship.cargoCapacity > 0 ? (ship.cargoUnits / ship.cargoCapacity) * 100 : 0}%`,
              background: color,
            }}
          />
        </div>
      </div>

      {ship.cargo.length > 0 ? (
        <div className="flex flex-col gap-1 mb-2">
          {ship.cargo.map((c) => (
            <div key={c.symbol} className="flex justify-between text-[11px] font-mono">
              <span style={{ color: NOIR.ink }}>{c.symbol}</span>
              <span style={{ color: NOIR.muted }}>{c.units}u</span>
            </div>
          ))}
        </div>
      ) : (
        <div className="text-[11px] font-mono mb-2" style={{ color: NOIR.dim }}>hold empty</div>
      )}

      {ship.containerId && (
        <div className="text-[9px] font-mono truncate pt-2" style={{ color: NOIR.dim, borderTop: `1px solid ${noirAlpha(NOIR.dim, 0.2)}` }} title={ship.containerId}>
          ⚙ {ship.containerId}
        </div>
      )}
    </div>
  );
}
