import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FlowTooltip } from '../FlowTooltip';
import { useFlowStore } from '../../../store/flowStore';
import { freshnessColor } from '../freshness';
import { mockTopology, mockLanes, mockLiveFlows, mockFreshness } from '../../../mocks/mockFlows';

const NOW = Date.parse('2026-07-17T00:00:00Z');

describe('FlowTooltip', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
    useFlowStore.setState({
      topology: mockTopology,
      lanes: mockLanes('6h'),
      live: mockLiveFlows(NOW),
      freshness: mockFreshness(),
      tooltip: null,
    });
  });

  it('renders nothing when no tooltip target is set', () => {
    const { container } = render(<FlowTooltip />);
    expect(container).toBeEmptyDOMElement();
  });

  it('system card: symbol, ramp-colored visibility pct, realized credits, hull count, post status', () => {
    useFlowStore.setState({ tooltip: { kind: 'system', key: 'X1-NK36', x: 100, y: 80 } });
    const { container } = render(<FlowTooltip />);
    expect(screen.getByText(/X1-NK36 · HOME/)).toBeInTheDocument();
    const pct = screen.getByText('95%');
    expect(pct).toHaveStyle({ color: freshnessColor(95) });
    expect(screen.getByText(/301,000/)).toBeInTheDocument(); // systemActivity realized (6h window)
    expect(screen.getByText(/1 hull in-system/)).toBeInTheDocument(); // only TORWIND-3's nav sits in NK36
    expect(screen.getByText(/manned/)).toBeInTheDocument(); // scout-post status
    expect(screen.getByText(/TORWIND-9/)).toBeInTheDocument(); // scout-post hull
    // Floats offset from the pointer's client coords.
    const panel = container.firstElementChild as HTMLElement;
    expect(panel).toHaveStyle({ left: '114px', top: '94px' });
  });

  it('system card omits missing sections gracefully (unsensed system)', () => {
    useFlowStore.setState({ tooltip: { kind: 'system', key: 'X1-UU57', x: 0, y: 0 } });
    render(<FlowTooltip />);
    expect(screen.getByText('X1-UU57')).toBeInTheDocument();
    expect(screen.queryByText(/%/)).toBeNull(); // no freshness record -> no visibility line
    expect(screen.queryByText(/post/)).toBeNull(); // no scout post -> no post line
    expect(screen.getByText(/0 hulls in-system/)).toBeInTheDocument();
  });

  it('lane card: corridor, credits, trips, and top-goods lines', () => {
    useFlowStore.setState({ tooltip: { kind: 'lane', key: 'X1-NK36→X1-KA42', x: 5, y: 5 } });
    render(<FlowTooltip />);
    expect(screen.getByText('X1-NK36 → X1-KA42')).toBeInTheDocument();
    expect(screen.getByText(/312,000/)).toBeInTheDocument();
    expect(screen.getByText(/6 trips/)).toBeInTheDocument();
    expect(screen.getByText('ELECTRONICS')).toBeInTheDocument();
    expect(screen.getByText('+210k')).toBeInTheDocument();
    expect(screen.getByText('FABRICS')).toBeInTheDocument();
    expect(screen.getByText('+102k')).toBeInTheDocument();
  });

  it('lane card falls back to the corridor header when the record is unknown', () => {
    useFlowStore.setState({ tooltip: { kind: 'lane', key: 'X1-AA→X1-BB', x: 0, y: 0 } });
    render(<FlowTooltip />);
    expect(screen.getByText('X1-AA → X1-BB')).toBeInTheDocument();
    expect(screen.queryByText(/trips/)).toBeNull();
  });
});
