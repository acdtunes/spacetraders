import type { Waypoint as WaypointType } from '../types/spacetraders';

/**
 * Waypoint domain logic - encapsulates all waypoint-related business rules
 */
export const Waypoint = {
  /**
   * Get waypoint radius based on type
   * Varies by type and uses position-based hashing for diversity
   */
  getRadius(waypoint: WaypointType): number {
    if (waypoint.type.includes('PLANET')) {
      // Vary planet size based on position hash for diversity
      const hash = (waypoint.x * 73856093) ^ (waypoint.y * 19349663);
      const sizeVariation = (Math.abs(hash) % 6) + 1; // 1 to 6
      return 5 + sizeVariation; // 6 to 11
    }

    if (waypoint.type.includes('GAS_GIANT')) {
      // Gas giants also vary in size
      const hash = (waypoint.x * 73856093) ^ (waypoint.y * 19349663);
      const sizeVariation = (Math.abs(hash) % 5) + 1; // 1 to 5
      return 10 + sizeVariation; // 11 to 15
    }

    if (waypoint.type === 'MOON') {
      // Moons vary in size but remain small; widen variation range.
      const hash = (waypoint.x * 73856093) ^ (waypoint.y * 19349663);
      const sizeVariation = (Math.abs(hash) % 5) / 4; // 0 to 1 step of 0.25
      const baseSize = 0.3 + sizeVariation * 0.4; // 0.3 to 0.7
      return baseSize; // ≈0.3 to 0.7
    }

    if (waypoint.type === 'ORBITAL_STATION') {
      return 2.5;
    }

    if (waypoint.type === 'ENGINEERED_ASTEROID') {
      return 0.75;
    }

    if (waypoint.type.includes('STATION')) {
      return 5;
    }

    return 3;
  },

  /**
   * Get waypoint display name
   */
  getDisplayName(waypoint: WaypointType): string {
    return waypoint.symbol.split('-').pop() || waypoint.symbol;
  },

  /**
   * Check if waypoint has a trait
   */
  hasTrait(waypoint: WaypointType, traitSymbol: string): boolean {
    return waypoint.traits?.some(trait => trait.symbol === traitSymbol) ?? false;
  },

  /**
   * Check if waypoint is a marketplace
   */
  isMarketplace(waypoint: WaypointType): boolean {
    return this.hasTrait(waypoint, 'MARKETPLACE');
  },

  /**
   * Check if waypoint is a shipyard
   */
  isShipyard(waypoint: WaypointType): boolean {
    return this.hasTrait(waypoint, 'SHIPYARD');
  },

  /**
   * Check if waypoint is uncharted
   */
  isUncharted(waypoint: WaypointType): boolean {
    return this.hasTrait(waypoint, 'UNCHARTED');
  },

  /**
   * Get all waypoint traits as a formatted string
   */
  getTraitsText(waypoint: WaypointType): string {
    if (!waypoint.traits || waypoint.traits.length === 0) {
      return 'No traits';
    }
    return waypoint.traits.map(t => t.name).join(', ');
  },
};
