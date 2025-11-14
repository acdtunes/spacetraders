import { describe, it, expect } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useWaypointTooltipAnchor } from '../useWaypointTooltipAnchor';
import type { Waypoint as WaypointType } from '../../types/spacetraders';

const waypoint: WaypointType = {
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 10,
  y: 5,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
};

const getWaypointPosition = () => ({ x: waypoint.x, y: waypoint.y });

const render = (selectedObject: Parameters<typeof useWaypointTooltipAnchor>[0]['selectedObject']) =>
  renderHook(() =>
    useWaypointTooltipAnchor({
      selectedObject,
      waypoints: new Map([[waypoint.symbol, waypoint]]),
      getWaypointPosition,
    })
  );

describe('useWaypointTooltipAnchor', () => {
  it('returns null when no waypoint selected', () => {
    const { result } = render(null);
    expect(result.current.anchor).toBeNull();
  });

  it('anchors tooltip when waypoint selected', () => {
    const { result } = render({ type: 'waypoint', symbol: waypoint.symbol });
    expect(result.current.anchor).toEqual({ symbol: waypoint.symbol, worldX: waypoint.x, worldY: waypoint.y });
  });

  it('can manually show and clear anchor', () => {
    const { result } = render(null);
    act(() => result.current.showForWaypoint(waypoint));
    expect(result.current.anchor).not.toBeNull();

    act(() => result.current.clearAnchor());
    expect(result.current.anchor).toBeNull();
  });
});
