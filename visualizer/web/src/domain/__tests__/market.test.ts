import { describe, it, expect } from 'vitest';
import { MarketOpportunityAnalyzer } from '../market';
import type { Market } from '../../types/spacetraders';

describe('MarketOpportunityAnalyzer', () => {
  const analyzer = new MarketOpportunityAnalyzer({ profitThreshold: 50 });

  const marketA: Market = {
    symbol: 'X1-A',
    exports: [
      { symbol: 'IRON_ORE', tradeVolume: 10, supply: 'HIGH', purchasePrice: 50, sellPrice: 0 },
    ],
    imports: [],
    exchange: [],
  };

  const marketB: Market = {
    symbol: 'X1-B',
    exports: [],
    imports: [
      { symbol: 'IRON_ORE', tradeVolume: 10, supply: 'SCARCE', purchasePrice: 0, sellPrice: 200 },
    ],
    exchange: [],
  };

  it('finds profitable trade routes exceeding threshold', () => {
    const markets = new Map([
      [marketA.symbol, marketA],
      [marketB.symbol, marketB],
    ]);

    const opportunities = analyzer.findProfitableRoutes(markets);
    expect(opportunities).toHaveLength(1);
    expect(opportunities[0]).toMatchObject({
      good: 'IRON_ORE',
      buyLocation: 'X1-A',
      sellLocation: 'X1-B',
      profitPerUnit: 150,
    });
  });

  it('filters opportunities for a specific waypoint', () => {
    const markets = new Map([
      [marketA.symbol, marketA],
      [marketB.symbol, marketB],
    ]);

    const opportunities = analyzer.findOpportunitiesForWaypoint('X1-B', markets, 1);
    expect(opportunities).toHaveLength(1);
    expect(opportunities[0].sellLocation).toBe('X1-B');
  });
});
