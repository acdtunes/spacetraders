import { describe, it, expect } from 'vitest';
import { Waypoint } from '../waypoint';
import type { Waypoint as WaypointType } from '../../types/spacetraders';

const buildWaypoint = (overrides: Partial<WaypointType>): WaypointType => ({
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 10,
  y: 15,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
  ...overrides,
});

describe('Waypoint domain', () => {
  it('computes radius based on type hash', () => {
    const waypoint = buildWaypoint({ type: 'PLANET' });
    const radius = Waypoint.getRadius(waypoint);
    expect(radius).toBeGreaterThan(5);

    const moonRadius = Waypoint.getRadius(buildWaypoint({ type: 'MOON' }));
    expect(moonRadius).toBeLessThan(radius);
  });

  it('detects marketplace trait', () => {
    const waypoint = buildWaypoint({ traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: '' }] });
    expect(Waypoint.isMarketplace(waypoint)).toBe(true);
    expect(Waypoint.isMarketplace(buildWaypoint({ traits: [] }))).toBe(false);
  });

  it('formats display name by stripping prefix', () => {
    expect(Waypoint.getDisplayName(buildWaypoint({ symbol: 'X1-TEST-A1' }))).toBe('A1');
  });
});
