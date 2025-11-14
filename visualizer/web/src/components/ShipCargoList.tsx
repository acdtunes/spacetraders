interface ShipCargoListProps {
  entries: { icon: string; label: string; units: number }[];
  extraCount: number;
}

export const ShipCargoList = ({ entries, extraCount }: ShipCargoListProps) => {
  if (entries.length === 0) {
    return null;
  }

  return (
    <div className="mt-3">
      <div className="text-[10px] uppercase text-gray-400 mb-1">Cargo Hold</div>
      <div className="grid grid-cols-2 gap-2">
        {entries.map((item, index) => {
          const isImageIcon = item.icon.startsWith('/');

          return (
            <div
              key={`${item.label}-${index}`}
              className="flex items-center gap-2 text-xs text-gray-200 bg-white/5 border border-white/10 rounded-md px-2 py-1"
            >
              {isImageIcon ? (
                <img src={item.icon} alt={item.label} className="w-5 h-5 object-contain" />
              ) : (
                <span className="text-base leading-none">{item.icon}</span>
              )}
              <div className="flex flex-col leading-tight">
                <span className="text-[11px]">{item.label}</span>
                <span className="text-[10px] text-gray-400">×{item.units}</span>
              </div>
            </div>
          );
        })}
        {extraCount > 0 && (
          <div className="col-span-2 text-[10px] text-gray-500">
            +{extraCount} more item{extraCount > 1 ? 's' : ''}
          </div>
        )}
      </div>
    </div>
  );
};
