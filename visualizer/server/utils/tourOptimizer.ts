/**
 * Simple TSP solver for optimizing market scout tours
 * Uses 2-opt local search algorithm
 */

interface Waypoint {
  symbol: string;
  x: number;
  y: number;
}

/**
 * Calculate Euclidean distance between two waypoints
 */
function distance(a: Waypoint, b: Waypoint): number {
  const dx = a.x - b.x;
  const dy = a.y - b.y;
  return Math.sqrt(dx * dx + dy * dy);
}

/**
 * Calculate total tour distance
 */
function tourDistance(tour: Waypoint[]): number {
  let total = 0;
  for (let i = 0; i < tour.length - 1; i++) {
    total += distance(tour[i], tour[i + 1]);
  }
  return total;
}

/**
 * 2-opt improvement: try reversing segments to find better tour
 */
function twoOpt(tour: Waypoint[]): Waypoint[] {
  let improved = true;
  let best = [...tour];

  while (improved) {
    improved = false;

    for (let i = 1; i < best.length - 2; i++) {
      for (let j = i + 1; j < best.length; j++) {
        // Try reversing segment [i, j]
        const newTour = [...best.slice(0, i), ...best.slice(i, j).reverse(), ...best.slice(j)];

        if (tourDistance(newTour) < tourDistance(best)) {
          best = newTour;
          improved = true;
        }
      }
    }
  }

  return best;
}

/**
 * Nearest neighbor heuristic for initial tour
 */
function nearestNeighbor(waypoints: Waypoint[], start: Waypoint): Waypoint[] {
  const tour: Waypoint[] = [start];
  const remaining = waypoints.filter(wp => wp.symbol !== start.symbol);

  while (remaining.length > 0) {
    const current = tour[tour.length - 1];

    // Find nearest unvisited waypoint
    let nearest = remaining[0];
    let minDist = distance(current, nearest);

    for (let i = 1; i < remaining.length; i++) {
      const dist = distance(current, remaining[i]);
      if (dist < minDist) {
        minDist = dist;
        nearest = remaining[i];
      }
    }

    tour.push(nearest);
    const idx = remaining.indexOf(nearest);
    remaining.splice(idx, 1);
  }

  return tour;
}

/**
 * Optimize tour using nearest neighbor + 2-opt
 */
export function optimizeTour(waypoints: Waypoint[], startSymbol: string): {
  tourOrder: string[];
  totalDistance: number;
} {
  if (waypoints.length === 0) {
    return { tourOrder: [], totalDistance: 0 };
  }

  if (waypoints.length === 1) {
    return { tourOrder: [waypoints[0].symbol], totalDistance: 0 };
  }

  // Find start waypoint
  const start = waypoints.find(wp => wp.symbol === startSymbol) || waypoints[0];

  // Build initial tour with nearest neighbor
  const initialTour = nearestNeighbor(waypoints, start);

  // Improve with 2-opt
  const optimizedTour = twoOpt(initialTour);

  return {
    tourOrder: optimizedTour.map(wp => wp.symbol),
    totalDistance: Math.round(tourDistance(optimizedTour) * 100) / 100,
  };
}
