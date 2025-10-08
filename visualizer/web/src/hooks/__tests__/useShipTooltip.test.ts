import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useShipTooltip } from '../useShipTooltip';
import type { TaggedShip } from '../../types/spacetraders';

const buildShip = (overrides: Partial<TaggedShip> = {}): TaggedShip => ({
  symbol: 'SHIP-001',
  agentId: 'AGENT',
  agentColor: '#fff',
  registration: {
    name: 'Test Ship',
    factionSymbol: 'COSMIC',
    role: 'COMMAND',
  },
  nav: {
    systemSymbol: 'X1-TEST',
    waypointSymbol: 'X1-TEST-A1',
    status: 'DOCKED',
    flightMode: 'CRUISE',
    route: {
      origin: { symbol: 'ORIGIN', x: 0, y: 0 },
      destination: { symbol: 'DEST', x: 10, y: 0 },
      departureTime: new Date().toISOString(),
      arrival: new Date(Date.now() + 3600_000).toISOString(),
    },
  },
  fuel: { current: 50, capacity: 100, consumed: { amount: 0, timestamp: new Date().toISOString() } },
  cargo: { capacity: 100, units: 40, inventory: [] },
  frame: {} as any,
  engine: {} as any,
  reactor: {} as any,
  modules: [],
  mounts: [],
  crew: { capacity: 1, current: 1, required: 1, morale: 100, wages: 0 },
  cooldown: { remainingSeconds: 0, totalSeconds: 0 },
  state: 'OPERATIONAL',
  ...overrides,
});

describe('useShipTooltip', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns null when no active symbol provided', () => {
    const { result } = renderHook(() => useShipTooltip({ activeSymbol: null, ships: [] }));
    expect(result.current).toBeNull();
  });

  it('formats docked ships with location text', () => {
    const ship = buildShip({ nav: { ...buildShip().nav, status: 'DOCKED' } });
    const { result } = renderHook(() => useShipTooltip({ activeSymbol: ship.symbol, ships: [ship] }));
    expect(result.current?.statusText).toBe(`Docked at ${ship.nav.waypointSymbol}`);
  });

  it('computes ETA for ships in transit', () => {
    const arrival = new Date(Date.now() + 90_000).toISOString();
    const ship = buildShip({
      nav: {
        ...buildShip().nav,
        status: 'IN_TRANSIT',
        route: {
          ...buildShip().nav.route,
          arrival,
        },
      },
    });
    const { result } = renderHook(() => useShipTooltip({ activeSymbol: ship.symbol, ships: [ship] }));
    expect(result.current?.etaText).toMatch(/^00:01:/);
  });

  it('maps cargo entries and calculates percentages', () => {
    const ship = buildShip({
      cargo: {
        capacity: 10,
        units: 5,
        inventory: [
          { symbol: 'IRON_ORE', name: 'Iron Ore', units: 2 },
          { symbol: 'COPPER_ORE', name: 'Copper Ore', units: 3 },
          { symbol: 'ALUMINUM', name: 'Aluminum', units: 4 },
        ],
      },
    });
    const { result } = renderHook(() => useShipTooltip({ activeSymbol: ship.symbol, ships: [ship] }));
    expect(result.current?.cargoEntries).toHaveLength(3);
    expect(result.current?.cargoPercent).toBe(50);
  });

  it('returns cooldown seconds when present', () => {
    const ship = buildShip({ cooldown: { remainingSeconds: 12, totalSeconds: 60 } });
    const { result } = renderHook(() => useShipTooltip({ activeSymbol: ship.symbol, ships: [ship] }));
    expect(result.current?.cooldownSeconds).toBe(12);
  });
});
