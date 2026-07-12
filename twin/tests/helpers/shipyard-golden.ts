// twin/tests/helpers/shipyard-golden.ts
//
// The EXPECTED spec-complete GET .../shipyard response, derived from the reduced capture fixture by
// the SAME enrichment the real SpaceTraders 2.3.0 spec requires (gobot/api/openapi.json ShipyardShip):
// every listing gains symbol/supply/crew and the deep condition/integrity/quality/description +
// normalized requirements on frame/reactor/engine. It is HAND-WRITTEN from the spec here and NEVER
// imports the production serializer, so it is a genuine golden — a drift between the endpoint output
// and the spec shape surfaces as a toEqual failure rather than passing silently.
//
// The INDEPENDENT proof that this shape actually conforms to the vendored spec is the OpenAPI
// conformance sweep (tests/openapi/shape.test.ts, the "HAS TEETH" block validating captured responses
// against gobot/api/openapi.json); this golden is the exact field-for-field snapshot the endpoint and
// CLI round-trip tests diff against.

type Dict = Record<string, unknown>;

interface Reqs { power: number; crew: number; slots: number }
function reqs(r: unknown): Reqs {
  const o = (r ?? {}) as Partial<Reqs>;
  return { power: o.power ?? 0, crew: o.crew ?? 0, slots: o.slots ?? 0 };
}

/** "FRAME_PROBE" -> "Frame Probe" — matches src/world/serialize.ts humanize exactly. */
function humanize(symbol: string): string {
  return symbol
    .split('_')
    .map((w) => (w.length ? w[0].toUpperCase() + w.slice(1).toLowerCase() : w))
    .join(' ');
}

/** value when it is a number (0 kept), else the fallback — mirrors the serializer's `?? default`. */
function num(value: unknown, fallback: number): number {
  return typeof value === 'number' ? value : fallback;
}

function str(value: unknown, fallback: string): string {
  return typeof value === 'string' ? value : fallback;
}

function enrichFrame(f: Dict): Dict {
  const symbol = str(f.symbol, 'FRAME_UNKNOWN');
  return {
    symbol,
    name: str(f.name, humanize(symbol)),
    description: str(f.description, humanize(symbol)),
    condition: num(f.condition, 1),
    integrity: num(f.integrity, 1),
    moduleSlots: num(f.moduleSlots, 0),
    mountingPoints: num(f.mountingPoints, 0),
    fuelCapacity: num(f.fuelCapacity, 0),
    requirements: reqs(f.requirements),
    quality: num(f.quality, 1),
  };
}

function enrichReactor(r: Dict): Dict {
  const symbol = str(r.symbol, 'REACTOR_UNKNOWN');
  return {
    symbol,
    name: str(r.name, humanize(symbol)),
    description: str(r.description, humanize(symbol)),
    condition: num(r.condition, 1),
    integrity: num(r.integrity, 1),
    powerOutput: num(r.powerOutput, 1),
    requirements: reqs(r.requirements),
    quality: num(r.quality, 1),
  };
}

function enrichEngine(e: Dict): Dict {
  const symbol = str(e.symbol, 'ENGINE_IMPULSE_DRIVE_I');
  return {
    symbol,
    name: str(e.name, humanize(symbol)),
    description: str(e.description, humanize(symbol)),
    condition: num(e.condition, 1),
    integrity: num(e.integrity, 1),
    speed: num(e.speed, 1),
    requirements: reqs(e.requirements),
    quality: num(e.quality, 1),
  };
}

function enrichModule(m: Dict): Dict {
  const symbol = str(m.symbol, 'MODULE_UNKNOWN');
  return { ...m, symbol, name: str(m.name, humanize(symbol)), description: str(m.description, humanize(symbol)), requirements: reqs(m.requirements) };
}

function enrichMount(m: Dict): Dict {
  const symbol = str(m.symbol, 'MOUNT_UNKNOWN');
  return { ...m, symbol, name: str(m.name, humanize(symbol)), requirements: reqs(m.requirements) };
}

/** The full spec-complete GET .../shipyard `data` the twin emits for the given reduced fixture
 *  shipyard. `activity` is intentionally absent: the serializer leaves it undefined for these
 *  fixtures and JSON omits it, so the wire shape carries no `activity` key. */
export function expectedShipyardResponse(rawInput: unknown): Dict {
  const raw = rawInput as Dict;
  const ships = ((raw.ships as Dict[]) ?? []).map((listing): Dict => {
    const type = str(listing.type, '');
    const crew = (listing.crew ?? {}) as Dict;
    return {
      type,
      symbol: str(listing.symbol, type),
      name: str(listing.name, humanize(type)),
      description: str(listing.description, humanize(type)),
      supply: str(listing.supply, 'MODERATE'),
      purchasePrice: num(listing.purchasePrice, 0),
      frame: enrichFrame((listing.frame ?? {}) as Dict),
      reactor: enrichReactor((listing.reactor ?? {}) as Dict),
      engine: enrichEngine((listing.engine ?? {}) as Dict),
      modules: ((listing.modules as Dict[]) ?? []).map(enrichModule),
      mounts: ((listing.mounts as Dict[]) ?? []).map(enrichMount),
      crew: { required: num(crew.required, 0), capacity: num(crew.capacity, 0) },
    };
  });
  return {
    symbol: raw.symbol,
    shipTypes: raw.shipTypes,
    modificationsFee: raw.modificationsFee,
    transactions: (raw.transactions as unknown[]) ?? [],
    ships,
  };
}
