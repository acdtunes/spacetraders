import { useFlowStore } from '../../store/flowStore';
import { NOIR } from '../../theme/noir';

// How many fills the ambient strip shows at once (newest first).
const TICKER_ROWS = 6;
// Older rows fade toward the top of the strip: opacity 1 − rank × this step.
const TICKER_FADE_STEP = 0.15;

// Signed compact credits: +132k, -72k, +2.2k, +5M. Shared with the lane/system
// tooltip's top-goods lines so credits read identically everywhere ambient.
export function signedCompact(n: number): string {
  const abs = Math.abs(n);
  const body =
    abs >= 1_000_000
      ? `${(abs / 1_000_000).toFixed(abs >= 10_000_000 ? 0 : 1)}M`
      : abs >= 1_000
        ? `${(abs / 1_000).toFixed(abs >= 10_000 ? 0 : 1)}k`
        : String(Math.round(abs));
  return `${n < 0 ? '-' : '+'}${body.replace(/\.0(?=[kM])/, '')}`;
}

// Ambient bottom strip of the newest realized fills (15s poll). flex-col-reverse
// pins the newest row to the bottom edge; each new fill id mounts with a short
// slide/fade-in (keyed by id, plain CSS — no animation lib). Purely decorative:
// pointer-events-none, renders nothing without data.
export function FillTicker() {
  const fills = useFlowStore((s) => s.fills);
  const rows = fills?.fills.slice(0, TICKER_ROWS) ?? [];
  if (rows.length === 0) return null;

  return (
    <div className="absolute bottom-12 left-4 right-4 pointer-events-none flex flex-col-reverse gap-0.5 text-xs font-mono z-10">
      <style>{`@keyframes fill-ticker-in { from { opacity: 0; transform: translateY(6px); } }`}</style>
      {rows.map((f, i) => (
        <div
          key={f.id}
          data-fill-id={f.id}
          style={{
            color: f.isBuy ? NOIR.warn : NOIR.good,
            opacity: 1 - i * TICKER_FADE_STEP,
            textShadow: `0 0 6px ${NOIR.bg0}`,
            animation: 'fill-ticker-in 300ms ease-out',
          }}
        >
          {`${f.ship} ${f.isBuy ? 'bought' : 'sold'} ${f.units} ${f.good} ${signedCompact(f.credits)} @ ${f.waypoint}`}
        </div>
      ))}
    </div>
  );
}
