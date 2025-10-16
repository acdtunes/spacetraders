import { describe, it, expect } from 'vitest';
import { useSpaceMapOverlays } from '../useSpaceMapOverlays';
import type { TaggedShip, Waypoint as WaypointType, Market, WaypointRef } from '../../types/spacetraders';
import { renderHook } from '@testing-library/react';

const buildWaypoint = (overrides: Partial<WaypointType> = {}): WaypointType => ({
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 10,
  y: 5,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
  ...overrides,
});

const buildWaypointRef = (symbol: string, overrides: Partial<WaypointRef> = {}): WaypointRef => ({
  symbol,
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 0,
  y: 0,
  ...overrides,
});

const buildShip = (overrides: Partial<TaggedShip> = {}): TaggedShip => ({
  symbol: 'SHIP-1',
  agentId: 'AGENT',
  agentColor: '#fff',
  registration: {
    name: 'Ship',
    factionSymbol: 'COSMIC',
    role: 'COMMAND',
  },
  nav: {
    systemSymbol: 'X1-TEST',
    waypointSymbol: 'X1-TEST-A1',
    status: 'IN_TRANSIT',
    flightMode: 'CRUISE',
    route: {
      origin: buildWaypointRef('ORIGIN'),
      destination: buildWaypointRef('X1-TEST-A1', { x: 10, y: 5 }),
      departureTime: new Date().toISOString(),
      arrival: new Date(Date.now() + 60000).toISOString(),
    },
  },
  cargo: { capacity: 10, units: 0, inventory: [] },
  fuel: { capacity: 100, current: 50, consumed: { amount: 0, timestamp: new Date().toISOString() } },
  frame: {} as any,
  engine: {} as any,
  reactor: {} as any,
  modules: [],
  mounts: [],
  crew: { capacity: 1, current: 1, required: 1, morale: 100, wages: 0 },
  cooldown: { shipSymbol: 'SHIP-1', remainingSeconds: 0, totalSeconds: 0 },
  ...overrides,
});

const buildMarket = (): Market => ({
  symbol: 'X1-TEST-A1',
  exports: [],
  imports: [],
  exchange: [],
});

const defaultParams = {
  hoveredShip: null,
  selectedObject: null,
  selectedShip: null,
  selectedWaypoint: null,
  ships: [buildShip()],
  waypoints: new Map([[ 'X1-TEST-A1', buildWaypoint() ]]),
  markets: new Map([[ 'X1-TEST-A1', buildMarket() ]]),
  marketIntel: new Map(),
  projectToScreen: () => ({ x: 100, y: 200 }),
  getWaypointPosition: () => ({ x: 10, y: 5 }),
  getShipRenderPosition: () => ({ x: 10, y: 5 }),
  frameTimestamp: Date.now(),
  waypointTooltipAnchor: null,
  shipTooltipOffset: { x: 12, y: 12 },
  waypointTooltipOffset: { x: 12, y: 12 },
  getWaypointOpportunities: () => [],
  formatOpportunity: (opportunity: any) => String(opportunity),
};

describe('useSpaceMapOverlays', () => {
  it('returns null tooltip when nothing selected or hovered', () => {
    const { result } = renderHook(() => useSpaceMapOverlays(defaultParams));
    expect(result.current.shipTooltip).toBeNull();
    expect(result.current.selectionOverlays).toEqual([]);
  });

  it('returns ship tooltip when hovered', () => {
    const params = {
      ...defaultParams,
      hoveredShip: 'SHIP-1',
    };
    const { result } = renderHook(() => useSpaceMapOverlays(params));
    expect(result.current.shipTooltip?.symbol).toBe('SHIP-1');
    expect(result.current.shipTooltipPosition).toEqual({ left: 88, top: 188 });
  });

  it('provides selection overlay for waypoint object', () => {
    const params = {
      ...defaultParams,
      selectedObject: { type: 'waypoint', symbol: 'X1-TEST-A1', x: 0, y: 0 },
      selectedWaypoint: buildWaypoint(),
    } as const;
    const { result } = renderHook(() => useSpaceMapOverlays(params));
    expect(result.current.selectionOverlays[0]).toMatchObject({ type: 'waypoint', size: 18 });
  });

  it('returns waypoint tooltip with market data', () => {
    const markets = new Map<string, Market>([
      [
        'X1-TEST-A1',
        {
          symbol: 'X1-TEST-A1',
          exports: [{ symbol: 'IRON', tradeVolume: 10, supply: 'HIGH', purchasePrice: 50, sellPrice: 0 }],
          imports: [],
          exchange: [],
        },
      ],
      [
        'X1-TEST-B1',
        {
          symbol: 'X1-TEST-B1',
          exports: [],
          imports: [{ symbol: 'IRON', tradeVolume: 10, supply: 'SCARCE', purchasePrice: 0, sellPrice: 200 }],
          exchange: [],
        },
      ],
    ]);

    const intel = new Map([
      [
        'X1-TEST-A1',
        {
          waypointSymbol: 'X1-TEST-A1',
          lastUpdated: new Date().toISOString(),
          goods: [
            {
              symbol: 'IRON',
              supply: 'HIGH',
              activity: 'ACTIVE',
              purchasePrice: 50,
              sellPrice: 200,
              tradeVolume: 100,
            },
          ],
        },
      ],
    ]);

    const waypointWithMarket = buildWaypoint({ traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: '' }] });

    const params = {
      ...defaultParams,
      waypoints: new Map([[waypointWithMarket.symbol, waypointWithMarket]]),
      markets,
      marketIntel: intel,
      waypointTooltipAnchor: { symbol: waypointWithMarket.symbol, worldX: waypointWithMarket.x, worldY: waypointWithMarket.y },
      getWaypointOpportunities: (_symbol: string, inputMarkets: Map<string, Market>) => {
        expect(inputMarkets).toBe(markets);
        return [{ good: 'IRON', profitPerUnit: 150, buyLocation: 'X1-TEST-A1', sellLocation: 'X1-TEST-B1' }];
      },
      formatOpportunity: () => 'IRON: +150 cr/unit',
    } as const;
    const { result } = renderHook(() => useSpaceMapOverlays(params));
    expect(result.current.waypointTooltip?.marketData?.opportunities).toEqual(['IRON: +150 cr/unit']);
    expect(result.current.waypointTooltip?.intel?.goods[0]).toMatchObject({ symbol: 'IRON', spread: 150 });
    expect(result.current.waypointTooltipPosition).toEqual({ left: 112, top: 188 });
  });
});
