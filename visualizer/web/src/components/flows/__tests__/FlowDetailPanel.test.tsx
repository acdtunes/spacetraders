import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FlowDetailPanel } from '../FlowDetailPanel';
import { mockLiveFlows } from '../../../mocks/mockFlows';

const tour = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')).flows.find((f) => f.program === 'tour')!;

describe('FlowDetailPanel', () => {
  it('renders nothing when no flow is selected', () => {
    const { container } = render(<FlowDetailPanel flow={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders program, ship, tour id, current leg, cargo, hops+tranches and P&L', () => {
    render(<FlowDetailPanel flow={tour} />);
    expect(screen.getByText('tour')).toBeInTheDocument(); // program badge (exact — tourId also contains "tour")
    expect(screen.getByText('TORWIND-19')).toBeInTheDocument();
    expect(screen.getByText(/X1-NK36-FE8A/)).toBeInTheDocument(); // current leg from
    expect(screen.getAllByText(/X1-KA42-D39/).length).toBeGreaterThan(0);  // current leg to + hop (appears twice)
    expect(screen.getByText(/FABRICS/)).toBeInTheDocument();       // cargo good
    expect(screen.getByText(/ADVANCED_CIRCUITRY/)).toBeInTheDocument(); // hop tranche good
    expect(screen.getByText(/312,?000/)).toBeInTheDocument();      // projected profit
  });
});
