import { NOIR } from '../../theme/noir';
import type { FlowProgram } from '../../types/flows';

interface Props {
  systemSymbol: string;
  isHome: boolean;
  waypointCount: number;
  laneCount: number;
  hullCount: number;
  loading: boolean;
  error: string | null;
  feedLost: boolean;
  onClose: () => void;
}

const PROGRAM_COLOR: Record<FlowProgram, string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

// The drilldown chrome (HTML, so jsdom-testable): system identity, the HOME badge
// when viewing the headquarters system, a live count summary, the program legend,
// and the close control. The waypoint scene below it is Konva (screenshot-verified).
export function DrilldownHeader({
  systemSymbol,
  isHome,
  waypointCount,
  laneCount,
  hullCount,
  loading,
  error,
  feedLost,
  onClose,
}: Props) {
  return (
    <div
      className="flex items-center justify-between px-4 py-2 border-b"
      style={{ borderColor: NOIR.nebulaCore, background: `${NOIR.panel}F2` }}
    >
      <div className="flex items-center gap-3 min-w-0">
        <span className="font-mono text-sm" style={{ color: NOIR.accent }}>{systemSymbol}</span>
        {isHome && (
          <span
            className="text-[10px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded"
            style={{ background: NOIR.star, color: NOIR.bg0 }}
            aria-label="home system"
          >
            ★ Home
          </span>
        )}
        <span className="text-xs truncate" style={{ color: NOIR.muted }}>
          {loading
            ? 'charting waypoints…'
            : error
              ? error
              : `${waypointCount} waypoints · ${laneCount} lanes · ${hullCount} ${hullCount === 1 ? 'hull' : 'hulls'}`}
        </span>
        {feedLost && !loading && (
          <span className="text-[10px] font-mono" style={{ color: NOIR.bad }}>· feed lost (no intent)</span>
        )}
      </div>

      <div className="flex items-center gap-3">
        <div className="hidden sm:flex items-center gap-2">
          {(['tour', 'trade-route', 'arb'] as FlowProgram[]).map((p) => (
            <span key={p} className="flex items-center gap-1 text-[10px]" style={{ color: NOIR.dim }}>
              <span className="inline-block w-2 h-2 rounded-full" style={{ background: PROGRAM_COLOR[p] }} />
              {p}
            </span>
          ))}
        </div>
        <button
          onClick={onClose}
          className="text-xs px-2 py-1 rounded"
          style={{ color: NOIR.muted, border: `1px solid ${NOIR.dim}` }}
        >
          close
        </button>
      </div>
    </div>
  );
}
