// Map-coverage notice: the one-line statement of what the map is NOT showing.
//
// /topology serves only systems with real era-scoped coordinates, because a
// system we cannot place cannot be drawn honestly (sp-fw6a2). That is the right
// call, but silence about it is not: an operator looking at 306 orbs must not
// conclude the era's galaxy IS 306 systems. This turns the omission into text.
//
// Pure — no React, no fetching. Returns null when there is nothing to say.
import type { LiveFlow, TopologyResponse } from '../../types/flows';

export interface CoverageNotice {
  /** One line, ready to render. */
  text: string;
  /** Live hulls whose current system is not on the map (roster still lists them). */
  hiddenHulls: number;
}

/** Locale-independent thousands grouping (deterministic in tests, unlike toLocaleString). */
export function groupThousands(n: number): string {
  if (!Number.isFinite(n)) return '0';
  const neg = n < 0;
  const digits = String(Math.abs(Math.trunc(n))).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return neg ? `-${digits}` : digits;
}

/**
 * Build the notice for a topology payload and the current live flows, or null
 * when the map is showing everything the era's gate graph knows about (and no
 * hull is off-map). Never throws on missing/partial input — an absent
 * `coverage` (older cached payload, fixture) simply yields null.
 */
export function coverageNotice(
  topology: TopologyResponse | null | undefined,
  flows: LiveFlow[] | null | undefined,
): CoverageNotice | null {
  const coverage = topology?.coverage;
  if (coverage == null) return null;
  const positioned = Number(coverage.positioned);
  const known = Number(coverage.known);
  if (!Number.isFinite(positioned) || !Number.isFinite(known)) return null;

  // A hull in an unplaceable system is dropped from the canvas along with its
  // system — count it so it never looks like a rendering bug.
  const drawn = new Set((topology?.systems ?? []).map((s) => s.symbol));
  let hiddenHulls = 0;
  for (const f of flows ?? []) {
    const sym = f?.shipNav?.systemSymbol;
    if (sym != null && sym !== '' && !drawn.has(sym)) hiddenHulls++;
  }

  const omitted = known - positioned;
  if (omitted <= 0 && hiddenHulls === 0) return null;

  // State the FRACTION, never a bare count: coverage moves (it climbed 213→378
  // in one session as the lazy backfill caught up), so "378 systems" would read
  // as "the galaxy is 378 systems". The percentage makes "partial" the headline
  // and leaves room for it to be different next poll.
  const pct = known > 0 ? Math.round((positioned / known) * 100) : 0;
  const parts = [`${groupThousands(positioned)} of ${groupThousands(known)} systems positioned (${pct}%)`];
  // Name the CAUSE, not just the count — a hull listed in the roster but absent
  // from the canvas has to be explicable from this line alone.
  if (hiddenHulls > 0) {
    parts.push(`${hiddenHulls} hull${hiddenHulls === 1 ? '' : 's'} in unpositioned systems`);
  }
  // Era resolution failing is WHY positioned is 0 — say which, or the empty map
  // reads as "we own nothing" instead of "we could not scope the era".
  if (coverage.eraId == null) parts.push('era unresolved');
  return { text: parts.join(' · '), hiddenHulls };
}
