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
const waypointCommandPatrol = 'X1-MOCK-P1';
const waypointMoon1 = 'X1-MOCK-MOON1';
const waypointMoon2 = 'X1-MOCK-MOON2';

const waypointPositions: Record<string, { x: number; y: number }> = {
  [waypointA]: { x: 200, y: 0 },
  [waypointB]: { x: -100, y: 173 },
  [waypointC]: { x: -100, y: -173 },
  [waypointMining]: { x: 0, y: 200 },
  [waypointFuelDepot]: { x: 0, y: -200 },
  [waypointCommandPatrol]: { x: 141, y: -141 },
  [waypointMoon1]: { x: 120, y: 80 },
  [waypointMoon2]: { x: 60, y: -160 },
};

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
        { symbol: waypointA, type: 'PLANET', systemSymbol, ...waypointPositions[waypointA] },
        { symbol: waypointB, type: 'PLANET', systemSymbol, ...waypointPositions[waypointB] },
        { symbol: waypointC, type: 'FUEL_STATION', systemSymbol, ...waypointPositions[waypointC] },
        { symbol: waypointMining, type: 'ASTEROID_FIELD', systemSymbol, ...waypointPositions[waypointMining] },
        { symbol: waypointCommandPatrol, type: 'PLANET', systemSymbol, ...waypointPositions[waypointCommandPatrol] },
        { symbol: waypointFuelDepot, type: 'FUEL_STATION', systemSymbol, ...waypointPositions[waypointFuelDepot] },
      ],
      factions: [],
    },
  ],
  waypoints: [
    {
      symbol: waypointA,
      type: 'PLANET',
      systemSymbol,
      ...waypointPositions[waypointA],
      orbitals: [],
      traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'General goods' }],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypointB,
      type: 'PLANET',
      systemSymbol,
      ...waypointPositions[waypointB],
      orbitals: [],
      traits: [{ symbol: 'ORE_RICH', name: 'Rich in Ore', description: 'High yield mining' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointC,
      type: 'FUEL_STATION',
      systemSymbol,
      ...waypointPositions[waypointC],
      orbitals: [],
      traits: [{ symbol: 'FUEL_STATION', name: 'Fuel Station', description: 'Refuel here' }],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypointMoon1,
      type: 'MOON',
      systemSymbol,
      ...waypointPositions[waypointMoon1],
      orbitals: [],
      traits: [{ symbol: 'ICE_RICH', name: 'Ice Deposits', description: 'Frozen resources' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointMoon2,
      type: 'MOON',
      systemSymbol,
      ...waypointPositions[waypointMoon2],
      orbitals: [],
      traits: [{ symbol: 'FROZEN', name: 'Frozen Surface', description: 'Extremely cold conditions' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointMining,
      type: 'ASTEROID_FIELD',
      systemSymbol,
      ...waypointPositions[waypointMining],
      orbitals: [],
      traits: [{ symbol: 'ORE_RICH', name: 'Dense Asteroid Field', description: 'Great for mining drones' }],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypointFuelDepot,
      type: 'FUEL_STATION',
      systemSymbol,
      ...waypointPositions[waypointFuelDepot],
      orbitals: [],
      traits: [{ symbol: 'FUEL_STATION', name: 'Remote Fuel Depot', description: 'Refuel point' }],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypointCommandPatrol,
      type: 'PLANET',
      systemSymbol,
      x: 25,
      y: 18,
      orbitals: [],
      traits: [{ symbol: 'OBSERVATORY', name: 'Observation Outpost', description: 'Patrol hub' }],
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
    {
      symbol: 'SHIP-5',
      registration: {
        name: 'Command Frigate',
        factionSymbol: 'MOCK',
        role: 'COMMAND',
      },
      nav: {
        systemSymbol,
        waypointSymbol: waypointA,
        route: baseRoute(waypointA, waypointB),
        status: 'IN_TRANSIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: {},
      cargo: {
        capacity: 30,
        units: 0,
        inventory: [],
      },
      fuel: {
        current: 80,
        capacity: 120,
      },
      agentId: 'AGENT-2',
      agentColor: '#fbbf24',
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

// New phased scenario runner replaces the old cycle
type Phase = { name: string; duration: number; apply: () => void };

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

const findShipByRole = (role: string) => mockState.ships.find((ship) => ship.registration.role === role);

const ensureCargoEntry = (ship: MockShip, symbol: string, name: string) => {
  if (!ship.cargo.inventory.length) {
    ship.cargo.inventory.push({ symbol, name, description: '', units: ship.cargo.units });
  } else {
    ship.cargo.inventory[0].symbol = symbol;
    ship.cargo.inventory[0].name = name;
    ship.cargo.inventory[0].units = ship.cargo.units;
  }
};

// Explorer phases
const setExplorerMining = () => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'IN_ORBIT';
  explorer.nav.waypointSymbol = waypointMining;
  explorer.nav.route = baseRoute(waypointMining, waypointA);
  explorer.nav.flightMode = 'DRIFT';
  explorer.fuel.current = clamp(explorer.fuel.current - 2, 0, explorer.fuel.capacity);
  explorer.cargo.units = clamp(explorer.cargo.units + 5, 0, explorer.cargo.capacity);
  ensureCargoEntry(explorer, 'ICE', 'Ice Water');
};

const setExplorerTransit = (origin: string, destination: string, fuelCost: number, mode: FlightMode) => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'IN_TRANSIT';
  explorer.nav.waypointSymbol = destination;
  explorer.nav.route = baseRoute(origin, destination);
  explorer.nav.flightMode = mode;
  explorer.fuel.current = clamp(explorer.fuel.current - fuelCost, 0, explorer.fuel.capacity);
};

const setExplorerDocked = (waypointSymbol: string) => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'DOCKED';
  explorer.nav.waypointSymbol = waypointSymbol;
  explorer.nav.flightMode = 'DRIFT';
  explorer.fuel.current = clamp(explorer.fuel.current + 6, 0, explorer.fuel.capacity);
};

const setExplorerOrbit = (waypointSymbol: string) => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'IN_ORBIT';
  explorer.nav.waypointSymbol = waypointSymbol;
  explorer.nav.flightMode = 'DRIFT';
};

// Supporting ships
const setMinerOrbit = () => {
  const miner = findShipByRole('EXCAVATOR');
  if (!miner) return;
  miner.nav.status = 'IN_ORBIT';
  miner.nav.waypointSymbol = waypointB;
  miner.nav.flightMode = 'DRIFT';
  miner.cargo.units = clamp(miner.cargo.units + 4, 0, miner.cargo.capacity);
  if (miner.cargo.inventory.length) {
    miner.cargo.inventory[0].units = miner.cargo.units;
  }
};

const setHaulerDocked = () => {
  const hauler = findShipByRole('HAULER');
  if (!hauler) return;
  hauler.nav.status = 'DOCKED';
  hauler.nav.waypointSymbol = waypointC;
  hauler.nav.flightMode = 'DRIFT';
  hauler.fuel.current = clamp(hauler.fuel.current + 8, 0, hauler.fuel.capacity);
};

const setHaulerTransit = () => {
  const hauler = findShipByRole('HAULER');
  if (!hauler) return;
  hauler.nav.status = 'IN_TRANSIT';
  hauler.nav.waypointSymbol = waypointA;
  hauler.nav.route = baseRoute(waypointC, waypointA);
  hauler.nav.flightMode = 'CRUISE';
  hauler.fuel.current = clamp(hauler.fuel.current - 12, 0, hauler.fuel.capacity);
};

const setDroneMining = () => {
  const drone = findShipByRole('MINING_DRONE');
  if (!drone) return;
  drone.nav.status = 'IN_ORBIT';
  drone.nav.waypointSymbol = waypointMining;
  drone.nav.flightMode = 'DRIFT';
  drone.cargo.units = clamp(drone.cargo.units + 3, 0, drone.cargo.capacity);
  ensureCargoEntry(drone, 'ORE', 'Raw Ore');
};

const setDroneDockedRefuel = () => {
  const drone = findShipByRole('MINING_DRONE');
  if (!drone) return;
  drone.nav.status = 'DOCKED';
  drone.nav.waypointSymbol = waypointFuelDepot;
  drone.nav.flightMode = 'DRIFT';
  drone.fuel.current = clamp(drone.fuel.current + 6, 0, drone.fuel.capacity);
  if (drone.cargo.inventory.length) {
    drone.cargo.inventory[0].units = Math.max(0, drone.cargo.inventory[0].units - 4);
    drone.cargo.units = drone.cargo.inventory[0].units;
  }
};

const setDroneTransitToMine = () => {
  const drone = findShipByRole('MINING_DRONE');
  if (!drone) return;
  drone.nav.status = 'IN_TRANSIT';
  drone.nav.waypointSymbol = waypointMining;
  drone.nav.route = baseRoute(waypointFuelDepot, waypointMining);
  drone.nav.flightMode = 'CRUISE';
  drone.fuel.current = clamp(drone.fuel.current - 5, 0, drone.fuel.capacity);
};

const setFrigateDocked = (waypointSymbol: string) => {
  const frigate = findShipByRole('COMMAND');
  if (!frigate) return;
  frigate.nav.status = 'DOCKED';
  frigate.nav.waypointSymbol = waypointSymbol;
  frigate.nav.flightMode = 'DRIFT';
  frigate.fuel.current = clamp(frigate.fuel.current + 4, 0, frigate.fuel.capacity);
};

const setFrigateTransit = (origin: string, destination: string) => {
  const frigate = findShipByRole('COMMAND');
  if (!frigate) return;
  frigate.nav.status = 'IN_TRANSIT';
  frigate.nav.waypointSymbol = destination;
  frigate.nav.route = baseRoute(origin, destination);
  frigate.nav.flightMode = 'CRUISE';
  frigate.fuel.current = clamp(frigate.fuel.current - 6, 0, frigate.fuel.capacity);
};

const phases: Phase[] = [
  {
    name: 'explorer-mining',
    duration: 10000,
    apply: () => {
      setExplorerMining();
      setMinerOrbit();
      setHaulerDocked();
      setDroneMining();
      setFrigateDocked(waypointCommandPatrol);
    },
  },
  {
    name: 'explorer-transit-a1',
    duration: 6000,
    apply: () => {
      setExplorerTransit(waypointMining, waypointA, 8, 'CRUISE');
      setMinerOrbit();
      setDroneMining();
      setHaulerDocked();
      setFrigateTransit(waypointCommandPatrol, waypointB);
    },
  },
  {
    name: 'explorer-docked-a1',
    duration: 10000,
    apply: () => {
      setExplorerDocked(waypointA);
      setMinerOrbit();
      setDroneMining();
      setHaulerDocked();
      setFrigateDocked(waypointB);
    },
  },
  {
    name: 'explorer-orbit-a1',
    duration: 10000,
    apply: () => {
      setExplorerOrbit(waypointA);
      setMinerOrbit();
      setDroneDockedRefuel();
      setHaulerDocked();
      setFrigateTransit(waypointB, waypointA);
    },
  },
  {
    name: 'explorer-return-to-mine',
    duration: 6000,
    apply: () => {
      setExplorerTransit(waypointA, waypointMining, 10, 'BURN');
      setMinerOrbit();
      setHaulerTransit();
      setDroneTransitToMine();
      setFrigateDocked(waypointA);
    },
  },
];

let scenarioRunning = false;
let phaseIndex = 0;
let phaseTimeout: ReturnType<typeof setTimeout> | null = null;

const runPhase = () => {
  const phase = phases[phaseIndex];
  phase.apply();
  phaseTimeout = setTimeout(() => {
    phaseIndex = (phaseIndex + 1) % phases.length;
    runPhase();
  }, phase.duration);
};

export const startMockScenarioIfNeeded = () => {
  if (scenarioRunning) return;
  scenarioRunning = true;
  runPhase();
};

export const advanceShipScenario = () => {
  if (phaseTimeout) {
    clearTimeout(phaseTimeout);
    phaseTimeout = null;
  }
  if (!scenarioRunning) {
    scenarioRunning = true;
  }
  phaseIndex = (phaseIndex + 1) % phases.length;
  runPhase();
};
