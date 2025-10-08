interface ShipRouteInfoProps {
  routeSummary: string | null;
  etaText: string | null;
}

export const ShipRouteInfo = ({ routeSummary, etaText }: ShipRouteInfoProps) => {
  if (!routeSummary) {
    return null;
  }

  return (
    <div>
      <div className="text-[10px] uppercase text-gray-400">Route</div>
      <div className="text-xs flex items-center gap-2">
        <span>{routeSummary}</span>
        {etaText && (
          <span className="text-[10px] text-red-200 bg-red-500/10 px-1.5 py-0.5 rounded-full">
            ETA {etaText}
          </span>
        )}
      </div>
    </div>
  );
};
