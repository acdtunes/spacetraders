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
    expect(screen.getByText('TORWIND-3')).toBeInTheDocument();
    expect(screen.getByText(/X1-NK36-FE8A/)).toBeInTheDocument(); // current leg from
    expect(screen.getByText(/X1-NK36-I52/)).toBeInTheDocument();  // current leg to (the gate)
    expect(screen.getByText(/X1-KA42-D39/)).toBeInTheDocument();  // remaining hop waypoint
    expect(screen.getByText(/FABRICS/)).toBeInTheDocument();       // cargo good
    expect(screen.getByText(/ADVANCED_CIRCUITRY/)).toBeInTheDocument(); // hop tranche good
    expect(screen.getByText(/250,?000/)).toBeInTheDocument();      // projected profit
  });

  it('renders realized-so-far next to the projection', () => {
    const flow = mockLiveFlows(Date.now()).flows[0];
    render(<FlowDetailPanel flow={flow} />);
    expect(screen.getByText(/Realized so far/i)).toBeTruthy();
    expect(screen.getByText(/96,000/)).toBeTruthy();
  });
});
