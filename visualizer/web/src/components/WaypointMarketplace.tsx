interface WaypointMarketplaceProps {
  hasMarketplace: boolean;
  marketData?: {
    importsCount: number;
    exportsCount: number;
    opportunities: string[];
  } | null;
}

export const WaypointMarketplace = ({ hasMarketplace, marketData }: WaypointMarketplaceProps) => {
  if (!hasMarketplace) {
    return null;
  }

  return (
    <div className="border-t border-sky-500/40 pt-2 mt-2">
      <div className="flex items-center justify-between mb-1">
        <span className="text-[10px] uppercase text-sky-300 tracking-wide">Marketplace</span>
        <span className="text-sm">🏪</span>
      </div>
      {marketData ? (
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
      ) : (
        <div className="text-[11px] text-zinc-500">
          Market intel unavailable. Enable Markets overlay for trade insights.
        </div>
      )}
    </div>
  );
};
