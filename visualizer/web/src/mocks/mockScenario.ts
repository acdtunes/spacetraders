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

const waypointTypes: Record<string, Waypoint['type']> = {
  [waypointA]: 'PLANET',
  [waypointB]: 'PLANET',
  [waypointC]: 'FUEL_STATION',
  [waypointMining]: 'ASTEROID_FIELD',
  [waypointFuelDepot]: 'FUEL_STATION',
  [waypointCommandPatrol]: 'PLANET',
  [waypointMoon1]: 'MOON',
  [waypointMoon2]: 'MOON',
};

const FLIGHT_MODE_MULTIPLIERS: Record<FlightMode, number> = {
  DRIFT: 26,
  CRUISE: 31,
  BURN: 15,
  STEALTH: 50,
};

const DEFAULT_ENGINE_SPEED = 80;

const ENGINE_SPEED_BY_ROLE: Record<string, number> = {
  COMMAND: 110,
  EXPLORER: 90,
  EXCAVATOR: 70,
  HAULER: 85,
  MINING_DRONE: 120,
};

const now = () => new Date().toISOString();

const getWaypointPosition = (symbol: string) => waypointPositions[symbol] ?? { x: 0, y: 0 };

const getEngineSpeedForShip = (ship?: MockShip): number => {
  if (!ship) return DEFAULT_ENGINE_SPEED;
  const speedFromShip = Number(ship.engine?.speed);
  if (Number.isFinite(speedFromShip) && speedFromShip > 0) {
    return speedFromShip;
  }
  const roleSpeed = ENGINE_SPEED_BY_ROLE[ship.registration.role] ?? DEFAULT_ENGINE_SPEED;
  if (typeof ship.engine === 'object' && ship.engine !== null) {
    ship.engine.speed = roleSpeed;
  } else {
    ship.engine = { speed: roleSpeed };
  }
  return roleSpeed;
};

const computeTransitDurationMs = (
  origin: string,
  destination: string,
  mode: FlightMode,
  engineSpeed: number
) => {
  const originPos = getWaypointPosition(origin);
  const destinationPos = getWaypointPosition(destination);
  const distance = Math.hypot(destinationPos.x - originPos.x, destinationPos.y - originPos.y);
  if (distance === 0) return 0;
  const multiplier = FLIGHT_MODE_MULTIPLIERS[mode] ?? FLIGHT_MODE_MULTIPLIERS.CRUISE;
  const durationSeconds = Math.max(1, Math.round((distance * multiplier) / Math.max(engineSpeed, 1)));
  return durationSeconds * 1000;
};

const baseRoute = (
  origin: string,
  destination: string,
  mode: FlightMode = 'CRUISE',
  engineSpeed: number = DEFAULT_ENGINE_SPEED
): ShipNavRoute => {
  const departure = new Date();
  const travelDuration = computeTransitDurationMs(origin, destination, mode, engineSpeed);
  const arrival = new Date(departure.getTime() + travelDuration);
  const originPos = getWaypointPosition(origin);
  const destinationPos = getWaypointPosition(destination);
  return {
    origin: {
      symbol: origin,
      type: waypointTypes[origin] ?? 'PLANET',
      systemSymbol,
      x: originPos.x,
      y: originPos.y,
    },
    destination: {
      symbol: destination,
      type: waypointTypes[destination] ?? 'PLANET',
      systemSymbol,
      x: destinationPos.x,
      y: destinationPos.y,
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
        route: baseRoute(
          waypointMining,
          waypointA,
          'CRUISE',
          ENGINE_SPEED_BY_ROLE.EXPLORER ?? DEFAULT_ENGINE_SPEED
        ),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER ?? DEFAULT_ENGINE_SPEED },
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
        route: baseRoute(
          waypointA,
          waypointB,
          'CRUISE',
          ENGINE_SPEED_BY_ROLE.EXCAVATOR ?? DEFAULT_ENGINE_SPEED
        ),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR ?? DEFAULT_ENGINE_SPEED },
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
        route: baseRoute(
          waypointC,
          waypointC,
          'DRIFT',
          ENGINE_SPEED_BY_ROLE.HAULER ?? DEFAULT_ENGINE_SPEED
        ),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER ?? DEFAULT_ENGINE_SPEED },
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
        route: baseRoute(
          waypointMining,
          waypointA,
          'CRUISE',
          ENGINE_SPEED_BY_ROLE.MINING_DRONE ?? DEFAULT_ENGINE_SPEED
        ),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.MINING_DRONE ?? DEFAULT_ENGINE_SPEED },
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
        route: baseRoute(
          waypointA,
          waypointB,
          'CRUISE',
          ENGINE_SPEED_BY_ROLE.COMMAND ?? DEFAULT_ENGINE_SPEED
        ),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND ?? DEFAULT_ENGINE_SPEED },
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
type Phase = { name: string; duration: number; apply: () => number | void };

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

const findShipByRole = (role: string) => mockState.ships.find((ship) => ship.registration.role === role);

const setShipCooldown = (ship: MockShip, remainingSeconds: number, totalSeconds = 30) => {
  const clampedRemaining = Math.max(0, Math.floor(remainingSeconds));
  const clampedTotal = Math.max(clampedRemaining, Math.floor(totalSeconds));
  if (!ship.cooldown) {
    ship.cooldown = {
      shipSymbol: ship.symbol,
      totalSeconds: clampedTotal,
      remainingSeconds: clampedRemaining,
    };
    return;
  }
  ship.cooldown.totalSeconds = clampedTotal;
  ship.cooldown.remainingSeconds = clampedRemaining;
};

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
  const engineSpeed = getEngineSpeedForShip(explorer);
  explorer.nav.status = 'IN_ORBIT';
  explorer.nav.waypointSymbol = waypointMining;
  explorer.nav.route = baseRoute(waypointMining, waypointA, 'CRUISE', engineSpeed);
  explorer.nav.flightMode = 'DRIFT';
  explorer.fuel.current = clamp(explorer.fuel.current - 2, 0, explorer.fuel.capacity);
  explorer.cargo.units = clamp(explorer.cargo.units + 5, 0, explorer.cargo.capacity);
  ensureCargoEntry(explorer, 'ICE', 'Ice Water');
  setShipCooldown(explorer, 12, 12);
};

const setExplorerTransit = (
  origin: string,
  destination: string,
  fuelCost: number,
  mode: FlightMode
): number => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return 0;
  const engineSpeed = getEngineSpeedForShip(explorer);
  explorer.nav.status = 'IN_TRANSIT';
  explorer.nav.waypointSymbol = destination;
  explorer.nav.route = baseRoute(origin, destination, mode, engineSpeed);
  explorer.nav.flightMode = mode;
  explorer.fuel.current = clamp(explorer.fuel.current - fuelCost, 0, explorer.fuel.capacity);
  setShipCooldown(explorer, 0, explorer.cooldown?.totalSeconds ?? 0);
  return computeTransitDurationMs(origin, destination, mode, engineSpeed);
};

const setExplorerDocked = (waypointSymbol: string) => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'DOCKED';
  explorer.nav.waypointSymbol = waypointSymbol;
  explorer.nav.flightMode = 'DRIFT';
  explorer.fuel.current = clamp(explorer.fuel.current + 6, 0, explorer.fuel.capacity);
  setShipCooldown(explorer, 0, explorer.cooldown?.totalSeconds ?? 0);
};

const setExplorerOrbit = (waypointSymbol: string) => {
  const explorer = findShipByRole('EXPLORER');
  if (!explorer) return;
  explorer.nav.status = 'IN_ORBIT';
  explorer.nav.waypointSymbol = waypointSymbol;
  explorer.nav.flightMode = 'DRIFT';
  setShipCooldown(explorer, 0, explorer.cooldown?.totalSeconds ?? 0);
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
  setShipCooldown(miner, 10, 10);
};

const setHaulerDocked = () => {
  const hauler = findShipByRole('HAULER');
  if (!hauler) return;
  hauler.nav.status = 'DOCKED';
  hauler.nav.waypointSymbol = waypointC;
  hauler.nav.flightMode = 'DRIFT';
  hauler.fuel.current = clamp(hauler.fuel.current + 8, 0, hauler.fuel.capacity);
};

const setHaulerOrbit = () => {
  const hauler = findShipByRole('HAULER');
  if (!hauler) return;
  hauler.nav.status = 'IN_ORBIT';
  hauler.nav.waypointSymbol = waypointC;
  hauler.nav.flightMode = 'DRIFT';
};

const setDroneMining = () => {
  const drone = findShipByRole('MINING_DRONE');
  if (!drone) return;
  drone.nav.status = 'IN_ORBIT';
  drone.nav.waypointSymbol = waypointMining;
  drone.nav.flightMode = 'DRIFT';
  drone.cargo.units = clamp(drone.cargo.units + 3, 0, drone.cargo.capacity);
  ensureCargoEntry(drone, 'ORE', 'Raw Ore');
  setShipCooldown(drone, 10, 10);
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
  setShipCooldown(drone, 0, drone.cooldown?.totalSeconds ?? 0);
};

const setDroneTransitToMine = (): number => {
  const drone = findShipByRole('MINING_DRONE');
  if (!drone) return 0;
  const engineSpeed = getEngineSpeedForShip(drone);
  drone.nav.status = 'IN_TRANSIT';
  drone.nav.waypointSymbol = waypointMining;
  drone.nav.route = baseRoute(waypointFuelDepot, waypointMining, 'CRUISE', engineSpeed);
  drone.nav.flightMode = 'CRUISE';
  drone.fuel.current = clamp(drone.fuel.current - 5, 0, drone.fuel.capacity);
  setShipCooldown(drone, 0, drone.cooldown?.totalSeconds ?? 0);
  return computeTransitDurationMs(waypointFuelDepot, waypointMining, 'CRUISE', engineSpeed);
};

const setFrigateDocked = (waypointSymbol: string) => {
  const frigate = findShipByRole('COMMAND');
  if (!frigate) return;
  frigate.nav.status = 'DOCKED';
  frigate.nav.waypointSymbol = waypointSymbol;
  frigate.nav.flightMode = 'DRIFT';
  frigate.fuel.current = clamp(frigate.fuel.current + 4, 0, frigate.fuel.capacity);
};

const setFrigateTransit = (origin: string, destination: string): number => {
  const frigate = findShipByRole('COMMAND');
  if (!frigate) return 0;
  const engineSpeed = getEngineSpeedForShip(frigate);
  frigate.nav.status = 'IN_TRANSIT';
  frigate.nav.waypointSymbol = destination;
  frigate.nav.route = baseRoute(origin, destination, 'CRUISE', engineSpeed);
  frigate.nav.flightMode = 'CRUISE';
  frigate.fuel.current = clamp(frigate.fuel.current - 6, 0, frigate.fuel.capacity);
  return computeTransitDurationMs(origin, destination, 'CRUISE', engineSpeed);
};

const setFrigateOrbit = (waypointSymbol: string) => {
  const frigate = findShipByRole('COMMAND');
  if (!frigate) return;
  frigate.nav.status = 'IN_ORBIT';
  frigate.nav.waypointSymbol = waypointSymbol;
  frigate.nav.flightMode = 'DRIFT';
};

const DEFAULT_PHASE_DURATION = 10000;

const HAULER_LOOP_INTERVAL_MS = 10000;
let haulerLoopState: 'ORBIT' | 'DOCK' = 'ORBIT';
let haulerLoopTimeout: ReturnType<typeof setTimeout> | null = null;

const runHaulerLoopStep = () => {
  if (haulerLoopState === 'ORBIT') {
    setHaulerOrbit();
    haulerLoopState = 'DOCK';
  } else {
    setHaulerDocked();
    haulerLoopState = 'ORBIT';
  }
  haulerLoopTimeout = setTimeout(runHaulerLoopStep, HAULER_LOOP_INTERVAL_MS);
};

const startHaulerLoopIfNeeded = () => {
  if (haulerLoopTimeout) return;
  runHaulerLoopStep();
};

const advanceHaulerLoop = () => {
  if (haulerLoopTimeout) {
    clearTimeout(haulerLoopTimeout);
    haulerLoopTimeout = null;
  }
  runHaulerLoopStep();
};

const FRIGATE_WAIT_MS = 10000;
type FrigateStep = { name: string; apply: () => number };

const frigateSteps: FrigateStep[] = [
  {
    name: 'frigate-docked-a1',
    apply: () => {
      setFrigateDocked(waypointA);
      return FRIGATE_WAIT_MS;
    },
  },
  {
    name: 'frigate-orbit-a1',
    apply: () => {
      setFrigateOrbit(waypointA);
      return FRIGATE_WAIT_MS;
    },
  },
  {
    name: 'frigate-transit-a1-b1',
    apply: () => Math.max(setFrigateTransit(waypointA, waypointB), 1000),
  },
  {
    name: 'frigate-orbit-b1',
    apply: () => {
      setFrigateOrbit(waypointB);
      return FRIGATE_WAIT_MS;
    },
  },
  {
    name: 'frigate-docked-b1',
    apply: () => {
      setFrigateDocked(waypointB);
      return FRIGATE_WAIT_MS;
    },
  },
  {
    name: 'frigate-orbit-b1-hold',
    apply: () => {
      setFrigateOrbit(waypointB);
      return FRIGATE_WAIT_MS;
    },
  },
  {
    name: 'frigate-transit-b1-a1',
    apply: () => Math.max(setFrigateTransit(waypointB, waypointA), 1000),
  },
];

let frigateLoopIndex = 0;
let frigateLoopTimeout: ReturnType<typeof setTimeout> | null = null;

const runFrigateLoopStep = () => {
  const step = frigateSteps[frigateLoopIndex];
  const duration = step.apply();
  const waitMs = Math.max(1000, Math.floor(duration));
  frigateLoopTimeout = setTimeout(() => {
    frigateLoopIndex = (frigateLoopIndex + 1) % frigateSteps.length;
    runFrigateLoopStep();
  }, waitMs);
};

const startFrigateLoopIfNeeded = () => {
  if (frigateLoopTimeout) return;
  runFrigateLoopStep();
};

const advanceFrigateLoop = () => {
  if (frigateLoopTimeout) {
    clearTimeout(frigateLoopTimeout);
    frigateLoopTimeout = null;
  }
  runFrigateLoopStep();
};

const phases: Phase[] = [
  {
    name: 'explorer-mining',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      setExplorerMining();
      setMinerOrbit();
      setDroneMining();
      return DEFAULT_PHASE_DURATION;
    },
  },
  {
    name: 'explorer-transit-a1',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      const explorerTransit = setExplorerTransit(waypointMining, waypointA, 8, 'CRUISE');
      setMinerOrbit();
      setDroneMining();
      return Math.max(explorerTransit, DEFAULT_PHASE_DURATION);
    },
  },
  {
    name: 'explorer-docked-a1',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      setExplorerDocked(waypointA);
      setMinerOrbit();
      setDroneMining();
      return DEFAULT_PHASE_DURATION;
    },
  },
  {
    name: 'explorer-orbit-a1',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      setExplorerOrbit(waypointA);
      setMinerOrbit();
      setDroneDockedRefuel();
      return DEFAULT_PHASE_DURATION;
    },
  },
  {
    name: 'explorer-return-to-mine',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      const explorerTransit = setExplorerTransit(waypointA, waypointMining, 10, 'BURN');
      setMinerOrbit();
      const droneTransit = setDroneTransitToMine();
      return Math.max(explorerTransit, droneTransit, DEFAULT_PHASE_DURATION);
    },
  },
  {
    name: 'explorer-resume-mining',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      setExplorerMining();
      setMinerOrbit();
      setDroneMining();
      return DEFAULT_PHASE_DURATION;
    },
  },
  {
    name: 'frigate-return-a1',
    duration: DEFAULT_PHASE_DURATION,
    apply: () => {
      setExplorerOrbit(waypointMining);
      setMinerOrbit();
      setDroneMining();
      return DEFAULT_PHASE_DURATION;
    },
  },
];

let scenarioRunning = false;
let phaseIndex = 0;
let phaseTimeout: ReturnType<typeof setTimeout> | null = null;

const runPhase = () => {
  const phase = phases[phaseIndex];
  const nextDuration = phase.apply();
  const computedDuration =
    typeof nextDuration === 'number' && Number.isFinite(nextDuration) ? nextDuration : phase.duration;
  phase.duration = Math.max(1000, Math.floor(computedDuration));
  phaseTimeout = setTimeout(() => {
    phaseIndex = (phaseIndex + 1) % phases.length;
    runPhase();
  }, phase.duration);
};

export const startMockScenarioIfNeeded = () => {
  if (scenarioRunning) return;
  scenarioRunning = true;
  startHaulerLoopIfNeeded();
  startFrigateLoopIfNeeded();
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
  advanceHaulerLoop();
  advanceFrigateLoop();
  runPhase();
};
