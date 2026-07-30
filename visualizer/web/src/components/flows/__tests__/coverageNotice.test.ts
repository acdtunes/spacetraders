// sp-fw6a2 — /topology now serves only systems it can place truthfully, so the
// map is a subset of the era's gate graph. These pin that the omission is
// STATED: an operator must not read 306 orbs as "the galaxy is 306 systems",
// and a hull dropped along with its unplaceable system must not look like a bug.
import { describe, it, expect } from 'vitest';
import { coverageNotice, groupThousands } from '../coverageNotice';
import type { LiveFlow, TopologyCoverage, TopologyResponse } from '../../../types/flows';

const topo = (
  coverage: TopologyCoverage | undefined,
  symbols: string[] = [],
): TopologyResponse => ({
  systems: symbols.map((symbol, i) => ({ symbol, x: i * 10, y: 0, layout: 'real' as const })),
  edges: [],
  ...(coverage ? { coverage } : {}),
  generatedAt: new Date(0).toISOString(),
});

const flowIn = (system: string | null): LiveFlow =>
  ({ shipNav: system == null ? null : { systemSymbol: system } }) as unknown as LiveFlow;

describe('groupThousands', () => {
  it('groups deterministically regardless of locale', () => {
    expect(groupThousands(0)).toBe('0');
    expect(groupThousands(306)).toBe('306');
    expect(groupThousands(1293)).toBe('1,293');
    expect(groupThousands(1234567)).toBe('1,234,567');
    expect(groupThousands(-1500)).toBe('-1,500');
  });

  it('never throws on non-finite input', () => {
    expect(groupThousands(NaN)).toBe('0');
    expect(groupThousands(Infinity)).toBe('0');
  });
});

describe('coverageNotice', () => {
  it('states the positioned FRACTION, not a bare count', () => {
    // Coverage moves (213 -> 378 in one session), so the percentage has to be
    // computed from the payload — a bare "306 systems" reads as the galaxy's size.
    const n = coverageNotice(topo({ positioned: 306, known: 1293, omittedEdges: 4134, eraId: 5 }), []);
    expect(n?.text).toBe('306 of 1,293 systems positioned (24%)');
  });

  it('recomputes the percentage as coverage grows (nothing pinned to one number)', () => {
    const at213 = coverageNotice(topo({ positioned: 213, known: 1293, omittedEdges: 0, eraId: 5 }), []);
    const at378 = coverageNotice(topo({ positioned: 378, known: 1293, omittedEdges: 0, eraId: 5 }), []);
    expect(at213?.text).toBe('213 of 1,293 systems positioned (16%)');
    expect(at378?.text).toBe('378 of 1,293 systems positioned (29%)');
  });

  it('does not divide by zero when the era names no systems at all', () => {
    const n = coverageNotice(topo({ positioned: 0, known: 0, omittedEdges: 0, eraId: 5 }), []);
    expect(n).toBeNull(); // nothing omitted, nothing to say
  });

  it('stays silent when the map shows everything the era knows', () => {
    // Nothing omitted and no hull off-map — a notice here would be noise.
    expect(coverageNotice(topo({ positioned: 40, known: 40, omittedEdges: 0, eraId: 5 }), [])).toBeNull();
  });

  it('counts live hulls whose system is not on the map', () => {
    // X1-A is drawn; X1-GHOST is not, so the two hulls there are off-canvas
    // (the roster still lists them) and the notice has to admit it.
    const t = topo({ positioned: 1, known: 3, omittedEdges: 2, eraId: 5 }, ['X1-A']);
    const n = coverageNotice(t, [flowIn('X1-A'), flowIn('X1-GHOST'), flowIn('X1-GHOST')]);
    expect(n?.hiddenHulls).toBe(2);
    expect(n?.text).toBe('1 of 3 systems positioned (33%) · 2 hulls in unpositioned systems');
  });

  it('singularizes a lone off-map hull', () => {
    const t = topo({ positioned: 1, known: 2, omittedEdges: 1, eraId: 5 }, ['X1-A']);
    expect(coverageNotice(t, [flowIn('X1-GHOST')])?.text)
      .toBe('1 of 2 systems positioned (50%) · 1 hull in unpositioned systems');
  });

  it('speaks up for an off-map hull even when every known system is positioned', () => {
    // Coverage can be complete for the gate graph while a hull sits in a system
    // the graph never named — silence would make it look like a lost ship.
    const t = topo({ positioned: 2, known: 2, omittedEdges: 0, eraId: 5 }, ['X1-A', 'X1-B']);
    const n = coverageNotice(t, [flowIn('X1-ELSEWHERE')]);
    expect(n?.text).toBe('2 of 2 systems positioned (100%) · 1 hull in unpositioned systems');
  });

  it('ignores hulls with no nav system rather than counting them as hidden', () => {
    const t = topo({ positioned: 1, known: 1, omittedEdges: 0, eraId: 5 }, ['X1-A']);
    expect(coverageNotice(t, [flowIn(null), { shipNav: { systemSymbol: '' } } as unknown as LiveFlow]))
      .toBeNull();
  });

  it('names an unresolved era as the reason the map is empty', () => {
    const n = coverageNotice(topo({ positioned: 0, known: 1293, omittedEdges: 5173, eraId: null }), []);
    expect(n?.text).toBe('0 of 1,293 systems positioned (0%) · era unresolved');
  });

  it('returns null when the payload carries no coverage (older cache, fixture)', () => {
    expect(coverageNotice(topo(undefined), [])).toBeNull();
    expect(coverageNotice(null, [])).toBeNull();
    expect(coverageNotice(undefined, undefined)).toBeNull();
  });

  it('returns null on non-numeric coverage instead of rendering NaN', () => {
    const bad = topo({ positioned: 'x', known: null } as unknown as TopologyCoverage);
    expect(coverageNotice(bad, [])).toBeNull();
  });

  it('survives ragged flow rows', () => {
    const t = topo({ positioned: 1, known: 3, omittedEdges: 2, eraId: 5 }, ['X1-A']);
    const n = coverageNotice(t, [null, undefined, flowIn('X1-GHOST')] as unknown as LiveFlow[]);
    expect(n?.hiddenHulls).toBe(1);
  });
});
