import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FeedLostChip } from '../FeedLostChip';
import { formatElapsed } from '../feedLostElapsed';

describe('formatElapsed', () => {
  it('formats mm:ss since the given timestamp', () => {
    const base = Date.parse('2026-07-11T00:00:00Z');
    expect(formatElapsed('2026-07-11T00:00:00Z', base + 125_000)).toBe('02:05');
  });
  it('returns a dash when no timestamp is known', () => {
    expect(formatElapsed(null, Date.now())).toBe('—');
  });
});

describe('FeedLostChip', () => {
  it('renders nothing while the feed is healthy', () => {
    const { container } = render(<FeedLostChip feedLost={false} lastPlanAt={null} nowMs={Date.now()} />);
    expect(container).toBeEmptyDOMElement();
  });
  it('shows FEED LOST with the elapsed-since-last-plan when the feed is down', () => {
    const base = Date.parse('2026-07-11T00:00:00Z');
    render(<FeedLostChip feedLost lastPlanAt="2026-07-11T00:00:00Z" nowMs={base + 65_000} />);
    expect(screen.getByText(/FEED LOST/)).toBeInTheDocument();
    expect(screen.getByText(/01:05/)).toBeInTheDocument();
  });
});
