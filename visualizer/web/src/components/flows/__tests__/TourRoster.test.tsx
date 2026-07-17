import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { TourRoster } from '../TourRoster';
import { mockLiveFlows, mockLanes } from '../../../mocks/mockFlows';

const NOW = Date.parse('2026-07-17T12:00:00Z');

describe('TourRoster', () => {
  const flows = mockLiveFlows(NOW).flows;
  const lanes = mockLanes('6h');

  it('renders one row per flow with projected and realized credits', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText('TORWIND-3')).toBeTruthy();
    expect(screen.getByText(/250,000/)).toBeTruthy();  // tour A projected
    expect(screen.getByText(/real 96,000/)).toBeTruthy();   // tour A realized (disambiguated from tour C's 96,000/hr rate)
  });

  it('shows fleet totals in the header', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText(/Σ projected/i)).toBeTruthy();
    expect(screen.getByText(/Σ realized/i)).toBeTruthy();
  });

  it('badges closed loops and relocations', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText(/loop/i)).toBeTruthy();
    expect(screen.getByText(/relocation/i)).toBeTruthy();
  });

  it('row click reports the container id', () => {
    const onRowClick = vi.fn();
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={onRowClick} />);
    fireEvent.click(screen.getByText('TORWIND-3'));
    expect(onRowClick).toHaveBeenCalledWith(flows[0].containerId);
  });
});
