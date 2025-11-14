import type { MarketSupply } from '../types/spacetraders';

interface WaypointMarketplaceProps {
  hasMarketplace: boolean;
  marketData?: {
    importsCount: number;
    exportsCount: number;
    opportunities: string[];
  } | null;
  intel?: {
    lastUpdated: string;
    goods: Array<{
      symbol: string;
      supply: MarketSupply;
      activity: string | null;
      purchasePrice: number;
      sellPrice: number;
      tradeVolume: number;
      spread: number;
    }>;
  } | null;
}

const formatTimeAgo = (timestamp: string) => {
  const diffMs = Date.now() - new Date(timestamp).getTime();
  if (Number.isNaN(diffMs)) {
    return 'unknown';
  }

  const diffMinutes = Math.floor(diffMs / 60000);
  if (diffMinutes < 1) return 'just now';
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays}d ago`;
};

const formatSupplyLabel = (supply: MarketSupply): string =>
  supply
    .toLowerCase()
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (char) => char.toUpperCase());

export const WaypointMarketplace = ({ hasMarketplace, marketData, intel }: WaypointMarketplaceProps) => {
  if (!hasMarketplace) {
    return null;
  }

  const hasIntel = Boolean(intel && intel.goods.length > 0);
  const hasMarketData = Boolean(marketData);

  if (!hasIntel && !hasMarketData) {
    return (
      <div className="border-t border-sky-500/40 pt-2 mt-2">
        <div className="flex items-center justify-between mb-1">
          <span className="text-[10px] uppercase text-sky-300 tracking-wide">Marketplace</span>
          <span className="text-sm">🏪</span>
        </div>
        <div className="text-[11px] text-zinc-500">
          Market intel unavailable. Enable Markets overlay for trade insights.
        </div>
      </div>
    );
  }

  const topGoods = hasIntel ? intel!.goods.slice(0, 4) : [];

  return (
    <div className="border-t border-sky-500/40 pt-2 mt-2">
      <div className="flex items-center justify-between mb-1">
        <span className="text-[10px] uppercase text-sky-300 tracking-wide">Marketplace</span>
        <span className="text-sm">🏪</span>
      </div>
      <div className="space-y-2">
        {hasMarketData && marketData && (
          <div className="space-y-1">
            <div className="flex justify-between text-[11px] text-sky-100">
              <span>Imports</span>
              <span>{marketData.importsCount}</span>
            </div>
            <div className="flex justify-between text-[11px] text-rose-100">
              <span>Exports</span>
              <span>{marketData.exportsCount}</span>
            </div>
            {marketData.opportunities.length > 0 && (
              <div>
                <div className="text-[10px] uppercase text-emerald-300 mb-0.5">Opportunities</div>
                <ul className="list-disc list-inside text-[11px] text-emerald-200 space-y-0.5">
                  {marketData.opportunities.map((opp, index) => (
                    <li key={`opportunity-${index}`}>{opp}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}

        {hasIntel && intel && (
          <div className="border border-sky-500/30 rounded-md p-2 bg-gray-900/70">
            <div className="flex items-center justify-between text-[10px] uppercase text-sky-200 mb-1">
              <span>Recent Intel</span>
              <span className="text-sky-300">{formatTimeAgo(intel.lastUpdated)}</span>
            </div>
            <div className="grid grid-cols-2 gap-1.5">
              {topGoods.map((good) => (
                <div
                  key={good.symbol}
                  className="bg-gray-800/60 border border-gray-700/60 rounded px-2 py-1.5 space-y-1"
                >
                  <div className="flex items-center justify-between text-[11px] text-gray-200">
                    <span className="font-semibold truncate max-w-[120px]" title={good.symbol}>
                      {good.symbol}
                    </span>
                    <span className="font-mono text-emerald-300 whitespace-nowrap">
                      +{good.spread.toLocaleString()}₡
                    </span>
                  </div>
                  <div className="flex items-center justify-between text-[10px] text-gray-500">
                    <span>Buy {good.purchasePrice.toLocaleString()}₡</span>
                    <span>Sell {good.sellPrice.toLocaleString()}₡</span>
                  </div>
                  <div className="flex items-center justify-between text-[10px] text-gray-500">
                    <span>Supply {formatSupplyLabel(good.supply)}</span>
                    {good.activity && <span>{good.activity}</span>}
                  </div>
                </div>
              ))}
              {intel.goods.length > topGoods.length && (
                <div className="col-span-2 text-[10px] text-gray-500 text-right">
                  +{intel.goods.length - topGoods.length} more goods
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
};
