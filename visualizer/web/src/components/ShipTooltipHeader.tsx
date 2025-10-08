interface ShipTooltipHeaderProps {
  symbol: string;
  role: string;
  statusText: string;
  flightMode: string;
}

export const ShipTooltipHeader = ({ symbol, role, statusText, flightMode }: ShipTooltipHeaderProps) => (
  <div className="flex flex-col gap-1 mb-3">
    <div className="flex items-center gap-2">
      <span className="text-sm font-bold text-white leading-snug">{symbol}</span>
      <span className="text-[10px] font-semibold text-red-200 bg-red-500/15 border border-red-500/40 rounded-full px-1.5 py-0.5 whitespace-nowrap">
        {role}
      </span>
    </div>
    <div className="text-[11px] text-gray-200 flex items-center justify-between gap-2">
      <span className="text-red-200 font-semibold truncate uppercase">{statusText}</span>
      <span className="text-gray-400 text-[10px] uppercase whitespace-nowrap">{flightMode}</span>
    </div>
  </div>
);
