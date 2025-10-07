import type { Market, TradeOpportunity } from '../types/spacetraders';

const PROFIT_THRESHOLD = 100; // Minimum profit per unit to be considered an opportunity

/**
 * Calculate trade opportunities from available market data.
 * Finds profitable goods where one market exports (low buy price) and another imports (high sell price).
 */
export function calculateOpportunities(markets: Map<string, Market>): TradeOpportunity[] {
  const opportunities: TradeOpportunity[] = [];

  // For each export market
  for (const [exportWp, exportMarket] of markets) {
    for (const exportGood of exportMarket.exports) {
      // Find import markets for the same good
      for (const [importWp, importMarket] of markets) {
        if (exportWp === importWp) continue; // Skip same waypoint

        const importGood = importMarket.imports.find((g) => g.symbol === exportGood.symbol);

        if (importGood) {
          const profit = importGood.sellPrice - exportGood.purchasePrice;

          if (profit > PROFIT_THRESHOLD) {
            opportunities.push({
              good: exportGood.symbol,
              profitPerUnit: profit,
              buyLocation: exportWp,
              sellLocation: importWp,
            });
          }
        }
      }
    }
  }

  // Sort by profit descending
  return opportunities.sort((a, b) => b.profitPerUnit - a.profitPerUnit);
}

/**
 * Get top N trade opportunities for a specific waypoint (either as buyer or seller).
 */
export function getWaypointOpportunities(
  waypointSymbol: string,
  markets: Map<string, Market>,
  limit: number = 2
): TradeOpportunity[] {
  const allOpportunities = calculateOpportunities(markets);

  // Filter opportunities where this waypoint is either buy or sell location
  const relevant = allOpportunities.filter(
    (op) => op.buyLocation === waypointSymbol || op.sellLocation === waypointSymbol
  );

  return relevant.slice(0, limit);
}

/**
 * Format trade opportunity for display.
 */
export function formatOpportunity(opportunity: TradeOpportunity): string {
  return `${opportunity.good}: +${opportunity.profitPerUnit} cr/unit`;
}
