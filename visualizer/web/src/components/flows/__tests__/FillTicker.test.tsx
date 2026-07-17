import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FillTicker } from '../FillTicker';
import { useFlowStore } from '../../../store/flowStore';
import { mockFills } from '../../../mocks/mockFlows';
import { NOIR } from '../../../theme/noir';

const NOW = Date.parse('2026-07-17T00:00:00Z');

describe('FillTicker', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
    useFlowStore.setState({ fills: mockFills(NOW) });
  });

  it('renders newest-first fill lines with verbs, signed compact credits and waypoint', () => {
    const { container } = render(<FillTicker />);
    const rows = [...container.querySelectorAll('[data-fill-id]')];
    expect(rows.map((r) => r.getAttribute('data-fill-id'))).toEqual(['t-1', 't-2', 'a-3', 't-4', 't-5', 't-6']);
    expect(screen.getByText('TORWIND-3 sold 60 ELECTRONICS +132k @ X1-KA42-D39')).toBeInTheDocument();
    expect(screen.getByText('TORWIND-7 bought 40 MACHINERY -72k @ X1-UU57-E21Z')).toBeInTheDocument();
  });

  it('styles sells good and buys warn, fading with age rank', () => {
    render(<FillTicker />);
    const newest = screen.getByText(/sold 60 ELECTRONICS/);
    expect(newest).toHaveStyle({ color: NOIR.good, opacity: '1' });
    const second = screen.getByText(/bought 40 MACHINERY/);
    expect(second).toHaveStyle({ color: NOIR.warn, opacity: '0.85' });
  });

  it('caps at 6 rows and renders nothing when there are no fills', () => {
    const { container } = render(<FillTicker />);
    expect(container.querySelectorAll('[data-fill-id]')).toHaveLength(6);
    expect(screen.queryByText(/ADVANCED_CIRCUITRY/)).toBeNull(); // 8 mock fills; oldest two dropped
    useFlowStore.setState({ fills: { fills: [], generatedAt: 'x' } });
    const { container: empty } = render(<FillTicker />);
    expect(empty).toBeEmptyDOMElement();
  });
});
