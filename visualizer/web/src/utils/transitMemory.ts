// Client-side origin memory for contract-ops ship animation (sp-c6pm).
//
// The daemon persists only the DESTINATION (+arrival time) for IN_TRANSIT
// ships — origin and departure are deliberately dropped (see gobot
// ship_dto.go; engine bead sp-vp9k tracks persisting them). So the browser
// reconstructs flight paths itself: it remembers each ship's last stationary
// waypoint, and the poll at which a ship flips stationary→transit marks the
// departure (±one poll interval). Every flight observed from its start then
// animates along its true path; ships already mid-flight at page load render
// as 'inbound' at their destination until they next go stationary.

export type NavStatus = 'DOCKED' | 'IN_ORBIT' | 'IN_TRANSIT';

export interface OpsShipSnapshot {
  symbol: string;
  navStatus: NavStatus;
  waypoint: string; // for IN_TRANSIT this is the DESTINATION
  x: number;
  y: number;
  arrivalTime: string | null;
}

export interface ShipMemory {
  lastStationary?: { waypoint: string; x: number; y: number };
  transit?: { originWaypoint: string; originX: number; originY: number; departedAtMs: number };
}

export function updateTransitMemory(
  prev: ReadonlyMap<string, ShipMemory>,
  ships: OpsShipSnapshot[],
  nowMs: number,
): Map<string, ShipMemory> {
  const next = new Map(prev);
  for (const ship of ships) {
    const mem = next.get(ship.symbol) ?? {};
    if (ship.navStatus === 'IN_TRANSIT') {
      if (!mem.transit && mem.lastStationary && mem.lastStationary.waypoint !== ship.waypoint) {
        next.set(ship.symbol, {
          ...mem,
          transit: {
            originWaypoint: mem.lastStationary.waypoint,
            originX: mem.lastStationary.x,
            originY: mem.lastStationary.y,
            departedAtMs: nowMs,
          },
        });
      }
      // Already-known transit keeps its original departure; a cold start
      // (no lastStationary, or destination === last stationary waypoint,
      // i.e. a round trip whose outbound leg we never saw) stays memoryless.
    } else {
      next.set(ship.symbol, {
        lastStationary: { waypoint: ship.waypoint, x: ship.x, y: ship.y },
      });
    }
  }
  return next;
}

export type PositionMode = 'stationary' | 'exact' | 'inbound';

export interface ShipPosition {
  x: number;
  y: number;
  mode: PositionMode;
  progress: number | null;
  headingRad: number | null;
}

export function positionShip(
  ship: OpsShipSnapshot,
  memory: ShipMemory | undefined,
  nowMs: number,
): ShipPosition {
  if (ship.navStatus !== 'IN_TRANSIT') {
    return { x: ship.x, y: ship.y, mode: 'stationary', progress: null, headingRad: null };
  }
  const transit = memory?.transit;
  const arrivalMs = ship.arrivalTime ? Date.parse(ship.arrivalTime) : NaN;
  if (!transit || !Number.isFinite(arrivalMs)) {
    return { x: ship.x, y: ship.y, mode: 'inbound', progress: null, headingRad: null };
  }
  const span = arrivalMs - transit.departedAtMs;
  const raw = span > 0 ? (nowMs - transit.departedAtMs) / span : 1;
  const t = Math.min(1, Math.max(0, raw));
  const dx = ship.x - transit.originX;
  const dy = ship.y - transit.originY;
  return {
    x: transit.originX + dx * t,
    y: transit.originY + dy * t,
    mode: 'exact',
    progress: t,
    headingRad: dx === 0 && dy === 0 ? null : Math.atan2(dy, dx),
  };
}
