import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FreshnessLegend, basisNote } from '../FreshnessLegend';

describe('basisNote', () => {
  it('names the measurement and its denominator', () => {
    expect(basisNote('observed', 13_525)).toBe('full scale = p95 scan age · 13,525 markets');
  });

  it('says a stalled rotation out loud rather than quietly saturating', () => {
    // The failure mode a self-scaling ramp could otherwise hide: at the ceiling
    // the ramp still looks healthy, so the words have to carry it.
    expect(basisNote('ceiling', 13_525)).toContain('rotation has stalled');
  });

  it('refuses to assert a scale it did not measure', () => {
    expect(basisNote('unknown', 0)).toBe('no scan data — scale unknown');
    expect(basisNote(undefined, 0)).toBe('no scan data — scale unknown');
  });
});

describe('FreshnessLegend', () => {
  it('prints the live full scale, so a 7h rotation and a 3d one never look alike', () => {
    render(<FreshnessLegend boundMinutes={420} basis="observed" marketsKnown={13_525} missedPolls={0} />);
    expect(screen.getByTestId('freshness-full-scale').textContent).toBe('7.0h');
    render(<FreshnessLegend boundMinutes={4320} basis="ceiling" marketsKnown={13_525} missedPolls={0} />);
    const all = screen.getAllByTestId('freshness-full-scale');
    expect(all[all.length - 1].textContent).toBe('3.0d');
  });

  it('shows a dash — never a number — when the server measured nothing', () => {
    render(<FreshnessLegend boundMinutes={15} basis="unknown" marketsKnown={0} missedPolls={0} />);
    expect(screen.getByTestId('freshness-full-scale').textContent).toBe('—');
  });

  it('warns when the poll is failing, because the drawn ages are then frozen', () => {
    render(<FreshnessLegend boundMinutes={420} basis="observed" marketsKnown={100} missedPolls={3} />);
    expect(screen.getByTestId('freshness-stale-poll').textContent).toContain('3 missed polls');
  });

  it('labels dark and scout as their own states, not ends of the ramp', () => {
    render(<FreshnessLegend boundMinutes={420} basis="observed" marketsKnown={100} missedPolls={0} />);
    const legend = screen.getByTestId('freshness-legend');
    expect(legend.textContent).toContain('dark');
    expect(legend.textContent).toContain('scout');
    expect(legend.textContent).toContain('just scanned');
  });
});
