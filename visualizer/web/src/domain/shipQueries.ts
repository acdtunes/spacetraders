import type { Ship as ShipType, Waypoint as WaypointType } from '../types/spacetraders';
import { Ship } from './ship';

type ShipWithAgent = ShipType & { agentId?: string };

/**
 * Ship query and filter operations
 */
export const ShipQueries = {
  /**
   * Filter ships by navigation status
   */
  filterByStatus(ships: ShipType[], statuses: Set<string>): ShipType[] {
    return ships.filter(ship => statuses.has(ship.nav.status));
  },

  /**
   * Filter ships by agent ID (exclude hidden agents)
   */
  filterByAgent(ships: ShipWithAgent[], hiddenAgentIds: Set<string>): ShipType[] {
    return ships.filter(ship => !ship.agentId || !hiddenAgentIds.has(ship.agentId));
  },

  /**
   * Filter ships in a specific system
   */
  filterBySystem(ships: ShipType[], systemSymbol: string): ShipType[] {
    return ships.filter(ship => ship.nav.systemSymbol === systemSymbol);
  },

  /**
   * Filter ships by multiple criteria
   */
  filter(
    ships: ShipWithAgent[],
    options: {
      systemSymbol?: string;
      statuses?: Set<string>;
      hiddenAgentIds?: Set<string>;
    }
  ): ShipType[] {
    let filtered = ships;

    if (options.systemSymbol) {
      filtered = this.filterBySystem(filtered, options.systemSymbol);
    }

    if (options.statuses) {
      filtered = this.filterByStatus(filtered, options.statuses);
    }

    if (options.hiddenAgentIds) {
      filtered = this.filterByAgent(filtered, options.hiddenAgentIds);
    }

    return filtered;
  },

  /**
   * Group ships by system
   */
  groupBySystem(ships: ShipType[]): Map<string, ShipType[]> {
    return ships.reduce((acc, ship) => {
      const system = ship.nav.systemSymbol;
      if (!acc.has(system)) acc.set(system, []);
      acc.get(system)!.push(ship);
      return acc;
    }, new Map<string, ShipType[]>());
  },

  /**
   * Group ships by navigation status
   */
  groupByStatus(ships: ShipType[]): Map<string, ShipType[]> {
    return ships.reduce((acc, ship) => {
      const status = ship.nav.status;
      if (!acc.has(status)) acc.set(status, []);
      acc.get(status)!.push(ship);
      return acc;
    }, new Map<string, ShipType[]>());
  },

  /**
   * Group ships by waypoint (current location)
   */
  groupByWaypoint(ships: ShipType[]): Map<string, ShipType[]> {
    return ships.reduce((acc, ship) => {
      const waypoint = ship.nav.waypointSymbol;
      if (!acc.has(waypoint)) acc.set(waypoint, []);
      acc.get(waypoint)!.push(ship);
      return acc;
    }, new Map<string, ShipType[]>());
  },

  /**
   * Get ships at a specific waypoint
   */
  atWaypoint(ships: ShipType[], waypointSymbol: string): ShipType[] {
    return ships.filter(ship => ship.nav.waypointSymbol === waypointSymbol);
  },

  /**
   * Get ships currently in transit
   */
  inTransit(ships: ShipType[]): ShipType[] {
    return ships.filter(ship => Ship.isInTransit(ship));
  },

  /**
   * Get ships in orbit
   */
  inOrbit(ships: ShipType[]): ShipType[] {
    return ships.filter(ship => Ship.isInOrbit(ship));
  },

  /**
   * Get docked ships
   */
  docked(ships: ShipType[]): ShipType[] {
    return ships.filter(ship => Ship.isDocked(ship));
  },

  /**
   * Find ship by symbol
   */
  findBySymbol(ships: ShipType[], symbol: string): ShipType | undefined {
    return ships.find(ship => ship.symbol === symbol);
  },

  /**
   * Count ships by status
   */
  countByStatus(ships: ShipType[]): Map<string, number> {
    return ships.reduce((acc, ship) => {
      const status = ship.nav.status;
      acc.set(status, (acc.get(status) || 0) + 1);
      return acc;
    }, new Map<string, number>());
  },

  /**
   * Count ships by system
   */
  countBySystem(ships: ShipType[]): Map<string, number> {
    return ships.reduce((acc, ship) => {
      const system = ship.nav.systemSymbol;
      acc.set(system, (acc.get(system) || 0) + 1);
      return acc;
    }, new Map<string, number>());
  },
};

/**
 * Waypoint query operations
 */
export const WaypointQueries = {
  /**
   * Filter waypoints by type
   */
  filterByType(waypoints: WaypointType[], types: Set<string>): WaypointType[] {
    return waypoints.filter(waypoint => types.has(waypoint.type));
  },

  /**
   * Filter waypoints by trait
   */
  filterByTrait(waypoints: WaypointType[], traitSymbol: string): WaypointType[] {
    return waypoints.filter(waypoint =>
      waypoint.traits?.some(trait => trait.symbol === traitSymbol)
    );
  },

  /**
   * Get marketplaces
   */
  marketplaces(waypoints: WaypointType[]): WaypointType[] {
    return this.filterByTrait(waypoints, 'MARKETPLACE');
  },

  /**
   * Get shipyards
   */
  shipyards(waypoints: WaypointType[]): WaypointType[] {
    return this.filterByTrait(waypoints, 'SHIPYARD');
  },

  /**
   * Find waypoint by symbol
   */
  findBySymbol(waypoints: WaypointType[], symbol: string): WaypointType | undefined {
    return waypoints.find(waypoint => waypoint.symbol === symbol);
  },

  /**
   * Get waypoints in a bounding box
   */
  inBounds(
    waypoints: WaypointType[],
    bounds: { minX: number; maxX: number; minY: number; maxY: number }
  ): WaypointType[] {
    return waypoints.filter(
      waypoint =>
        waypoint.x >= bounds.minX &&
        waypoint.x <= bounds.maxX &&
        waypoint.y >= bounds.minY &&
        waypoint.y <= bounds.maxY
    );
  },

  /**
   * Get waypoints within a radius of a point
   */
  withinRadius(waypoints: WaypointType[], center: { x: number; y: number }, radius: number): WaypointType[] {
    return waypoints.filter(waypoint => {
      const dx = waypoint.x - center.x;
      const dy = waypoint.y - center.y;
      const distance = Math.sqrt(dx * dx + dy * dy);
      return distance <= radius;
    });
  },

  /**
   * Calculate bounds of all waypoints
   */
  calculateBounds(waypoints: WaypointType[]): {
    minX: number;
    maxX: number;
    minY: number;
    maxY: number;
  } {
    if (waypoints.length === 0) {
      return { minX: 0, maxX: 0, minY: 0, maxY: 0 };
    }

    let minX = Infinity;
    let maxX = -Infinity;
    let minY = Infinity;
    let maxY = -Infinity;

    waypoints.forEach(wp => {
      minX = Math.min(minX, wp.x);
      maxX = Math.max(maxX, wp.x);
      minY = Math.min(minY, wp.y);
      maxY = Math.max(maxY, wp.y);
    });

    return { minX, maxX, minY, maxY };
  },
};
