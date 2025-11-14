import type { Market } from '../types/spacetraders';

interface MarketOpportunityConfig {
  profitThreshold: number;
}

export interface TradeOpportunity {
  good: string;
  profitPerUnit: number;
  buyLocation: string;
  sellLocation: string;
}

export class MarketOpportunityAnalyzer {
  constructor(private readonly config: MarketOpportunityConfig) {}

  findProfitableRoutes(markets: Map<string, Market>): TradeOpportunity[] {
    const opportunities: TradeOpportunity[] = [];

    markets.forEach((exportMarket, exportSymbol) => {
      exportMarket.exports.forEach((exportGood) => {
        markets.forEach((importMarket, importSymbol) => {
          if (exportSymbol === importSymbol) {
            return;
          }

          const importGood = importMarket.imports.find((good) => good.symbol === exportGood.symbol);
          if (!importGood) {
            return;
          }

          const profit = importGood.sellPrice - exportGood.purchasePrice;
          if (profit <= this.config.profitThreshold) {
            return;
          }

          opportunities.push({
            good: exportGood.symbol,
            profitPerUnit: profit,
            buyLocation: exportSymbol,
            sellLocation: importSymbol,
          });
        });
      });
    });

    return opportunities.sort((a, b) => b.profitPerUnit - a.profitPerUnit);
  }

  findOpportunitiesForWaypoint(
    waypointSymbol: string,
    markets: Map<string, Market>,
    limit: number
  ): TradeOpportunity[] {
    const allOpportunities = this.findProfitableRoutes(markets);
    const relevant = allOpportunities.filter(
      (opportunity) =>
        opportunity.buyLocation === waypointSymbol || opportunity.sellLocation === waypointSymbol
    );

    return relevant.slice(0, limit);
  }

  format(opportunity: TradeOpportunity): string {
    return `${opportunity.good}: +${opportunity.profitPerUnit} cr/unit`;
  }
}

const defaultAnalyzer = new MarketOpportunityAnalyzer({ profitThreshold: 100 });

export const calculateOpportunities = (markets: Map<string, Market>): TradeOpportunity[] =>
  defaultAnalyzer.findProfitableRoutes(markets);

export const getWaypointOpportunities = (
  waypointSymbol: string,
  markets: Map<string, Market>,
  limit: number = 2
): TradeOpportunity[] => defaultAnalyzer.findOpportunitiesForWaypoint(waypointSymbol, markets, limit);

export const formatOpportunity = (opportunity: TradeOpportunity): string =>
  defaultAnalyzer.format(opportunity);
