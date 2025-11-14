import type { ScoutTour } from '../types/spacetraders';

/**
 * Generate a unique identifier for a scout tour
 * Uses daemon_id which is guaranteed unique per scout daemon
 */
export function getTourId(tour: ScoutTour): string {
  return tour.daemon_id;
}

/**
 * Get a display label for a tour
 * Shows the start waypoint's short name
 */
export function getTourLabel(tour: ScoutTour): string {
  const startWaypoint = tour.start_waypoint || tour.tour_order[0];
  // Extract just the waypoint identifier (e.g., "A1" from "X1-HU87-A1")
  const parts = startWaypoint.split('-');
  return parts[parts.length - 1] || startWaypoint;
}
