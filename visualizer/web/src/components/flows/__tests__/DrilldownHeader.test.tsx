import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { DrilldownHeader } from '../DrilldownHeader';

const base = {
  systemSymbol: 'X1-UQ16',
  isHome: false,
  waypointCount: 12,
  laneCount: 4,
  hullCount: 2,
  loading: false,
  error: null as string | null,
  feedLost: false,
  onClose: () => {},
};

describe('DrilldownHeader', () => {
  it('renders the system symbol and a count summary', () => {
    render(<DrilldownHeader {...base} />);
    expect(screen.getByText('X1-UQ16')).toBeInTheDocument();
    expect(screen.getByText(/12 waypoints · 4 lanes · 2 hulls/)).toBeInTheDocument();
  });

  it('singularizes a single hull', () => {
    render(<DrilldownHeader {...base} hullCount={1} />);
    expect(screen.getByText(/1 hull\b/)).toBeInTheDocument();
  });

  it('shows the HOME badge only when this is the home system', () => {
    const { rerender } = render(<DrilldownHeader {...base} isHome={false} />);
    expect(screen.queryByLabelText('home system')).not.toBeInTheDocument();
    rerender(<DrilldownHeader {...base} isHome />);
    expect(screen.getByLabelText('home system')).toBeInTheDocument();
    expect(screen.getByText(/Home/)).toBeInTheDocument();
  });

  it('shows a charting message while loading and the error otherwise', () => {
    const { rerender } = render(<DrilldownHeader {...base} loading />);
    expect(screen.getByText(/charting waypoints/)).toBeInTheDocument();
    rerender(<DrilldownHeader {...base} loading={false} error="waypoints failed" />);
    expect(screen.getByText('waypoints failed')).toBeInTheDocument();
  });

  it('notes feed loss (no intent) when the live feed is down', () => {
    render(<DrilldownHeader {...base} feedLost />);
    expect(screen.getByText(/feed lost \(no intent\)/)).toBeInTheDocument();
  });

  it('fires onClose when the close button is clicked', () => {
    const onClose = vi.fn();
    render(<DrilldownHeader {...base} onClose={onClose} />);
    fireEvent.click(screen.getByText('close'));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
