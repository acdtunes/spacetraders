import { hashString } from './hash';

// Single source of truth for sprite-asset selection, extracted from SpaceMap so
// the Contract Ops scene renders the same visual language as the system map.

const WAYPOINT_ASSET_BASE_PATH = '/assets/waypoints/';
const SHIP_ASSET_BASE_PATH = '/assets/ships/';

const WAYPOINT_ASSET_VARIANTS: Record<string, string[]> = {
  asteroid: ['waypoint-asteroid-1.png', 'waypoint-asteroid-2.png'],
  asteroidBase: ['waypoint-asteroid-base-1.png', 'waypoint-asteroid-base-2.png'],
  engineeredAsteroid: ['waypoint-engineered-asteroid-2.png'],
  orbitalStation: ['waypoint-orbital-station-1.png'],
  frozenMoon: ['waypoint-frozen-moon-1.png', 'waypoint-frozen-moon-2.png'],
  planetTemperate: ['waypoint-planet-temperate-1.png', 'waypoint-planet-temperate-2.png'],
  planetOcean: ['waypoint-planet-ocean-1.png', 'waypoint-planet-ocean-2.png'],
  planetFrozen: ['waypoint-planet-frozen-1.png', 'waypoint-planet-frozen-2.png'],
  planetRocky: ['waypoint-planet-rocky-1.png', 'waypoint-planet-rocky-2.png'],
  planetVolcanic: ['waypoint-planet-volcanic-1.png', 'waypoint-planet-volcanic-2.png'],
  planetRadioactive: [
    'waypoint-planet-radioactive-1.png',
    'waypoint-planet-radioactive-2.png',
    'waypoint-planet-radioactive-3.png',
    'waypoint-planet-radioactive-4.png',
  ],
  planetSwamp: ['waypoint-planet-swamp-2.png'],
  planetJovian: ['waypoint-planet-jovian-1.png', 'waypoint-planet-jovian-2.png'],
  fuelStation: ['waypoint-fuel-station-1.png', 'waypoint-fuel-station-2.png'],
  volcanicMoon: ['waypoint-volcanic-moon-1.png', 'waypoint-volcanic-moon-2.png'],
  jumpGate: ['waytpoint-jumpgate.png'],
};

const SHIP_ASSET_VARIANTS: Record<string, string[]> = {
  command: ['ship-command-frigate-2.png'],
  hauler: ['ship-light-hauler-1.png'],
  mining: ['ship-mining-drone-1.png', 'ship-mining-drone-2.png'],
  probe: ['ship-probe-2.png'],
  satellite: ['ship-satellite-1.png', 'ship-satellite-2.png'],
  station: ['ship-space-station-1.png'],
};

const DEFAULT_WAYPOINT_ASSET = 'waypoint-planet-rocky-1.png';
const DEFAULT_SHIP_ASSET = 'ship-command-frigate-2.png';

type TraitLike = string | { symbol: string };

const traitSymbols = (traits: ReadonlyArray<TraitLike>): string[] =>
  traits.map((t) => (typeof t === 'string' ? t : t.symbol).toUpperCase());

export function selectWaypointAsset(
  symbol: string,
  type: string,
  traits: ReadonlyArray<TraitLike> = [],
): string {
  const symbols = traitSymbols(traits);
  const hasTrait = (...keywords: string[]) =>
    symbols.some((trait) => keywords.some((keyword) => trait.includes(keyword)));

  let variantKey: string;
  if (type === 'JUMP_GATE') {
    variantKey = 'jumpGate';
  } else if (type === 'ASTEROID' || type === 'ASTEROID_FIELD') {
    variantKey = 'asteroid';
  } else if (type === 'ASTEROID_BASE') {
    variantKey = 'asteroidBase';
  } else if (type === 'ENGINEERED_ASTEROID') {
    variantKey = 'engineeredAsteroid';
  } else if (type === 'GAS_GIANT' || hasTrait('GAS_GIANT') || hasTrait('JOVIAN')) {
    variantKey = 'planetJovian';
  } else if (hasTrait('OCEAN', 'WATER')) {
    variantKey = 'planetOcean';
  } else if (hasTrait('TEMPERATE', 'TROPICAL', 'FOREST')) {
    variantKey = 'planetTemperate';
  } else if (hasTrait('FROZEN', 'ICE')) {
    variantKey = type === 'MOON' ? 'frozenMoon' : 'planetFrozen';
  } else if (hasTrait('VOLCANIC', 'INFERNO')) {
    variantKey = type === 'MOON' ? 'volcanicMoon' : 'planetVolcanic';
  } else if (hasTrait('RADIOACTIVE', 'NUCLEAR')) {
    variantKey = 'planetRadioactive';
  } else if (type === 'ORBITAL_STATION' || hasTrait('ORBITAL')) {
    variantKey = 'orbitalStation';
  } else if (type.includes('STATION')) {
    variantKey = 'fuelStation';
  } else if (hasTrait('SWAMP', 'JUNGLE', 'BOG')) {
    variantKey = 'planetSwamp';
  } else if (type === 'FUEL_STATION' || hasTrait('FUEL')) {
    variantKey = 'fuelStation';
  } else {
    variantKey = 'planetRocky';
  }

  const variants = WAYPOINT_ASSET_VARIANTS[variantKey] ?? WAYPOINT_ASSET_VARIANTS.planetRocky;
  const assetIndex = variants.length > 0 ? hashString(`${symbol}:${variantKey}`) % variants.length : 0;
  return `${WAYPOINT_ASSET_BASE_PATH}${variants[assetIndex] ?? DEFAULT_WAYPOINT_ASSET}`;
}

export function selectShipAssetByRole(symbol: string, registrationRole: string | undefined): string {
  const role = registrationRole?.toLowerCase() ?? '';

  let variantKey: string;
  if (role.includes('satellite')) {
    variantKey = 'satellite';
  } else if (role.includes('station') || role.includes('platform')) {
    variantKey = 'station';
  } else if (role.includes('probe') || role.includes('scout') || role.includes('explorer')) {
    variantKey = 'probe';
  } else if (
    role.includes('mine') ||
    role.includes('extract') ||
    role.includes('drone') ||
    role.includes('excavator') ||
    role.includes('miner')
  ) {
    variantKey = 'mining';
  } else if (role.includes('haul') || role.includes('freight') || role.includes('cargo') || role.includes('transport')) {
    variantKey = 'hauler';
  } else {
    variantKey = 'command';
  }

  const variants = SHIP_ASSET_VARIANTS[variantKey];
  if (!variants || variants.length === 0) return `${SHIP_ASSET_BASE_PATH}${DEFAULT_SHIP_ASSET}`;
  return `${SHIP_ASSET_BASE_PATH}${variants[hashString(`${symbol}:${variantKey}`) % variants.length] ?? DEFAULT_SHIP_ASSET}`;
}

// World-unit sprite radii for the Contract Ops scene backdrop (its plane is
// per-system waypoint coordinates, roughly ±100).
export function waypointVisualRadius(type: string): number {
  switch (type) {
    case 'GAS_GIANT': return 5;
    case 'PLANET': return 3.4;
    case 'JUMP_GATE': return 3;
    case 'ORBITAL_STATION': return 2.4;
    case 'ASTEROID_BASE': return 2.2;
    case 'ENGINEERED_ASTEROID': return 2;
    case 'FUEL_STATION': return 2;
    case 'MOON': return 1.8;
    case 'ASTEROID':
    case 'ASTEROID_FIELD': return 1.3;
    default: return 2;
  }
}
