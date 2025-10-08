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
const waypointMining = 'X1-MOCK-MINE1';
const waypointFuelDepot = 'X1-MOCK-FUEL';

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
        { symbol: waypointMining, type: 'ASTEROID_FIELD', systemSymbol, x: 15, y: 35 },
        { symbol: waypointFuelDepot, type: 'FUEL_STATION', systemSymbol, x: -40, y: -20 },
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
    {
      symbol: 'X1-MOCK-MOON2',
      type: 'MOON',
      systemSymbol,
      x: -12,
      y: 18,
      orbitals: [],
      traits: [{ symbol: 'FROZEN', name: 'Frozen Surface', description: 'Extremely cold conditions' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointMining,
      type: 'ASTEROID_FIELD',
      systemSymbol,
      x: 15,
      y: 35,
      orbitals: [],
      traits: [{ symbol: 'ORE_RICH', name: 'Dense Asteroid Field', description: 'Great for mining drones' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointFuelDepot,
      type: 'FUEL_STATION',
      systemSymbol,
      x: -40,
      y: -20,
      orbitals: [],
      traits: [{ symbol: 'FUEL_STATION', name: 'Remote Fuel Depot', description: 'Refuel point' }],
      isUnderConstruction: false,
      hasMarketplace: true,
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
        waypointSymbol: waypointMining,
        route: baseRoute(waypointMining, waypointA),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
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
    {
      symbol: 'SHIP-4',
      registration: {
        name: 'Miner Drone',
        factionSymbol: 'MOCK',
        role: 'MINING_DRONE',
      },
      nav: {
        systemSymbol,
        waypointSymbol: waypointMining,
        route: baseRoute(waypointMining, waypointA),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: {},
      cargo: {
        capacity: 20,
        units: 0,
        inventory: [{ symbol: 'ORE', name: 'Raw Ore', description: '', units: 0 }],
      },
      fuel: {
        current: 20,
        capacity: 40,
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
      if (ship.registration.role === 'EXPLORER' || ship.registration.role === 'MINING_DRONE') {
        return;
      }
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

    const explorer = ships.find((ship) => ship.registration.role === 'EXPLORER');
    if (explorer) {
      explorer.nav.status = 'IN_ORBIT';
      explorer.nav.waypointSymbol = waypointMining;
      explorer.nav.flightMode = 'DRIFT';
      explorer.cargo.units = Math.min(explorer.cargo.capacity, explorer.cargo.units + 4);
      if (explorer.cargo.inventory.length > 0) {
        explorer.cargo.inventory[0].units = explorer.cargo.units;
      } else {
        explorer.cargo.inventory = [{ symbol: 'ICE', name: 'Ice Water', description: '', units: explorer.cargo.units }];
      }
    }

    const drone = ships.find((ship) => ship.registration.role === 'MINING_DRONE');
    if (drone) {
      drone.nav.status = 'IN_ORBIT';
      drone.nav.waypointSymbol = waypointMining;
      drone.nav.flightMode = 'MINING' as FlightMode;
      drone.cargo.units = Math.min(drone.cargo.capacity, drone.cargo.units + 3);
      if (drone.cargo.inventory.length > 0) {
        drone.cargo.inventory[0].units = drone.cargo.units;
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

    const explorer = ships.find((ship) => ship.registration.role === 'EXPLORER');
    if (explorer) {
      explorer.nav.status = 'IN_ORBIT';
      explorer.nav.waypointSymbol = waypointMining;
      explorer.nav.flightMode = 'DRIFT';
      explorer.fuel.current = Math.max(0, explorer.fuel.current - 5);
    }

    const drone = ships.find((ship) => ship.registration.role === 'MINING_DRONE');
    if (drone) {
      drone.nav.status = 'DOCKED';
      drone.nav.waypointSymbol = waypointFuelDepot;
      drone.nav.flightMode = 'DRIFT';
      drone.fuel.current = Math.min(drone.fuel.capacity, drone.fuel.current + 5);
      if (drone.cargo.inventory.length > 0) {
        drone.cargo.inventory[0].units = Math.max(0, drone.cargo.inventory[0].units - 5);
      }
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
