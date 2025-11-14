import type { CSSProperties } from 'react';
import type { Waypoint as WaypointType, MarketSupply } from '../types/spacetraders';
import { WaypointTraits } from './WaypointTraits';
import { WaypointMarketplace } from './WaypointMarketplace';

export interface WaypointTooltipData {
  symbol: string;
  type: string;
  traits: WaypointType['traits'];
  faction: WaypointType['faction'] | null | undefined;
  hasMarketplace: boolean;
  marketData: {
    importsCount: number;
    exportsCount: number;
    opportunities: string[];
  } | null;
  intel: {
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

export interface WaypointTooltipOverlayProps {
  tooltip: WaypointTooltipData;
  position: { left: number; top: number };
}

export const WaypointTooltipOverlay = ({ tooltip, position }: WaypointTooltipOverlayProps) => {
  const style: CSSProperties = {
    left: `${position.left}px`,
    top: `${position.top}px`,
    transform: 'translate(-50%, -110%)',
  };

  return (
    <div
      className="absolute bg-gray-900 bg-opacity-70 border border-sky-500/60 rounded-lg p-3 text-xs min-w-[220px] max-w-[280px] pointer-events-none z-30 shadow-2xl backdrop-blur"
      style={style}
    >
      <div className="flex items-start justify-between gap-2 mb-2">
        <div>
          <div className="text-sm font-bold text-white leading-snug">{tooltip.symbol}</div>
          <div className="text-[11px] text-sky-200 uppercase tracking-wide">
            {tooltip.type.replace(/_/g, ' ')}
          </div>
        </div>
        {tooltip.faction && (
          <span className="text-[10px] font-semibold text-sky-200 bg-sky-500/10 border border-sky-500/40 rounded-full px-1.5 py-0.5 whitespace-nowrap">
            {tooltip.faction.symbol}
          </span>
        )}
      </div>

      <div className="grid grid-cols-2 gap-1 text-zinc-300 mb-2">
        <WaypointTraits symbol={tooltip.symbol} traits={tooltip.traits} />
      </div>

      <WaypointMarketplace
        hasMarketplace={tooltip.hasMarketplace}
        marketData={tooltip.marketData}
        intel={tooltip.intel}
      />
    </div>
  );
};
