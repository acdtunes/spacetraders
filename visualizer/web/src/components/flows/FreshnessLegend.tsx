// The market-freshness legend: what the aura's colours mean, and — the part that
// matters most — what its FULL SCALE currently is.
//
// The scale is printed rather than assumed because it is derived per request from
// the era's observed rotation and moves with the map. A reader who cannot see the
// scale cannot tell a healthy 6h rotation from a stalled 3-day one; both would
// otherwise paint an identically-shaped ramp. Printing it turns a stall into a
// number that jumps, which is a louder signal than a saturated red map — a
// saturated red map is exactly what the old hardcoded 75-minute cutoff produced,
// and readers learned to ignore it.
import { FRESHNESS_RAMP, DARK_COLOR, SCOUT_COLOR, formatAge } from '../../nebula/freshness';
import { NOIR } from '../../theme/noir';
import type { RotationBoundBasis } from '../../types/flows';

const hex = (n: number) => `#${n.toString(16).padStart(6, '0')}`;

/** Plain-language gloss on how the server got the scale — so 'we measured this'
 * is never confused with 'we had nothing to measure'. */
export function basisNote(
  basis: RotationBoundBasis | undefined,
  marketsKnown: number,
): string {
  const denom = marketsKnown > 0 ? ` · ${marketsKnown.toLocaleString()} markets` : '';
  switch (basis) {
    case 'observed':
      return `full scale = p95 scan age${denom}`;
    case 'floor':
      return `full scale = floor (rotation faster than the floor)${denom}`;
    case 'ceiling':
      return `full scale = ceiling — rotation has stalled${denom}`;
    default:
      return 'no scan data — scale unknown';
  }
}

export interface FreshnessLegendProps {
  boundMinutes: number;
  basis: RotationBoundBasis | undefined;
  marketsKnown: number;
  /** Consecutive freshness-poll failures; ≥1 means what is drawn is stale. */
  missedPolls: number;
}

export function FreshnessLegend({ boundMinutes, basis, marketsKnown, missedPolls }: FreshnessLegendProps) {
  const gradient = `linear-gradient(90deg, ${FRESHNESS_RAMP.map(hex).join(', ')})`;
  const known = boundMinutes > 0 && basis !== 'unknown';

  // A single horizontal strip in the bottom control row, immediately right of the
  // layer toggles — the legend sits beside the control that turns it on, and that
  // row is the one band of chrome the fill ticker (bottom-12, growing UPWARD) and
  // the tour roster (top-right, max-h 82vh) both leave clear. A vertical card in
  // the bottom-left corner was tried first and rendered straight through the
  // ticker's text; the browser caught it, the unit tests could not have.
  return (
    <div
      className="absolute bottom-4 left-[33rem] flex items-center gap-3 rounded px-3 py-1.5 text-[10px] leading-none whitespace-nowrap"
      style={{ background: NOIR.panel, color: NOIR.muted }}
      data-testid="freshness-legend"
    >
      <span style={{ color: NOIR.ink }}>freshness</span>

      {/* The ramp — a single-hue ordinal scale, so the bar itself reads as ordered.
          The solid ring in front of it is not decoration: on the map, priced and
          dark are told apart by FORM (solid contour vs dashed one) because the
          orb underneath already owns the fresh end's hue. A key that showed only
          the colour bar would describe a channel the reader cannot use. */}
      <span className="flex items-center gap-1.5">
        <span
          className="inline-block rounded-full"
          style={{ width: 9, height: 9, border: `1px solid ${hex(FRESHNESS_RAMP[0])}` }}
          data-testid="freshness-priced-swatch"
        />
        <span>just scanned</span>
        <span className="h-1.5 rounded-sm" style={{ width: 72, background: gradient }} />
        <span data-testid="freshness-full-scale" style={{ color: known ? NOIR.ink : NOIR.dim }}>
          {known ? formatAge(boundMinutes) : '—'}
        </span>
      </span>

      {/* Dark and scout are separate STATES, not ramp positions — each gets its
          own swatch drawn in its own form (hollow dashed ring / filled diamond),
          so neither is ever read as "the far end of the scale". */}
      <span className="flex items-center gap-1.5">
        <span
          className="inline-block rounded-full"
          style={{ width: 9, height: 9, border: `1px dashed ${hex(DARK_COLOR)}` }}
        />
        <span>dark</span>
      </span>
      <span className="flex items-center gap-1.5">
        <span
          className="inline-block"
          style={{ width: 7, height: 7, background: hex(SCOUT_COLOR), transform: 'rotate(45deg)' }}
        />
        <span>scout</span>
      </span>

      <span style={{ color: NOIR.dim }}>{basisNote(basis, marketsKnown)}</span>
      {missedPolls > 0 && (
        <span style={{ color: NOIR.warn }} data-testid="freshness-stale-poll">
          {missedPolls} missed poll{missedPolls === 1 ? '' : 's'}
        </span>
      )}
    </div>
  );
}
