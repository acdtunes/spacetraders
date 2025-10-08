import type { Agent, Ship, System, Waypoint, Market, ShipNavStatus, FlightMode, ShipNavRoute } from '../types/spacetraders';

interface MockShip extends Ship {
  agentId: string;
  agentColor: string;
}

interface MockState {
  agents: Agent[];
  systems: System[];
  waypoints: Waypoint[];
  ships: MockShip[];
  markets: Map<string, Market>;
}

const systemSymbol = 'X1-MOCK';
const waypointA = 'X1-MOCK-A1';
const waypointB = 'X1-MOCK-B1';
const waypointC = 'X1-MOCK-C1';

const now = () => new Date().toISOString();

const baseRoute = (origin: string, destination: string): ShipNavRoute => {
  const departure = new Date();
  const arrival = new Date(departure.getTime() + 5 * 60 * 1000);
  return {
    origin: {
      symbol: origin,
      type: 'PLANET',
      systemSymbol,
      x: 0,
      y: 0,
    },
    destination: {
      symbol: destination,
      type: 'PLANET',
      systemSymbol,
      x: 20,
      y: 20,
    },
    departureTime: departure.toISOString(),
    arrival: arrival.toISOString(),
  };
};

export const mockState: MockState = {
  agents: [
    {
      id: 'AGENT-1',
      symbol: 'ALPHA',
      color: '#60a5fa',
      visible: true,
      createdAt: now(),
      credits: 12000,
    },
    {
      id: 'AGENT-2',
      symbol: 'BETA',
      color: '#f472b6',
      visible: true,
      createdAt: now(),
      credits: 9000,
    },
  ],
  systems: [
    {
      symbol: systemSymbol,
      sectorSymbol: 'MOCK-SECTOR',
      type: 'YELLOW_STAR',
      x: 0,
      y: 0,
      waypoints: [
        { symbol: waypointA, type: 'PLANET', systemSymbol, x: 0, y: 0 },
        { symbol: waypointB, type: 'PLANET', systemSymbol, x: 40, y: -10 },
        { symbol: waypointC, type: 'FUEL_STATION', systemSymbol, x: -25, y: 30 },
      ],
      factions: [],
    },
  ],
  waypoints: [
    {
      symbol: waypointA,
      type: 'PLANET',
      systemSymbol,
      x: 0,
      y: 0,
      orbitals: [],
      traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'General goods' }],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypointB,
      type: 'PLANET',
      systemSymbol,
      x: 40,
      y: -10,
      orbitals: [],
      traits: [{ symbol: 'ORE_RICH', name: 'Rich in Ore', description: 'High yield mining' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointC,
      type: 'FUEL_STATION',
      systemSymbol,
      x: -25,
      y: 30,
      orbitals: [],
      traits: [{ symbol: 'FUEL_STATION', name: 'Fuel Station', description: 'Refuel here' }],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: 'X1-MOCK-MOON1',
      type: 'MOON',
      systemSymbol,
      x: 8,
      y: 6,
      orbitals: [],
      traits: [{ symbol: 'ICE_RICH', name: 'Ice Deposits', description: 'Frozen resources' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
  ],
  ships: [
    {
      symbol: 'SHIP-1',
      registration: {
        name: 'Explorer One',
        factionSymbol: 'MOCK',
        role: 'EXPLORER',
      },
      nav: {
        systemSymbol,
        waypointSymbol: waypointA,
        route: baseRoute(waypointA, waypointB),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: {},
      cargo: {
        capacity: 40,
        units: 10,
        inventory: [{ symbol: 'ICE', name: 'Ice Water', description: '', units: 10 }],
      },
      fuel: {
        current: 60,
        capacity: 100,
      },
      cooldown: {
        shipSymbol: 'SHIP-1',
        totalSeconds: 120,
        remainingSeconds: 45,
      },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'SHIP-2',
      registration: {
        name: 'Miner Bee',
        factionSymbol: 'MOCK',
        role: 'EXCAVATOR',
      },
      nav: {
        systemSymbol,
        waypointSymbol: waypointB,
        route: baseRoute(waypointA, waypointB),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: {},
      cargo: {
        capacity: 80,
        units: 40,
        inventory: [{ symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 40 }],
      },
      fuel: {
        current: 30,
        capacity: 100,
      },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'SHIP-3',
      registration: {
        name: 'Hauler Prime',
        factionSymbol: 'MOCK',
        role: 'HAULER',
      },
      nav: {
        systemSymbol,
        waypointSymbol: waypointC,
        route: baseRoute(waypointC, waypointA),
        status: 'IN_TRANSIT',
        flightMode: 'BURN',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: {},
      cargo: {
        capacity: 120,
        units: 80,
        inventory: [{ symbol: 'FUEL', name: 'Fuel Cells', description: '', units: 80 }],
      },
      fuel: {
        current: 90,
        capacity: 120,
      },
      agentId: 'AGENT-2',
      agentColor: '#f472b6',
    },
  ],
  markets: new Map<string, Market>([
    [
      `${systemSymbol}:${waypointA}`,
      {
        symbol: waypointA,
        exports: [{ symbol: 'FOOD', tradeVolume: 30, supply: 'HIGH', purchasePrice: 35, sellPrice: 45 }],
        imports: [{ symbol: 'IRON_ORE', tradeVolume: 50, supply: 'LIMITED', purchasePrice: 120, sellPrice: 150 }],
        exchange: [],
      },
    ],
    [
      `${systemSymbol}:${waypointC}`,
      {
        symbol: waypointC,
        exports: [{ symbol: 'FUEL', tradeVolume: 80, supply: 'ABUNDANT', purchasePrice: 50, sellPrice: 65 }],
        imports: [{ symbol: 'ICE', tradeVolume: 15, supply: 'SCARCE', purchasePrice: 95, sellPrice: 110 }],
        exchange: [],
      },
    ],
  ]),
};

let scenarioRunning = false;
let phaseIndex = 0;

const cycleStatuses: Array<(ships: MockShip[]) => void> = [
  (ships) => {
    // Phase 1: ships dock to refuel
    ships.forEach((ship) => {
      ship.nav.status = 'DOCKED';
      ship.nav.waypointSymbol = ship.registration.role === 'HAULER' ? waypointC : waypointA;
      ship.nav.flightMode = 'DRIFT';
      ship.fuel.current = Math.min(ship.fuel.capacity, ship.fuel.current + 10);
    });
  },
  (ships) => {
    // Phase 2: miner orbits target
    const miner = ships.find((ship) => ship.registration.role === 'EXCAVATOR');
    if (miner) {
      miner.nav.status = 'IN_ORBIT';
      miner.nav.waypointSymbol = waypointB;
      miner.nav.flightMode = 'DRIFT';
      miner.cargo.units = Math.min(miner.cargo.capacity, miner.cargo.units + 5);
      if (miner.cargo.inventory.length > 0) {
        miner.cargo.inventory[0].units = miner.cargo.units;
      }
    }
  },
  (ships) => {
    // Phase 3: hauler departs in transit
    const hauler = ships.find((ship) => ship.registration.role === 'HAULER');
    if (hauler) {
      hauler.nav.status = 'IN_TRANSIT';
      hauler.nav.route = baseRoute(waypointC, waypointA);
      hauler.nav.flightMode = 'CRUISE';
      hauler.fuel.current = Math.max(0, hauler.fuel.current - 15);
    }
  },
];

const advancePhase = () => {
  phaseIndex = (phaseIndex + 1) % cycleStatuses.length;
  cycleStatuses[phaseIndex](mockState.ships);
};

export const startMockScenarioIfNeeded = () => {
  if (scenarioRunning) return;
  scenarioRunning = true;
  setInterval(() => {
    advancePhase();
  }, 8000);
};

export const advanceShipScenario = () => {
  advancePhase();
};

