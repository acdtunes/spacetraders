import type { Agent, Ship, System, Waypoint, Market, FlightMode, ShipNavRoute } from '../types/spacetraders';

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

const isTestEnvironment = typeof import.meta !== 'undefined' && Boolean((import.meta as any).vitest);

const systemSymbol = 'X1-DEMO';
const gridSystemSymbol = 'X1-GRID';
const twinSystemSymbol = 'X1-TWIN';

// Comprehensive waypoint definitions - all types, no axis-aligned positions
const waypoints = {
  PLANET_MARKET: 'X1-DEMO-PM1',
  GAS_GIANT: 'X1-DEMO-GG1',
  MOON_ICE: 'X1-DEMO-MOON1',
  MOON_VOLCANIC: 'X1-DEMO-MOON2',
  MOON_ROCKY: 'X1-DEMO-MOON3',
  ORBITAL_STATION: 'X1-DEMO-OS1',
  ORBITAL_SHIPYARD: 'X1-DEMO-OS2',
  JUMP_GATE: 'X1-DEMO-JG1',
  ASTEROID_FIELD: 'X1-DEMO-AF1',
  ASTEROID_RICH: 'X1-DEMO-AST1',
  ASTEROID_RICH_2: 'X1-DEMO-AST2',
  ASTEROID_RICH_3: 'X1-DEMO-AST3',
  ENGINEERED_ASTEROID: 'X1-DEMO-EAST1',
  ASTEROID_BASE: 'X1-DEMO-AB1',
  NEBULA: 'X1-DEMO-NEB1',
  DEBRIS_FIELD: 'X1-DEMO-DF1',
  GRAVITY_WELL: 'X1-DEMO-GW1',
  ARTIFICIAL_GRAVITY_WELL: 'X1-DEMO-AGW1',
  FUEL_STATION: 'X1-DEMO-FS1',
} as const;

// Grid system waypoints - arranged in two 4x4 matrices (left and right)
const gridWaypoints = {
  // Left grid (A-D columns)
  GRID_A1: 'X1-GRID-A1',
  GRID_A2: 'X1-GRID-A2',
  GRID_A3: 'X1-GRID-A3',
  GRID_A4: 'X1-GRID-A4',
  GRID_B1: 'X1-GRID-B1',
  GRID_B2: 'X1-GRID-B2',
  GRID_B3: 'X1-GRID-B3',
  GRID_B4: 'X1-GRID-B4',
  GRID_C1: 'X1-GRID-C1',
  GRID_C2: 'X1-GRID-C2',
  GRID_C3: 'X1-GRID-C3',
  GRID_C4: 'X1-GRID-C4',
  GRID_D1: 'X1-GRID-D1',
  GRID_D2: 'X1-GRID-D2',
  GRID_D3: 'X1-GRID-D3',
  GRID_D4: 'X1-GRID-D4',
  // Right grid (E-H columns)
  GRID_E1: 'X1-GRID-E1',
  GRID_E2: 'X1-GRID-E2',
  GRID_E3: 'X1-GRID-E3',
  GRID_E4: 'X1-GRID-E4',
  GRID_F1: 'X1-GRID-F1',
  GRID_F2: 'X1-GRID-F2',
  GRID_F3: 'X1-GRID-F3',
  GRID_F4: 'X1-GRID-F4',
  GRID_G1: 'X1-GRID-G1',
  GRID_G2: 'X1-GRID-G2',
  GRID_G3: 'X1-GRID-G3',
  GRID_G4: 'X1-GRID-G4',
  GRID_H1: 'X1-GRID-H1',
  GRID_H2: 'X1-GRID-H2',
  GRID_H3: 'X1-GRID-H3',
  GRID_H4: 'X1-GRID-H4',
} as const;

// Twin system waypoints - gas giant and planet on diagonal
const twinWaypoints = {
  GAS_GIANT: 'X1-TWIN-GG1',
  PLANET: 'X1-TWIN-P1',
} as const;

// Grid positions - evenly spaced in 4x4 matrix (100 unit spacing)
const GRID_SPACING = 100;
const GRID_OFFSET = -150; // Left grid offset
const GRID_RIGHT_OFFSET = 250; // Right grid offset (100 units right of left grid)
const gridWaypointPositions: Record<string, { x: number; y: number }> = {
  // Left grid (A-D)
  [gridWaypoints.GRID_A1]: { x: GRID_OFFSET, y: GRID_OFFSET },
  [gridWaypoints.GRID_A2]: { x: GRID_OFFSET + GRID_SPACING, y: GRID_OFFSET },
  [gridWaypoints.GRID_A3]: { x: GRID_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET },
  [gridWaypoints.GRID_A4]: { x: GRID_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET },

  [gridWaypoints.GRID_B1]: { x: GRID_OFFSET, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_B2]: { x: GRID_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_B3]: { x: GRID_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_B4]: { x: GRID_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING },

  [gridWaypoints.GRID_C1]: { x: GRID_OFFSET, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_C2]: { x: GRID_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_C3]: { x: GRID_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_C4]: { x: GRID_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING * 2 },

  [gridWaypoints.GRID_D1]: { x: GRID_OFFSET, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_D2]: { x: GRID_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_D3]: { x: GRID_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_D4]: { x: GRID_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING * 3 },

  // Right grid (E-H)
  [gridWaypoints.GRID_E1]: { x: GRID_RIGHT_OFFSET, y: GRID_OFFSET },
  [gridWaypoints.GRID_E2]: { x: GRID_RIGHT_OFFSET + GRID_SPACING, y: GRID_OFFSET },
  [gridWaypoints.GRID_E3]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET },
  [gridWaypoints.GRID_E4]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET },

  [gridWaypoints.GRID_F1]: { x: GRID_RIGHT_OFFSET, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_F2]: { x: GRID_RIGHT_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_F3]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING },
  [gridWaypoints.GRID_F4]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING },

  [gridWaypoints.GRID_G1]: { x: GRID_RIGHT_OFFSET, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_G2]: { x: GRID_RIGHT_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_G3]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING * 2 },
  [gridWaypoints.GRID_G4]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING * 2 },

  [gridWaypoints.GRID_H1]: { x: GRID_RIGHT_OFFSET, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_H2]: { x: GRID_RIGHT_OFFSET + GRID_SPACING, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_H3]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 2, y: GRID_OFFSET + GRID_SPACING * 3 },
  [gridWaypoints.GRID_H4]: { x: GRID_RIGHT_OFFSET + GRID_SPACING * 3, y: GRID_OFFSET + GRID_SPACING * 3 },
};

const gridWaypointTypes: Record<string, Waypoint['type']> = {
  // Left grid (A-D)
  [gridWaypoints.GRID_A1]: 'PLANET',
  [gridWaypoints.GRID_A2]: 'ASTEROID',
  [gridWaypoints.GRID_A3]: 'MOON',
  [gridWaypoints.GRID_A4]: 'ORBITAL_STATION',
  [gridWaypoints.GRID_B1]: 'ASTEROID_FIELD',
  [gridWaypoints.GRID_B2]: 'FUEL_STATION',
  [gridWaypoints.GRID_B3]: 'GAS_GIANT',
  [gridWaypoints.GRID_B4]: 'PLANET',
  [gridWaypoints.GRID_C1]: 'NEBULA',
  [gridWaypoints.GRID_C2]: 'DEBRIS_FIELD',
  [gridWaypoints.GRID_C3]: 'ASTEROID_BASE',
  [gridWaypoints.GRID_C4]: 'ENGINEERED_ASTEROID',
  [gridWaypoints.GRID_D1]: 'MOON',
  [gridWaypoints.GRID_D2]: 'ORBITAL_STATION',
  [gridWaypoints.GRID_D3]: 'ASTEROID',
  [gridWaypoints.GRID_D4]: 'JUMP_GATE',
  // Right grid (E-H)
  [gridWaypoints.GRID_E1]: 'GAS_GIANT',
  [gridWaypoints.GRID_E2]: 'MOON',
  [gridWaypoints.GRID_E3]: 'PLANET',
  [gridWaypoints.GRID_E4]: 'ASTEROID',
  [gridWaypoints.GRID_F1]: 'ORBITAL_STATION',
  [gridWaypoints.GRID_F2]: 'ASTEROID_BASE',
  [gridWaypoints.GRID_F3]: 'FUEL_STATION',
  [gridWaypoints.GRID_F4]: 'NEBULA',
  [gridWaypoints.GRID_G1]: 'PLANET',
  [gridWaypoints.GRID_G2]: 'ENGINEERED_ASTEROID',
  [gridWaypoints.GRID_G3]: 'ASTEROID',
  [gridWaypoints.GRID_G4]: 'DEBRIS_FIELD',
  [gridWaypoints.GRID_H1]: 'ASTEROID_FIELD',
  [gridWaypoints.GRID_H2]: 'MOON',
  [gridWaypoints.GRID_H3]: 'JUMP_GATE',
  [gridWaypoints.GRID_H4]: 'ORBITAL_STATION',
};

const twinWaypointTypes: Record<string, Waypoint['type']> = {
  [twinWaypoints.GAS_GIANT]: 'GAS_GIANT',
  [twinWaypoints.PLANET]: 'PLANET',
};

// Positions - avoiding x/y axes, creating interesting clusters
// Waypoints that orbit share the SAME coordinates - visualizer auto-arranges them
const waypointPositions: Record<string, { x: number; y: number }> = {
  // Planet Market orbital system (all at same location)
  [waypoints.PLANET_MARKET]: { x: 180, y: 75 },
  [waypoints.ORBITAL_STATION]: { x: 180, y: 75 }, // Same as planet - will be offset by visualizer
  [waypoints.ORBITAL_SHIPYARD]: { x: 180, y: 75 }, // Same as planet - will be offset by visualizer

  // Gas Giant orbital system (all at same location)
  [waypoints.GAS_GIANT]: { x: -120, y: 160 },
  [waypoints.MOON_ICE]: { x: -120, y: 160 }, // Same as gas giant - will be offset by visualizer
  [waypoints.MOON_VOLCANIC]: { x: -120, y: 160 }, // Same as gas giant - will be offset by visualizer
  [waypoints.MOON_ROCKY]: { x: -120, y: 160 }, // Same as gas giant - will be offset by visualizer

  // Asteroid cluster system (close together but not same location for variety)
  [waypoints.ASTEROID_FIELD]: { x: -175, y: -85 },
  [waypoints.ASTEROID_RICH]: { x: -150, y: -110 }, // Separate location
  [waypoints.ASTEROID_RICH_2]: { x: -165, y: -95 }, // Separate location
  [waypoints.ASTEROID_RICH_3]: { x: -185, y: -102 }, // Separate location
  [waypoints.ASTEROID_BASE]: { x: -200, y: -60 }, // Near cluster

  // Standalone waypoints
  [waypoints.JUMP_GATE]: { x: 45, y: 220 },
  [waypoints.ENGINEERED_ASTEROID]: { x: 95, y: -145 },
  [waypoints.NEBULA]: { x: 125, y: -175 },
  [waypoints.DEBRIS_FIELD]: { x: -65, y: -195 },
  [waypoints.GRAVITY_WELL]: { x: 210, y: -45 },
  [waypoints.ARTIFICIAL_GRAVITY_WELL]: { x: -35, y: 145 },
  [waypoints.FUEL_STATION]: { x: 75, y: 185 },

  // Twin system waypoints
  [twinWaypoints.GAS_GIANT]: { x: -35, y: -35 },
  [twinWaypoints.PLANET]: { x: 35, y: 35 },
};

const waypointTypes: Record<string, Waypoint['type']> = {
  [waypoints.PLANET_MARKET]: 'PLANET',
  [waypoints.GAS_GIANT]: 'GAS_GIANT',
  [waypoints.MOON_ICE]: 'MOON',
  [waypoints.MOON_VOLCANIC]: 'MOON',
  [waypoints.MOON_ROCKY]: 'MOON',
  [waypoints.ORBITAL_STATION]: 'ORBITAL_STATION',
  [waypoints.ORBITAL_SHIPYARD]: 'ORBITAL_STATION',
  [waypoints.JUMP_GATE]: 'JUMP_GATE',
  [waypoints.ASTEROID_FIELD]: 'ASTEROID_FIELD',
  [waypoints.ASTEROID_RICH]: 'ASTEROID',
  [waypoints.ASTEROID_RICH_2]: 'ASTEROID',
  [waypoints.ASTEROID_RICH_3]: 'ASTEROID',
  [waypoints.ENGINEERED_ASTEROID]: 'ENGINEERED_ASTEROID',
  [waypoints.ASTEROID_BASE]: 'ASTEROID_BASE',
  [waypoints.NEBULA]: 'NEBULA',
  [waypoints.DEBRIS_FIELD]: 'DEBRIS_FIELD',
  [waypoints.GRAVITY_WELL]: 'GRAVITY_WELL',
  [waypoints.ARTIFICIAL_GRAVITY_WELL]: 'ARTIFICIAL_GRAVITY_WELL',
  [waypoints.FUEL_STATION]: 'FUEL_STATION',
};

// SpaceTraders flight mode time multipliers (from actual API)
// Formula: travel_seconds = (distance * multiplier) / engine_speed
// Lower multiplier = faster travel time
const FLIGHT_MODE_MULTIPLIERS: Record<FlightMode, number> = {
  BURN: 15,      // Fastest - consumes 2x fuel
  DRIFT: 26,     // Fuel efficient - consumes 0.003x fuel
  CRUISE: 31,    // Balanced - consumes 1x fuel
  STEALTH: 50,   // Slowest - consumes 1x fuel
};

const DEFAULT_ENGINE_SPEED = 80;

// Extended ship roles with engine speeds
const ENGINE_SPEED_BY_ROLE: Record<string, number> = {
  COMMAND: 110,
  EXPLORER: 95,
  EXCAVATOR: 70,
  HAULER: 85,
  MINING_DRONE: 120,
  SURVEYOR: 100,
  REFINERY: 65,
  TRANSPORT: 80,
  PATROL: 105,
  SATELLITE: 90,
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
      symbol: 'STARFLEET',
      color: '#60a5fa',
      visible: true,
      createdAt: now(),
      credits: 125000,
    },
    {
      id: 'AGENT-2',
      symbol: 'TRADERS_GUILD',
      color: '#f472b6',
      visible: true,
      createdAt: now(),
      credits: 89000,
    },
    {
      id: 'AGENT-3',
      symbol: 'MINERS_UNION',
      color: '#fbbf24',
      visible: true,
      createdAt: now(),
      credits: 156000,
    },
    {
      id: 'AGENT-4',
      symbol: 'GRID_OPS',
      color: '#a855f7',
      visible: true,
      createdAt: now(),
      credits: 200000,
    },
    {
      id: 'AGENT-5',
      symbol: 'TWIN_PATROL',
      color: '#10b981',
      visible: true,
      createdAt: now(),
      credits: 150000,
    },
  ],
  systems: [
    {
      symbol: systemSymbol,
      sectorSymbol: 'DEMO-SECTOR',
      type: 'YELLOW_STAR',
      x: 0,
      y: 0,
      waypoints: Object.entries(waypoints).map(([_, symbol]) => ({
        symbol,
        type: waypointTypes[symbol],
        systemSymbol,
        ...waypointPositions[symbol],
      })),
      factions: [],
    },
    {
      symbol: gridSystemSymbol,
      sectorSymbol: 'GRID-SECTOR',
      type: 'RED_STAR',
      x: 100,
      y: 100,
      waypoints: Object.entries(gridWaypoints).map(([_, symbol]) => ({
        symbol,
        type: gridWaypointTypes[symbol],
        systemSymbol: gridSystemSymbol,
        ...gridWaypointPositions[symbol],
      })),
      factions: [],
    },
    {
      symbol: twinSystemSymbol,
      sectorSymbol: 'TWIN-SECTOR',
      type: 'BLUE_STAR',
      x: 200,
      y: 200,
      waypoints: Object.entries(twinWaypoints).map(([_, symbol]) => ({
        symbol,
        type: twinWaypointTypes[symbol],
        systemSymbol: twinSystemSymbol,
        ...waypointPositions[symbol],
      })),
      factions: [],
    },
  ],
  waypoints: [
    {
      symbol: waypoints.PLANET_MARKET,
      type: 'PLANET',
      systemSymbol,
      ...waypointPositions[waypoints.PLANET_MARKET],
      orbitals: [{ symbol: waypoints.ORBITAL_STATION }, { symbol: waypoints.ORBITAL_SHIPYARD }],
      traits: [
        { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'A thriving center of commerce where traders from across the galaxy gather' },
        { symbol: 'SCATTERED_SETTLEMENTS', name: 'Scattered Settlements', description: 'Small, distributed population centers' },
        { symbol: 'TEMPERATE', name: 'Temperate', description: 'Mild climate suitable for habitation' },
      ],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypoints.GAS_GIANT,
      type: 'GAS_GIANT',
      systemSymbol,
      ...waypointPositions[waypoints.GAS_GIANT],
      orbitals: [{ symbol: waypoints.MOON_ICE }, { symbol: waypoints.MOON_VOLCANIC }, { symbol: waypoints.MOON_ROCKY }],
      traits: [
        { symbol: 'JOVIAN', name: 'Jovian', description: 'Gas giant planet' },
        { symbol: 'STRONG_GRAVITY', name: 'Strong Gravity', description: 'High gravitational pull' },
        { symbol: 'TOXIC_ATMOSPHERE', name: 'Toxic Atmosphere', description: 'Poisonous gases' },
        { symbol: 'EXTREME_PRESSURE', name: 'Extreme Pressure', description: 'Crushing atmospheric pressure' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.MOON_ICE,
      type: 'MOON',
      systemSymbol,
      ...waypointPositions[waypoints.MOON_ICE],
      orbitals: [],
      orbits: waypoints.GAS_GIANT,
      traits: [
        { symbol: 'FROZEN', name: 'Frozen', description: 'Completely frozen surface' },
        { symbol: 'ICE_CRYSTALS', name: 'Ice Crystals', description: 'Abundant crystalline ice formations' },
        { symbol: 'WEAK_GRAVITY', name: 'Weak Gravity', description: 'Low gravitational pull' },
        { symbol: 'THIN_ATMOSPHERE', name: 'Thin Atmosphere', description: 'Minimal atmospheric pressure' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.MOON_VOLCANIC,
      type: 'MOON',
      systemSymbol,
      ...waypointPositions[waypoints.MOON_VOLCANIC],
      orbitals: [],
      orbits: waypoints.GAS_GIANT,
      traits: [
        { symbol: 'VOLCANIC', name: 'Volcanic', description: 'Active volcanic activity' },
        { symbol: 'MAGMA_SEAS', name: 'Magma Seas', description: 'Vast oceans of molten rock' },
        { symbol: 'EXTREME_TEMPERATURES', name: 'Extreme Temperatures', description: 'Dangerously hot environment' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.MOON_ROCKY,
      type: 'MOON',
      systemSymbol,
      ...waypointPositions[waypoints.MOON_ROCKY],
      orbitals: [],
      orbits: waypoints.GAS_GIANT,
      traits: [
        { symbol: 'ROCKY', name: 'Rocky', description: 'Solid rocky composition' },
        { symbol: 'BARREN', name: 'Barren', description: 'Lifeless and desolate' },
        { symbol: 'WEAK_GRAVITY', name: 'Weak Gravity', description: 'Low gravitational pull' },
        { symbol: 'MINERAL_DEPOSITS', name: 'Mineral Deposits', description: 'Valuable mineral resources' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ORBITAL_STATION,
      type: 'ORBITAL_STATION',
      systemSymbol,
      ...waypointPositions[waypoints.ORBITAL_STATION],
      orbitals: [],
      orbits: waypoints.PLANET_MARKET,
      traits: [
        { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading and commerce center' },
        { symbol: 'OUTPOST', name: 'Outpost', description: 'Remote station' },
      ],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypoints.ORBITAL_SHIPYARD,
      type: 'ORBITAL_STATION',
      systemSymbol,
      ...waypointPositions[waypoints.ORBITAL_SHIPYARD],
      orbitals: [],
      orbits: waypoints.PLANET_MARKET,
      traits: [
        { symbol: 'SHIPYARD', name: 'Shipyard', description: 'Spacecraft construction and repair facility' },
        { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading and commerce center' },
        { symbol: 'OUTPOST', name: 'Outpost', description: 'Remote station' },
      ],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypoints.JUMP_GATE,
      type: 'JUMP_GATE',
      systemSymbol,
      ...waypointPositions[waypoints.JUMP_GATE],
      orbitals: [],
      traits: [],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ASTEROID_FIELD,
      type: 'ASTEROID_FIELD',
      systemSymbol,
      ...waypointPositions[waypoints.ASTEROID_FIELD],
      orbitals: [],
      traits: [
        { symbol: 'COMMON_METAL_DEPOSITS', name: 'Common Metal Deposits', description: 'Iron, copper, and aluminum ore' },
        { symbol: 'MINERAL_DEPOSITS', name: 'Mineral Deposits', description: 'Various mineral resources' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ASTEROID_RICH,
      type: 'ASTEROID',
      systemSymbol,
      ...waypointPositions[waypoints.ASTEROID_RICH],
      orbitals: [],
      traits: [
        { symbol: 'PRECIOUS_METAL_DEPOSITS', name: 'Precious Metal Deposits', description: 'Gold, silver, and platinum ore' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ASTEROID_RICH_2,
      type: 'ASTEROID',
      systemSymbol,
      ...waypointPositions[waypoints.ASTEROID_RICH_2],
      orbitals: [],
      traits: [
        { symbol: 'RARE_METAL_DEPOSITS', name: 'Rare Metal Deposits', description: 'Scarce and valuable metals' },
        { symbol: 'PRECIOUS_METAL_DEPOSITS', name: 'Precious Metal Deposits', description: 'Gold, silver, and platinum ore' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ASTEROID_RICH_3,
      type: 'ASTEROID',
      systemSymbol,
      ...waypointPositions[waypoints.ASTEROID_RICH_3],
      orbitals: [],
      traits: [
        { symbol: 'COMMON_METAL_DEPOSITS', name: 'Common Metal Deposits', description: 'Iron, copper, and aluminum ore' },
        { symbol: 'MINERAL_DEPOSITS', name: 'Mineral Deposits', description: 'Various mineral resources' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ENGINEERED_ASTEROID,
      type: 'ENGINEERED_ASTEROID',
      systemSymbol,
      ...waypointPositions[waypoints.ENGINEERED_ASTEROID],
      orbitals: [],
      traits: [
        { symbol: 'RESEARCH_FACILITY', name: 'Research Facility', description: 'Advanced scientific research station' },
        { symbol: 'MEGA_STRUCTURES', name: 'Mega Structures', description: 'Massive engineered constructions' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ASTEROID_BASE,
      type: 'ASTEROID_BASE',
      systemSymbol,
      ...waypointPositions[waypoints.ASTEROID_BASE],
      orbitals: [],
      traits: [
        { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading and commerce center' },
        { symbol: 'OUTPOST', name: 'Outpost', description: 'Remote mining station' },
        { symbol: 'COMMON_METAL_DEPOSITS', name: 'Common Metal Deposits', description: 'Iron, copper, and aluminum ore' },
      ],
      isUnderConstruction: false,
      hasMarketplace: true,
    },
    {
      symbol: waypoints.NEBULA,
      type: 'NEBULA',
      systemSymbol,
      ...waypointPositions[waypoints.NEBULA],
      orbitals: [],
      traits: [
        { symbol: 'EXPLOSIVE_GASES', name: 'Explosive Gases', description: 'Volatile gas clouds' },
        { symbol: 'VIBRANT_AURORAS', name: 'Vibrant Auroras', description: 'Beautiful energy displays' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.DEBRIS_FIELD,
      type: 'DEBRIS_FIELD',
      systemSymbol,
      ...waypointPositions[waypoints.DEBRIS_FIELD],
      orbitals: [],
      traits: [
        { symbol: 'DEBRIS_CLUSTER', name: 'Debris Cluster', description: 'Scattered wreckage' },
        { symbol: 'SHALLOW_CRATERS', name: 'Shallow Craters', description: 'Impact marks from collisions' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.GRAVITY_WELL,
      type: 'GRAVITY_WELL',
      systemSymbol,
      ...waypointPositions[waypoints.GRAVITY_WELL],
      orbitals: [],
      traits: [
        { symbol: 'MICRO_GRAVITY_ANOMALIES', name: 'Micro Gravity Anomalies', description: 'Unusual gravitational fluctuations' },
        { symbol: 'STRONG_MAGNETOSPHERE', name: 'Strong Magnetosphere', description: 'Powerful magnetic field' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.ARTIFICIAL_GRAVITY_WELL,
      type: 'ARTIFICIAL_GRAVITY_WELL',
      systemSymbol,
      ...waypointPositions[waypoints.ARTIFICIAL_GRAVITY_WELL],
      orbitals: [],
      traits: [
        { symbol: 'MEGA_STRUCTURES', name: 'Mega Structures', description: 'Massive engineered constructions' },
        { symbol: 'STRONG_MAGNETOSPHERE', name: 'Strong Magnetosphere', description: 'Powerful magnetic field' },
        { symbol: 'MILITARY_BASE', name: 'Military Base', description: 'Armed forces installation' },
      ],
      isUnderConstruction: false,
      hasMarketplace: false,
    },
    {
      symbol: waypoints.FUEL_STATION,
      type: 'FUEL_STATION',
      systemSymbol,
      ...waypointPositions[waypoints.FUEL_STATION],
      orbitals: [],
      traits: [
        { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading and commerce center' },
        { symbol: 'OUTPOST', name: 'Outpost', description: 'Remote refueling station' },
      ],
      isUnderConstruction: false,
      hasMarketplace: true,
    },

    // Grid System Waypoints (4x4 matrix)
    { symbol: gridWaypoints.GRID_A1, type: 'PLANET', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_A1], orbitals: [], traits: [{ symbol: 'TEMPERATE', name: 'Temperate', description: 'Mild climate' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_A2, type: 'ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_A2], orbitals: [], traits: [{ symbol: 'COMMON_METAL_DEPOSITS', name: 'Common Metal Deposits', description: 'Iron and copper' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_A3, type: 'MOON', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_A3], orbitals: [], traits: [{ symbol: 'BARREN', name: 'Barren', description: 'Lifeless surface' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_A4, type: 'ORBITAL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_A4], orbitals: [], traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading hub' }], isUnderConstruction: false, hasMarketplace: true },

    { symbol: gridWaypoints.GRID_B1, type: 'ASTEROID_FIELD', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_B1], orbitals: [], traits: [{ symbol: 'MINERAL_DEPOSITS', name: 'Mineral Deposits', description: 'Rich minerals' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_B2, type: 'FUEL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_B2], orbitals: [], traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Fuel sales' }], isUnderConstruction: false, hasMarketplace: true },
    { symbol: gridWaypoints.GRID_B3, type: 'GAS_GIANT', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_B3], orbitals: [], traits: [{ symbol: 'JOVIAN', name: 'Jovian', description: 'Gas giant' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_B4, type: 'PLANET', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_B4], orbitals: [], traits: [{ symbol: 'ROCKY', name: 'Rocky', description: 'Rocky terrain' }], isUnderConstruction: false, hasMarketplace: false },

    { symbol: gridWaypoints.GRID_C1, type: 'NEBULA', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_C1], orbitals: [], traits: [{ symbol: 'EXPLOSIVE_GASES', name: 'Explosive Gases', description: 'Volatile gases' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_C2, type: 'DEBRIS_FIELD', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_C2], orbitals: [], traits: [{ symbol: 'DEBRIS_CLUSTER', name: 'Debris Cluster', description: 'Wreckage' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_C3, type: 'ASTEROID_BASE', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_C3], orbitals: [], traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Mining outpost' }], isUnderConstruction: false, hasMarketplace: true },
    { symbol: gridWaypoints.GRID_C4, type: 'ENGINEERED_ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_C4], orbitals: [], traits: [{ symbol: 'RESEARCH_FACILITY', name: 'Research Facility', description: 'Science station' }], isUnderConstruction: false, hasMarketplace: false },

    { symbol: gridWaypoints.GRID_D1, type: 'MOON', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_D1], orbitals: [], traits: [{ symbol: 'FROZEN', name: 'Frozen', description: 'Ice covered' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_D2, type: 'ORBITAL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_D2], orbitals: [], traits: [{ symbol: 'SHIPYARD', name: 'Shipyard', description: 'Ship construction' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_D3, type: 'ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_D3], orbitals: [], traits: [{ symbol: 'PRECIOUS_METAL_DEPOSITS', name: 'Precious Metal Deposits', description: 'Gold and platinum' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_D4, type: 'JUMP_GATE', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_D4], orbitals: [], traits: [], isUnderConstruction: false, hasMarketplace: false },

    // Grid System Right Waypoints (4x4 matrix - E through H columns)
    { symbol: gridWaypoints.GRID_E1, type: 'GAS_GIANT', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_E1], orbitals: [], traits: [{ symbol: 'JOVIAN', name: 'Jovian', description: 'Gas giant' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_E2, type: 'MOON', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_E2], orbitals: [], traits: [{ symbol: 'VOLCANIC', name: 'Volcanic', description: 'Active volcanoes' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_E3, type: 'PLANET', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_E3], orbitals: [], traits: [{ symbol: 'OCEAN', name: 'Ocean', description: 'Water world' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_E4, type: 'ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_E4], orbitals: [], traits: [{ symbol: 'MINERAL_DEPOSITS', name: 'Mineral Deposits', description: 'Rich minerals' }], isUnderConstruction: false, hasMarketplace: false },

    { symbol: gridWaypoints.GRID_F1, type: 'ORBITAL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_F1], orbitals: [], traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trade station' }], isUnderConstruction: false, hasMarketplace: true },
    { symbol: gridWaypoints.GRID_F2, type: 'ASTEROID_BASE', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_F2], orbitals: [], traits: [{ symbol: 'OUTPOST', name: 'Outpost', description: 'Mining base' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_F3, type: 'FUEL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_F3], orbitals: [], traits: [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Fuel depot' }], isUnderConstruction: false, hasMarketplace: true },
    { symbol: gridWaypoints.GRID_F4, type: 'NEBULA', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_F4], orbitals: [], traits: [{ symbol: 'VIBRANT_AURORAS', name: 'Vibrant Auroras', description: 'Beautiful lights' }], isUnderConstruction: false, hasMarketplace: false },

    { symbol: gridWaypoints.GRID_G1, type: 'PLANET', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_G1], orbitals: [], traits: [{ symbol: 'FROZEN', name: 'Frozen', description: 'Ice world' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_G2, type: 'ENGINEERED_ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_G2], orbitals: [], traits: [{ symbol: 'RESEARCH_FACILITY', name: 'Research Facility', description: 'Science lab' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_G3, type: 'ASTEROID', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_G3], orbitals: [], traits: [{ symbol: 'RARE_METAL_DEPOSITS', name: 'Rare Metal Deposits', description: 'Valuable ores' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_G4, type: 'DEBRIS_FIELD', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_G4], orbitals: [], traits: [{ symbol: 'DEBRIS_CLUSTER', name: 'Debris Cluster', description: 'Salvage site' }], isUnderConstruction: false, hasMarketplace: false },

    { symbol: gridWaypoints.GRID_H1, type: 'ASTEROID_FIELD', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_H1], orbitals: [], traits: [{ symbol: 'COMMON_METAL_DEPOSITS', name: 'Common Metal Deposits', description: 'Iron deposits' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_H2, type: 'MOON', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_H2], orbitals: [], traits: [{ symbol: 'BARREN', name: 'Barren', description: 'Lifeless rock' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_H3, type: 'JUMP_GATE', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_H3], orbitals: [], traits: [], isUnderConstruction: false, hasMarketplace: false },
    { symbol: gridWaypoints.GRID_H4, type: 'ORBITAL_STATION', systemSymbol: gridSystemSymbol, ...gridWaypointPositions[gridWaypoints.GRID_H4], orbitals: [], traits: [{ symbol: 'SHIPYARD', name: 'Shipyard', description: 'Ship factory' }], isUnderConstruction: false, hasMarketplace: false },

    // Twin System Waypoints
    { symbol: twinWaypoints.GAS_GIANT, type: 'GAS_GIANT', systemSymbol: twinSystemSymbol, ...waypointPositions[twinWaypoints.GAS_GIANT], orbitals: [], traits: [{ symbol: 'JOVIAN', name: 'Jovian', description: 'Massive gas giant' }], isUnderConstruction: false, hasMarketplace: false },
    { symbol: twinWaypoints.PLANET, type: 'PLANET', systemSymbol: twinSystemSymbol, ...waypointPositions[twinWaypoints.PLANET], orbitals: [], traits: [{ symbol: 'TEMPERATE', name: 'Temperate', description: 'Earth-like planet' }, { symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trading hub' }], isUnderConstruction: false, hasMarketplace: true },
  ],
  ships: [
    // AGENT-1 (STARFLEET) - 5 ships
    {
      symbol: 'STARFLEET-EXPLORER-1',
      registration: { name: 'USS Discovery', factionSymbol: 'STARFLEET', role: 'EXPLORER' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.NEBULA,
        route: baseRoute(waypoints.NEBULA, waypoints.JUMP_GATE, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 5, inventory: [{ symbol: 'EXOTIC_MATTER', name: 'Exotic Matter', description: '', units: 5 }] },
      fuel: { current: 85, capacity: 100 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'STARFLEET-COMMAND-1',
      registration: { name: 'USS Enterprise', factionSymbol: 'STARFLEET', role: 'COMMAND' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ORBITAL_STATION,
        route: baseRoute(waypoints.ORBITAL_STATION, waypoints.PLANET_MARKET, 'DRIFT', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: { capacity: 60, units: 0, inventory: [] },
      fuel: { current: 120, capacity: 150 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'STARFLEET-PATROL-1',
      registration: { name: 'USS Defiant', factionSymbol: 'STARFLEET', role: 'PATROL' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.GRAVITY_WELL,
        route: baseRoute(waypoints.GRAVITY_WELL, waypoints.ARTIFICIAL_GRAVITY_WELL, 'BURN', ENGINE_SPEED_BY_ROLE.PATROL),
        status: 'IN_TRANSIT',
        flightMode: 'BURN',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.PATROL },
      modules: [],
      mounts: [],
      cargo: { capacity: 30, units: 0, inventory: [] },
      fuel: { current: 60, capacity: 80 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'STARFLEET-SURVEYOR-1',
      registration: { name: 'USS Voyager', factionSymbol: 'STARFLEET', role: 'SURVEYOR' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.MOON_ROCKY,
        route: baseRoute(waypoints.MOON_ROCKY, waypoints.MOON_ROCKY, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 50, units: 12, inventory: [{ symbol: 'QUARTZ_SAND', name: 'Mineral Samples', description: '', units: 12 }] },
      fuel: { current: 70, capacity: 90 },
      cooldown: { shipSymbol: 'STARFLEET-SURVEYOR-1', totalSeconds: 60, remainingSeconds: 15 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'STARFLEET-SATELLITE-1',
      registration: { name: 'Deep Space 9', factionSymbol: 'STARFLEET', role: 'SATELLITE' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.JUMP_GATE,
        route: baseRoute(waypoints.JUMP_GATE, waypoints.JUMP_GATE, 'DRIFT', ENGINE_SPEED_BY_ROLE.SATELLITE),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SATELLITE },
      modules: [],
      mounts: [],
      cargo: { capacity: 10, units: 0, inventory: [] },
      fuel: { current: 50, capacity: 50 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },
    {
      symbol: 'STARFLEET-REFINERY-1',
      registration: { name: 'Gas Harvester Alpha', factionSymbol: 'STARFLEET', role: 'REFINERY' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.GAS_GIANT,
        route: baseRoute(waypoints.GAS_GIANT, waypoints.GAS_GIANT, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 80,
        units: 45,
        inventory: [{ symbol: 'HYDROCARBON', name: 'Hydrocarbon Gas', description: '', units: 45 }]
      },
      fuel: { current: 65, capacity: 100 },
      agentId: 'AGENT-1',
      agentColor: '#60a5fa',
    },

    // AGENT-2 (TRADERS_GUILD) - 4 ships
    {
      symbol: 'TRADERS-HAULER-1',
      registration: { name: 'Profit Maximus', factionSymbol: 'TRADERS', role: 'HAULER' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.PLANET_MARKET,
        route: baseRoute(waypoints.PLANET_MARKET, waypoints.PLANET_MARKET, 'CRUISE', ENGINE_SPEED_BY_ROLE.HAULER),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 150,
        units: 120,
        inventory: [
          { symbol: 'FOOD', name: 'Food Supplies', description: '', units: 50 },
          { symbol: 'FUEL', name: 'Fuel Cells', description: '', units: 70 },
        ],
      },
      fuel: { current: 100, capacity: 120 },
      agentId: 'AGENT-2',
      agentColor: '#f472b6',
    },
    {
      symbol: 'TRADERS-HAULER-2',
      registration: { name: 'Quick Fortune', factionSymbol: 'TRADERS', role: 'HAULER' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ORBITAL_STATION,
        route: baseRoute(waypoints.ORBITAL_STATION, waypoints.ORBITAL_STATION, 'DRIFT', ENGINE_SPEED_BY_ROLE.HAULER),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER },
      modules: [],
      mounts: [],
      cargo: { capacity: 150, units: 0, inventory: [] },
      fuel: { current: 120, capacity: 120 },
      agentId: 'AGENT-2',
      agentColor: '#f472b6',
    },
    {
      symbol: 'TRADERS-TRANSPORT-1',
      registration: { name: 'Silk Road Express', factionSymbol: 'TRADERS', role: 'TRANSPORT' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ORBITAL_STATION,
        route: baseRoute(waypoints.ORBITAL_STATION, waypoints.PLANET_MARKET, 'BURN', ENGINE_SPEED_BY_ROLE.TRANSPORT),
        status: 'IN_ORBIT',
        flightMode: 'BURN',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.TRANSPORT },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 100,
        units: 85,
        inventory: [{ symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 85 }],
      },
      fuel: { current: 55, capacity: 100 },
      agentId: 'AGENT-2',
      agentColor: '#f472b6',
    },
    {
      symbol: 'TRADERS-EXPLORER-1',
      registration: { name: 'Trade Scout Alpha', factionSymbol: 'TRADERS', role: 'EXPLORER' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.MOON_VOLCANIC,
        route: baseRoute(waypoints.MOON_VOLCANIC, waypoints.MOON_VOLCANIC, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 0, inventory: [] },
      fuel: { current: 80, capacity: 100 },
      agentId: 'AGENT-2',
      agentColor: '#f472b6',
    },

    // AGENT-3 (MINERS_UNION) - 5 ships
    {
      symbol: 'MINERS-EXCAVATOR-1',
      registration: { name: 'Deep Digger', factionSymbol: 'MINERS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ASTEROID_RICH,
        route: baseRoute(waypoints.ASTEROID_FIELD, waypoints.ASTEROID_RICH, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_TRANSIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 100,
        units: 75,
        inventory: [{ symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 75 }],
      },
      fuel: { current: 40, capacity: 80 },
      cooldown: { shipSymbol: 'MINERS-EXCAVATOR-1', totalSeconds: 120, remainingSeconds: 45 },
      agentId: 'AGENT-3',
      agentColor: '#fbbf24',
    },
    {
      symbol: 'MINERS-EXCAVATOR-2',
      registration: { name: 'Rock Crusher', factionSymbol: 'MINERS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ASTEROID_RICH,
        route: baseRoute(waypoints.ASTEROID_RICH, waypoints.ASTEROID_BASE, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_TRANSIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 100,
        units: 95,
        inventory: [{ symbol: 'PRECIOUS_METAL', name: 'Platinum', description: '', units: 95 }],
      },
      fuel: { current: 35, capacity: 80 },
      agentId: 'AGENT-3',
      agentColor: '#fbbf24',
    },
    {
      symbol: 'MINERS-DRONE-1',
      registration: { name: 'AutoMiner-7', factionSymbol: 'MINERS', role: 'MINING_DRONE' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.MOON_ICE,
        route: baseRoute(waypoints.MOON_ICE, waypoints.MOON_ICE, 'DRIFT', ENGINE_SPEED_BY_ROLE.MINING_DRONE),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.MINING_DRONE },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 30,
        units: 18,
        inventory: [{ symbol: 'ICE_WATER', name: 'Water Ice', description: '', units: 18 }],
      },
      fuel: { current: 25, capacity: 40 },
      cooldown: { shipSymbol: 'MINERS-DRONE-1', totalSeconds: 60, remainingSeconds: 10 },
      agentId: 'AGENT-3',
      agentColor: '#fbbf24',
    },
    {
      symbol: 'MINERS-REFINERY-1',
      registration: { name: 'Ore Processor', factionSymbol: 'MINERS', role: 'REFINERY' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.ASTEROID_BASE,
        route: baseRoute(waypoints.ASTEROID_BASE, waypoints.ASTEROID_BASE, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 80,
        units: 60,
        inventory: [
          { symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 30 },
          { symbol: 'IRON', name: 'Refined Iron', description: '', units: 30 },
        ],
      },
      fuel: { current: 70, capacity: 100 },
      agentId: 'AGENT-3',
      agentColor: '#fbbf24',
    },
    {
      symbol: 'MINERS-HAULER-1',
      registration: { name: 'Ore Freighter', factionSymbol: 'MINERS', role: 'HAULER' },
      nav: {
        systemSymbol,
        waypointSymbol: waypoints.PLANET_MARKET,
        route: baseRoute(waypoints.ASTEROID_BASE, waypoints.PLANET_MARKET, 'DRIFT', ENGINE_SPEED_BY_ROLE.HAULER),
        status: 'IN_TRANSIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 150,
        units: 0,
        inventory: [],
      },
      fuel: { current: 90, capacity: 120 },
      agentId: 'AGENT-3',
      agentColor: '#fbbf24',
    },

    // AGENT-4 (GRID-OPS) - Grid system ships (16 ships, one per waypoint)
    {
      symbol: 'GRID-SCOUT-A1',
      registration: { name: 'Grid Scout A1', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_A1,
        route: baseRoute(gridWaypoints.GRID_A1, gridWaypoints.GRID_A1, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 0, inventory: [] },
      fuel: { current: 100, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-MINER-A2',
      registration: { name: 'Grid Miner A2', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_A2,
        route: baseRoute(gridWaypoints.GRID_A2, gridWaypoints.GRID_A2, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 35, inventory: [{ symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 35 }] },
      fuel: { current: 60, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SURVEYOR-A3',
      registration: { name: 'Grid Surveyor A3', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_A3,
        route: baseRoute(gridWaypoints.GRID_A3, gridWaypoints.GRID_A3, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 0, inventory: [] },
      fuel: { current: 50, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-COMMAND-A4',
      registration: { name: 'Grid Command A4', factionSymbol: 'GRID-OPS', role: 'COMMAND' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_A4,
        route: baseRoute(gridWaypoints.GRID_A4, gridWaypoints.GRID_A4, 'DRIFT', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: { capacity: 60, units: 0, inventory: [] },
      fuel: { current: 120, capacity: 150 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXCAVATOR-B1',
      registration: { name: 'Grid Excavator B1', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_B1,
        route: baseRoute(gridWaypoints.GRID_B1, gridWaypoints.GRID_B1, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 40, inventory: [{ symbol: 'COPPER_ORE', name: 'Copper Ore', description: '', units: 40 }] },
      fuel: { current: 55, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-REFINERY-B2',
      registration: { name: 'Grid Refinery B2', factionSymbol: 'GRID-OPS', role: 'REFINERY' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_B2,
        route: baseRoute(gridWaypoints.GRID_B2, gridWaypoints.GRID_B2, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: { capacity: 100, units: 50, inventory: [{ symbol: 'FUEL', name: 'Fuel', description: '', units: 50 }] },
      fuel: { current: 80, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-HARVESTER-B3',
      registration: { name: 'Grid Harvester B3', factionSymbol: 'GRID-OPS', role: 'REFINERY' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_B3,
        route: baseRoute(gridWaypoints.GRID_B3, gridWaypoints.GRID_B3, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: { capacity: 100, units: 60, inventory: [{ symbol: 'HYDROCARBON', name: 'Hydrocarbon', description: '', units: 60 }] },
      fuel: { current: 70, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXPLORER-B4',
      registration: { name: 'Grid Explorer B4', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_B4,
        route: baseRoute(gridWaypoints.GRID_B4, gridWaypoints.GRID_B4, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 0, inventory: [] },
      fuel: { current: 85, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SCOUT-C1',
      registration: { name: 'Grid Scout C1', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_C1,
        route: baseRoute(gridWaypoints.GRID_C1, gridWaypoints.GRID_C1, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 5, inventory: [{ symbol: 'EXOTIC_MATTER', name: 'Exotic Matter', description: '', units: 5 }] },
      fuel: { current: 90, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SALVAGER-C2',
      registration: { name: 'Grid Salvager C2', factionSymbol: 'GRID-OPS', role: 'HAULER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_C2,
        route: baseRoute(gridWaypoints.GRID_C2, gridWaypoints.GRID_C2, 'DRIFT', ENGINE_SPEED_BY_ROLE.HAULER),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER },
      modules: [],
      mounts: [],
      cargo: { capacity: 120, units: 30, inventory: [{ symbol: 'SHIP_PARTS', name: 'Ship Parts', description: '', units: 30 }] },
      fuel: { current: 75, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-MINER-C3',
      registration: { name: 'Grid Miner C3', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_C3,
        route: baseRoute(gridWaypoints.GRID_C3, gridWaypoints.GRID_C3, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 45, inventory: [{ symbol: 'PRECIOUS_METALS', name: 'Precious Metals', description: '', units: 45 }] },
      fuel: { current: 65, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-RESEARCH-C4',
      registration: { name: 'Grid Research C4', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_C4,
        route: baseRoute(gridWaypoints.GRID_C4, gridWaypoints.GRID_C4, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 0, inventory: [] },
      fuel: { current: 45, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SURVEYOR-D1',
      registration: { name: 'Grid Surveyor D1', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_D1,
        route: baseRoute(gridWaypoints.GRID_D1, gridWaypoints.GRID_D1, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'IN_ORBIT',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 10, inventory: [{ symbol: 'ICE_WATER', name: 'Ice Water', description: '', units: 10 }] },
      fuel: { current: 55, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-PATROL-D2',
      registration: { name: 'Grid Patrol D2', factionSymbol: 'GRID-OPS', role: 'PATROL' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_D2,
        route: baseRoute(gridWaypoints.GRID_D2, gridWaypoints.GRID_D2, 'BURN', ENGINE_SPEED_BY_ROLE.PATROL),
        status: 'IN_ORBIT',
        flightMode: 'BURN',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.PATROL },
      modules: [],
      mounts: [],
      cargo: { capacity: 50, units: 0, inventory: [] },
      fuel: { current: 100, capacity: 120 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXCAVATOR-D3',
      registration: { name: 'Grid Excavator D3', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_D3,
        route: baseRoute(gridWaypoints.GRID_D3, gridWaypoints.GRID_D3, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 50, inventory: [{ symbol: 'GOLD_ORE', name: 'Gold Ore', description: '', units: 50 }] },
      fuel: { current: 60, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-COMMAND-D4',
      registration: { name: 'Grid Command D4', factionSymbol: 'GRID-OPS', role: 'COMMAND' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_D4,
        route: baseRoute(gridWaypoints.GRID_D4, gridWaypoints.GRID_D4, 'DRIFT', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'IN_ORBIT',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: { capacity: 60, units: 0, inventory: [] },
      fuel: { current: 130, capacity: 150 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },

    // AGENT-4 (GRID-OPS) - Right grid ships (16 ships for E-H columns) - All docked
    {
      symbol: 'GRID-HARVESTER-E1',
      registration: { name: 'Grid Harvester E1', factionSymbol: 'GRID-OPS', role: 'REFINERY' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_E1,
        route: baseRoute(gridWaypoints.GRID_E1, gridWaypoints.GRID_E1, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: { capacity: 100, units: 55, inventory: [{ symbol: 'HYDROCARBON', name: 'Hydrocarbon', description: '', units: 55 }] },
      fuel: { current: 75, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SURVEYOR-E2',
      registration: { name: 'Grid Surveyor E2', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_E2,
        route: baseRoute(gridWaypoints.GRID_E2, gridWaypoints.GRID_E2, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 8, inventory: [{ symbol: 'QUARTZ_SAND', name: 'Quartz Sand', description: '', units: 8 }] },
      fuel: { current: 52, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXPLORER-E3',
      registration: { name: 'Grid Explorer E3', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_E3,
        route: baseRoute(gridWaypoints.GRID_E3, gridWaypoints.GRID_E3, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 0, inventory: [] },
      fuel: { current: 88, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-MINER-E4',
      registration: { name: 'Grid Miner E4', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_E4,
        route: baseRoute(gridWaypoints.GRID_E4, gridWaypoints.GRID_E4, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 38, inventory: [{ symbol: 'ALUMINUM_ORE', name: 'Aluminum Ore', description: '', units: 38 }] },
      fuel: { current: 58, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-COMMAND-F1',
      registration: { name: 'Grid Command F1', factionSymbol: 'GRID-OPS', role: 'COMMAND' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_F1,
        route: baseRoute(gridWaypoints.GRID_F1, gridWaypoints.GRID_F1, 'DRIFT', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: { capacity: 60, units: 0, inventory: [] },
      fuel: { current: 125, capacity: 150 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXCAVATOR-F2',
      registration: { name: 'Grid Excavator F2', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_F2,
        route: baseRoute(gridWaypoints.GRID_F2, gridWaypoints.GRID_F2, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 42, inventory: [{ symbol: 'COPPER_ORE', name: 'Copper Ore', description: '', units: 42 }] },
      fuel: { current: 62, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-REFINERY-F3',
      registration: { name: 'Grid Refinery F3', factionSymbol: 'GRID-OPS', role: 'REFINERY' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_F3,
        route: baseRoute(gridWaypoints.GRID_F3, gridWaypoints.GRID_F3, 'DRIFT', ENGINE_SPEED_BY_ROLE.REFINERY),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.REFINERY },
      modules: [],
      mounts: [],
      cargo: { capacity: 100, units: 48, inventory: [{ symbol: 'FUEL', name: 'Fuel', description: '', units: 48 }] },
      fuel: { current: 82, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SCOUT-F4',
      registration: { name: 'Grid Scout F4', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_F4,
        route: baseRoute(gridWaypoints.GRID_F4, gridWaypoints.GRID_F4, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 6, inventory: [{ symbol: 'EXOTIC_MATTER', name: 'Exotic Matter', description: '', units: 6 }] },
      fuel: { current: 92, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXPLORER-G1',
      registration: { name: 'Grid Explorer G1', factionSymbol: 'GRID-OPS', role: 'EXPLORER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_G1,
        route: baseRoute(gridWaypoints.GRID_G1, gridWaypoints.GRID_G1, 'CRUISE', ENGINE_SPEED_BY_ROLE.EXPLORER),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXPLORER },
      modules: [],
      mounts: [],
      cargo: { capacity: 40, units: 0, inventory: [] },
      fuel: { current: 95, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-RESEARCH-G2',
      registration: { name: 'Grid Research G2', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_G2,
        route: baseRoute(gridWaypoints.GRID_G2, gridWaypoints.GRID_G2, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 0, inventory: [] },
      fuel: { current: 48, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXCAVATOR-G3',
      registration: { name: 'Grid Excavator G3', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_G3,
        route: baseRoute(gridWaypoints.GRID_G3, gridWaypoints.GRID_G3, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 52, inventory: [{ symbol: 'PLATINUM_ORE', name: 'Platinum Ore', description: '', units: 52 }] },
      fuel: { current: 64, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SALVAGER-G4',
      registration: { name: 'Grid Salvager G4', factionSymbol: 'GRID-OPS', role: 'HAULER' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_G4,
        route: baseRoute(gridWaypoints.GRID_G4, gridWaypoints.GRID_G4, 'DRIFT', ENGINE_SPEED_BY_ROLE.HAULER),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.HAULER },
      modules: [],
      mounts: [],
      cargo: { capacity: 120, units: 25, inventory: [{ symbol: 'SHIP_PARTS', name: 'Ship Parts', description: '', units: 25 }] },
      fuel: { current: 78, capacity: 100 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-EXCAVATOR-H1',
      registration: { name: 'Grid Excavator H1', factionSymbol: 'GRID-OPS', role: 'EXCAVATOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_H1,
        route: baseRoute(gridWaypoints.GRID_H1, gridWaypoints.GRID_H1, 'DRIFT', ENGINE_SPEED_BY_ROLE.EXCAVATOR),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.EXCAVATOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 80, units: 44, inventory: [{ symbol: 'IRON_ORE', name: 'Iron Ore', description: '', units: 44 }] },
      fuel: { current: 56, capacity: 80 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-SURVEYOR-H2',
      registration: { name: 'Grid Surveyor H2', factionSymbol: 'GRID-OPS', role: 'SURVEYOR' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_H2,
        route: baseRoute(gridWaypoints.GRID_H2, gridWaypoints.GRID_H2, 'CRUISE', ENGINE_SPEED_BY_ROLE.SURVEYOR),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.SURVEYOR },
      modules: [],
      mounts: [],
      cargo: { capacity: 20, units: 5, inventory: [{ symbol: 'MOON_REGOLITH', name: 'Moon Regolith', description: '', units: 5 }] },
      fuel: { current: 50, capacity: 60 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-COMMAND-H3',
      registration: { name: 'Grid Command H3', factionSymbol: 'GRID-OPS', role: 'COMMAND' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_H3,
        route: baseRoute(gridWaypoints.GRID_H3, gridWaypoints.GRID_H3, 'DRIFT', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'DOCKED',
        flightMode: 'DRIFT',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: { capacity: 60, units: 0, inventory: [] },
      fuel: { current: 135, capacity: 150 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },
    {
      symbol: 'GRID-PATROL-H4',
      registration: { name: 'Grid Patrol H4', factionSymbol: 'GRID-OPS', role: 'PATROL' },
      nav: {
        systemSymbol: gridSystemSymbol,
        waypointSymbol: gridWaypoints.GRID_H4,
        route: baseRoute(gridWaypoints.GRID_H4, gridWaypoints.GRID_H4, 'BURN', ENGINE_SPEED_BY_ROLE.PATROL),
        status: 'DOCKED',
        flightMode: 'BURN',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.PATROL },
      modules: [],
      mounts: [],
      cargo: { capacity: 50, units: 0, inventory: [] },
      fuel: { current: 105, capacity: 120 },
      agentId: 'AGENT-4',
      agentColor: '#a855f7',
    },

    // AGENT-5 (TWIN_PATROL) - 2 ships cycling between gas giant and planet
    {
      symbol: 'TWIN-PATROL-1',
      registration: { name: 'Twin Patrol 1', factionSymbol: 'TWIN_PATROL', role: 'PATROL' },
      nav: {
        systemSymbol: twinSystemSymbol,
        waypointSymbol: twinWaypoints.GAS_GIANT,
        route: baseRoute(twinWaypoints.GAS_GIANT, twinWaypoints.GAS_GIANT, 'CRUISE', ENGINE_SPEED_BY_ROLE.PATROL),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.PATROL },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 60,
        units: 50,
        inventory: [
          { symbol: 'IRON_ORE', name: 'Iron Ore', description: 'Raw iron ore extracted from asteroids', units: 25 },
          { symbol: 'FUEL', name: 'Fuel', description: 'Refined hydrocarbon fuel', units: 15 },
          { symbol: 'PRECIOUS_STONES', name: 'Precious Stones', description: 'Valuable gemstones', units: 10 },
        ]
      },
      fuel: { current: 100, capacity: 120 },
      agentId: 'AGENT-5',
      agentColor: '#10b981',
    },
    {
      symbol: 'TWIN-PATROL-2',
      registration: { name: 'Twin Patrol 2', factionSymbol: 'TWIN_PATROL', role: 'COMMAND' },
      nav: {
        systemSymbol: twinSystemSymbol,
        waypointSymbol: twinWaypoints.PLANET,
        route: baseRoute(twinWaypoints.PLANET, twinWaypoints.PLANET, 'CRUISE', ENGINE_SPEED_BY_ROLE.COMMAND),
        status: 'DOCKED',
        flightMode: 'CRUISE',
      },
      crew: {},
      frame: {},
      reactor: {},
      engine: { speed: ENGINE_SPEED_BY_ROLE.COMMAND },
      modules: [],
      mounts: [],
      cargo: {
        capacity: 80,
        units: 65,
        inventory: [
          { symbol: 'IRON_ORE', name: 'Iron Ore', description: 'Raw iron ore', units: 40 },
          { symbol: 'FOOD', name: 'Food', description: 'Provisions', units: 15 },
          { symbol: 'ICE_WATER', name: 'Ice Water', description: 'Frozen water', units: 10 },
        ]
      },
      fuel: { current: 100, capacity: 120 },
      agentId: 'AGENT-5',
      agentColor: '#10b981',
    },
  ],
  markets: new Map<string, Market>([
    [
      `${systemSymbol}:${waypoints.PLANET_MARKET}`,
      {
        symbol: waypoints.PLANET_MARKET,
        exports: [
          { symbol: 'FOOD', tradeVolume: 80, supply: 'HIGH', purchasePrice: 35, sellPrice: 45 },
          { symbol: 'CONSUMER_GOODS', tradeVolume: 120, supply: 'ABUNDANT', purchasePrice: 60, sellPrice: 75 },
        ],
        imports: [
          { symbol: 'IRON_ORE', tradeVolume: 100, supply: 'LIMITED', purchasePrice: 120, sellPrice: 150 },
          { symbol: 'PRECIOUS_METAL', tradeVolume: 40, supply: 'SCARCE', purchasePrice: 850, sellPrice: 1100 },
        ],
        exchange: [{ symbol: 'FUEL', tradeVolume: 60, supply: 'MODERATE', purchasePrice: 55, sellPrice: 70 }],
      },
    ],
    [
      `${systemSymbol}:${waypoints.ORBITAL_STATION}`,
      {
        symbol: waypoints.ORBITAL_STATION,
        exports: [
          { symbol: 'SHIP_PARTS', tradeVolume: 50, supply: 'MODERATE', purchasePrice: 200, sellPrice: 250 },
          { symbol: 'ELECTRONICS', tradeVolume: 30, supply: 'LIMITED', purchasePrice: 180, sellPrice: 220 },
        ],
        imports: [
          { symbol: 'IRON', tradeVolume: 70, supply: 'MODERATE', purchasePrice: 180, sellPrice: 210 },
          { symbol: 'FUEL', tradeVolume: 90, supply: 'HIGH', purchasePrice: 45, sellPrice: 60 },
        ],
        exchange: [],
      },
    ],
    [
      `${systemSymbol}:${waypoints.ASTEROID_BASE}`,
      {
        symbol: waypoints.ASTEROID_BASE,
        exports: [
          { symbol: 'IRON_ORE', tradeVolume: 200, supply: 'ABUNDANT', purchasePrice: 25, sellPrice: 35 },
          { symbol: 'PRECIOUS_METAL', tradeVolume: 60, supply: 'MODERATE', purchasePrice: 600, sellPrice: 750 },
        ],
        imports: [
          { symbol: 'FOOD', tradeVolume: 40, supply: 'SCARCE', purchasePrice: 90, sellPrice: 120 },
          { symbol: 'FUEL', tradeVolume: 80, supply: 'LIMITED', purchasePrice: 75, sellPrice: 95 },
        ],
        exchange: [{ symbol: 'EQUIPMENT', tradeVolume: 20, supply: 'LIMITED', purchasePrice: 150, sellPrice: 180 }],
      },
    ],
    [
      `${systemSymbol}:${waypoints.FUEL_STATION}`,
      {
        symbol: waypoints.FUEL_STATION,
        exports: [
          { symbol: 'FUEL', tradeVolume: 300, supply: 'ABUNDANT', purchasePrice: 30, sellPrice: 40 },
        ],
        imports: [
          { symbol: 'ICE', tradeVolume: 100, supply: 'MODERATE', purchasePrice: 50, sellPrice: 65 },
        ],
        exchange: [],
      },
    ],
  ]),
};

// Helper functions for ship state updates
const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));

const findShip = (symbol: string) => mockState.ships.find((s) => s.symbol === symbol);

const setShipCooldown = (ship: MockShip, remainingSeconds: number, totalSeconds = 60) => {
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

const ensureCargoEntry = (ship: MockShip, symbol: string, name: string, units: number) => {
  const existing = ship.cargo.inventory.find(i => i.symbol === symbol);
  if (existing) {
    existing.units = units;
  } else {
    ship.cargo.inventory.push({ symbol, name, description: '', units });
  }
};

// Ship behavior patterns
type ShipBehavior = {
  shipSymbol: string;
  steps: Array<{ apply: () => number }>;
  currentStep: number;
};

const behaviors: ShipBehavior[] = [
  // Explorer: Nebula -> Jump Gate -> Fuel Station -> Nebula (loop)
  {
    shipSymbol: 'STARFLEET-EXPLORER-1',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.JUMP_GATE;
          ship.nav.route = baseRoute(waypoints.NEBULA, waypoints.JUMP_GATE, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 8, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.NEBULA, waypoints.JUMP_GATE, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 6000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.JUMP_GATE;
          setShipCooldown(ship, 0);
          return 6000;
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.FUEL_STATION;
          ship.nav.route = baseRoute(waypoints.JUMP_GATE, waypoints.FUEL_STATION, 'BURN', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'BURN';
          ship.fuel.current = clamp(ship.fuel.current - 10, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.JUMP_GATE, waypoints.FUEL_STATION, 'BURN', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 5000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = waypoints.FUEL_STATION;
          ship.fuel.current = ship.fuel.capacity; // Refuel
          return 5000;
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 2000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.FUEL_STATION;
          return 2000;
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-EXPLORER-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.NEBULA;
          ship.nav.route = baseRoute(waypoints.FUEL_STATION, waypoints.NEBULA, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 7, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.FUEL_STATION, waypoints.NEBULA, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
    ],
  },

  // Patrol: Loop between gravity wells
  {
    shipSymbol: 'STARFLEET-PATROL-1',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          const ship = findShip('STARFLEET-PATROL-1');
          if (!ship) return 6000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.ARTIFICIAL_GRAVITY_WELL;
          return 4000;
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.GRAVITY_WELL;
          ship.nav.route = baseRoute(waypoints.ARTIFICIAL_GRAVITY_WELL, waypoints.GRAVITY_WELL, 'BURN', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'BURN';
          ship.fuel.current = clamp(ship.fuel.current - 12, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.ARTIFICIAL_GRAVITY_WELL, waypoints.GRAVITY_WELL, 'BURN', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-PATROL-1');
          if (!ship) return 4000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.GRAVITY_WELL;
          return 4000;
        },
      },
      {
        apply: () => {
          const ship = findShip('STARFLEET-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.ARTIFICIAL_GRAVITY_WELL;
          ship.nav.route = baseRoute(waypoints.GRAVITY_WELL, waypoints.ARTIFICIAL_GRAVITY_WELL, 'BURN', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'BURN';
          ship.fuel.current = clamp(ship.fuel.current - 12, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.GRAVITY_WELL, waypoints.ARTIFICIAL_GRAVITY_WELL, 'BURN', getEngineSpeedForShip(ship));
        },
      },
    ],
  },

  // Traders hauler: Market -> Asteroid Base -> Market
  {
    shipSymbol: 'TRADERS-HAULER-1',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 5000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          ship.cargo.units = 100;
          ensureCargoEntry(ship, 'IRON_ORE', 'Iron Ore', 100);
          ship.fuel.current = clamp(ship.fuel.current + 10, 0, ship.fuel.capacity);
          return 5000;
        },
      },
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 2000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          return 2000;
        },
      },
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.PLANET_MARKET;
          ship.nav.route = baseRoute(waypoints.ASTEROID_BASE, waypoints.PLANET_MARKET, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 15, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.ASTEROID_BASE, waypoints.PLANET_MARKET, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 5000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = waypoints.PLANET_MARKET;
          ship.cargo.units = 0;
          ship.cargo.inventory = [];
          return 5000;
        },
      },
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 2000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.PLANET_MARKET;
          return 2000;
        },
      },
      {
        apply: () => {
          const ship = findShip('TRADERS-HAULER-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          ship.nav.route = baseRoute(waypoints.PLANET_MARKET, waypoints.ASTEROID_BASE, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 15, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.PLANET_MARKET, waypoints.ASTEROID_BASE, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
    ],
  },

  // Mining drone: orbit and mine asteroid field
  {
    shipSymbol: 'MINERS-DRONE-1',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          const ship = findShip('MINERS-DRONE-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_FIELD;
          ship.cargo.units = clamp(ship.cargo.units + 8, 0, ship.cargo.capacity);
          ensureCargoEntry(ship, 'ORE', 'Raw Ore', ship.cargo.units);
          setShipCooldown(ship, 60, 60);
          ship.fuel.current = clamp(ship.fuel.current - 2, 0, ship.fuel.capacity);
          return 8000;
        },
      },
    ],
  },

  // Excavator: mine -> transit to base -> dock -> back
  {
    shipSymbol: 'MINERS-EXCAVATOR-2',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          const ship = findShip('MINERS-EXCAVATOR-2');
          if (!ship) return 5000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          ship.cargo.units = 0;
          ship.cargo.inventory = [];
          ship.fuel.current = clamp(ship.fuel.current + 15, 0, ship.fuel.capacity);
          return 5000;
        },
      },
      {
        apply: () => {
          const ship = findShip('MINERS-EXCAVATOR-2');
          if (!ship) return 2000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          return 2000;
        },
      },
      {
        apply: () => {
          const ship = findShip('MINERS-EXCAVATOR-2');
          if (!ship) return 6000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_RICH;
          ship.nav.route = baseRoute(waypoints.ASTEROID_BASE, waypoints.ASTEROID_RICH, 'DRIFT', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'DRIFT';
          ship.fuel.current = clamp(ship.fuel.current - 3, 0, ship.fuel.capacity);
          return computeTransitDurationMs(waypoints.ASTEROID_BASE, waypoints.ASTEROID_RICH, 'DRIFT', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          const ship = findShip('MINERS-EXCAVATOR-2');
          if (!ship) return 10000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_RICH;
          ship.cargo.units = clamp(ship.cargo.units + 40, 0, ship.cargo.capacity);
          ensureCargoEntry(ship, 'PRECIOUS_METAL', 'Platinum', ship.cargo.units);
          setShipCooldown(ship, 120, 120);
          ship.fuel.current = clamp(ship.fuel.current - 2, 0, ship.fuel.capacity);
          return 10000;
        },
      },
      {
        apply: () => {
          const ship = findShip('MINERS-EXCAVATOR-2');
          if (!ship) return 6000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = waypoints.ASTEROID_BASE;
          ship.nav.route = baseRoute(waypoints.ASTEROID_RICH, waypoints.ASTEROID_BASE, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 5, 0, ship.fuel.capacity);
          setShipCooldown(ship, 0);
          return computeTransitDurationMs(waypoints.ASTEROID_RICH, waypoints.ASTEROID_BASE, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
    ],
  },
  // TWIN-PATROL-1: Gas Giant <-> Planet loop
  {
    shipSymbol: 'TWIN-PATROL-1',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          // Docked at gas giant (8s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          return 8000;
        },
      },
      {
        apply: () => {
          // Undock -> Orbiting gas giant (20s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          return 20000;
        },
      },
      {
        apply: () => {
          // Cruise to planet
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          ship.nav.route = baseRoute(twinWaypoints.GAS_GIANT, twinWaypoints.PLANET, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 5, 0, ship.fuel.capacity);
          return computeTransitDurationMs(twinWaypoints.GAS_GIANT, twinWaypoints.PLANET, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          // Arrive -> Orbiting planet (20s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          setShipCooldown(ship, 0);
          return 20000;
        },
      },
      {
        apply: () => {
          // Dock at planet (8s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          return 8000;
        },
      },
      {
        apply: () => {
          // Undock -> Orbiting planet (20s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          return 20000;
        },
      },
      {
        apply: () => {
          // Cruise back to gas giant
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          ship.nav.route = baseRoute(twinWaypoints.PLANET, twinWaypoints.GAS_GIANT, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 5, 0, ship.fuel.capacity);
          return computeTransitDurationMs(twinWaypoints.PLANET, twinWaypoints.GAS_GIANT, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          // Arrive -> Orbiting gas giant (20s)
          const ship = findShip('TWIN-PATROL-1');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          setShipCooldown(ship, 0);
          return 20000;
        },
      },
    ],
  },
  // TWIN-PATROL-2: Planet <-> Gas Giant loop (starts at opposite location)
  {
    shipSymbol: 'TWIN-PATROL-2',
    currentStep: 0,
    steps: [
      {
        apply: () => {
          // Docked at planet (8s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 8000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          return 8000;
        },
      },
      {
        apply: () => {
          // Undock -> Orbiting planet (20s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          return 20000;
        },
      },
      {
        apply: () => {
          // Cruise to gas giant
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          ship.nav.route = baseRoute(twinWaypoints.PLANET, twinWaypoints.GAS_GIANT, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 5, 0, ship.fuel.capacity);
          return computeTransitDurationMs(twinWaypoints.PLANET, twinWaypoints.GAS_GIANT, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          // Arrive -> Orbiting gas giant (20s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          setShipCooldown(ship, 0);
          return 20000;
        },
      },
      {
        apply: () => {
          // Dock at gas giant (8s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 8000;
          ship.nav.status = 'DOCKED';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          return 8000;
        },
      },
      {
        apply: () => {
          // Undock -> Orbiting gas giant (20s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.GAS_GIANT;
          return 20000;
        },
      },
      {
        apply: () => {
          // Cruise back to planet
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 8000;
          ship.nav.status = 'IN_TRANSIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          ship.nav.route = baseRoute(twinWaypoints.GAS_GIANT, twinWaypoints.PLANET, 'CRUISE', getEngineSpeedForShip(ship));
          ship.nav.flightMode = 'CRUISE';
          ship.fuel.current = clamp(ship.fuel.current - 5, 0, ship.fuel.capacity);
          return computeTransitDurationMs(twinWaypoints.GAS_GIANT, twinWaypoints.PLANET, 'CRUISE', getEngineSpeedForShip(ship));
        },
      },
      {
        apply: () => {
          // Arrive -> Orbiting planet (20s)
          const ship = findShip('TWIN-PATROL-2');
          if (!ship) return 20000;
          ship.nav.status = 'IN_ORBIT';
          ship.nav.waypointSymbol = twinWaypoints.PLANET;
          setShipCooldown(ship, 0);
          return 20000;
        },
      },
    ],
  },
];

let scenarioRunning = false;
const behaviorTimeouts = new Map<string, ReturnType<typeof setTimeout>>();

const runBehaviorStep = (behavior: ShipBehavior) => {
  if (isTestEnvironment) return;

  const step = behavior.steps[behavior.currentStep];
  const duration = step.apply();
  const waitMs = Math.max(1000, Math.floor(duration));

  const timeout = setTimeout(() => {
    behavior.currentStep = (behavior.currentStep + 1) % behavior.steps.length;
    runBehaviorStep(behavior);
  }, waitMs);

  behaviorTimeouts.set(behavior.shipSymbol, timeout);
};

export const startMockScenarioIfNeeded = () => {
  if (isTestEnvironment) return;
  if (scenarioRunning) return;
  scenarioRunning = true;

  // Start all ship behaviors
  for (const behavior of behaviors) {
    runBehaviorStep(behavior);
  }
};

export const advanceShipScenario = () => {
  if (isTestEnvironment) return;

  // Clear all timeouts
  for (const timeout of behaviorTimeouts.values()) {
    clearTimeout(timeout);
  }
  behaviorTimeouts.clear();

  if (!scenarioRunning) {
    scenarioRunning = true;
  }

  // Advance all behaviors by one step
  for (const behavior of behaviors) {
    behavior.currentStep = (behavior.currentStep + 1) % behavior.steps.length;
    runBehaviorStep(behavior);
  }
};
