/**
 * Domain layer barrel exports
 * Centralizes all domain logic for easy imports
 */

export { Ship } from './ship';
export type { Position, ShipPositionOptions } from './ship';

export { Waypoint } from './waypoint';

export { ShipQueries, WaypointQueries } from './shipQueries';

export { ViewportBounds } from './viewport';
export type { MinimapData } from './viewport';
