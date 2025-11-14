import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useShipTooltip } from '../useShipTooltip';
import type { TaggedShip, WaypointRef, CargoItem } from '../../types/spacetraders';

const buildWaypointRef = (symbol: string, overrides: Partial<WaypointRef> = {}): WaypointRef => ({
  symbol,
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 0,
  y: 0,
  ...overrides,
});

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
      origin: buildWaypointRef('ORIGIN'),
      destination: buildWaypointRef('DEST', { x: 10 }),
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
  cooldown: { shipSymbol: 'SHIP-001', remainingSeconds: 0, totalSeconds: 0 },
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
    const { result } = renderHook(() =>
      useShipTooltip({ activeSymbol: ship.symbol, ships: [ship], now: Date.now() })
    );
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
    const { result } = renderHook(() =>
      useShipTooltip({ activeSymbol: ship.symbol, ships: [ship], now: Date.now() })
    );
    expect(result.current?.etaText).toMatch(/^00:01:/);
  });

  it('maps cargo entries and calculates percentages', () => {
    const inventory: CargoItem[] = [
      { symbol: 'IRON_ORE', name: 'Iron Ore', description: 'Ore', units: 2 },
      { symbol: 'COPPER_ORE', name: 'Copper Ore', description: 'Ore', units: 3 },
      { symbol: 'ALUMINUM', name: 'Aluminum', description: 'Metal', units: 4 },
    ];
    const ship = buildShip({
      cargo: {
        capacity: 10,
        units: 5,
        inventory,
      },
    });
    const { result } = renderHook(() =>
      useShipTooltip({ activeSymbol: ship.symbol, ships: [ship], now: Date.now() })
    );
    expect(result.current?.cargoEntries).toHaveLength(3);
    expect(result.current?.cargoPercent).toBe(50);
  });

  it('returns cooldown seconds when present', () => {
    const ship = buildShip({ cooldown: { shipSymbol: 'SHIP-001', remainingSeconds: 12, totalSeconds: 60 } });
    const { result } = renderHook(() =>
      useShipTooltip({ activeSymbol: ship.symbol, ships: [ship], now: Date.now() })
    );
    expect(result.current?.cooldownSeconds).toBe(12);
  });

  it('updates ETA as the reference time advances', () => {
    const baseShip = buildShip();
    const ship = buildShip({
      nav: {
        ...baseShip.nav,
        status: 'IN_TRANSIT',
        route: {
          ...baseShip.nav.route,
          arrival: new Date(Date.now() + 60_000).toISOString(),
        },
      },
    });

    const { result, rerender } = renderHook(
      ({ now }) => useShipTooltip({ activeSymbol: ship.symbol, ships: [ship], now }),
      { initialProps: { now: 0 } }
    );

    expect(result.current?.etaText).toBe('00:01:00');

    rerender({ now: 20_000 });
    expect(result.current?.etaText).toBe('00:00:40');

    rerender({ now: 60_000 });
    expect(result.current?.etaText).toBe('00:00:00');
  });
});
