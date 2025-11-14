import { memo } from 'react';

interface ShipCargoBarProps {
  units: number;
  capacity: number;
  percent: number;
}

export const ShipCargoBar = memo(({ units, capacity, percent }: ShipCargoBarProps) => (
  <div>
    <div className="flex items-center justify-between text-[10px] uppercase text-gray-400">
      <span>Cargo</span>
      <span className="text-xs text-red-200 font-semibold">
        {units} / {capacity} ({percent}%)
      </span>
    </div>
    <div className="w-full bg-red-900/40 h-1.5 rounded-full mt-1">
      <div
        className="bg-red-500 h-1.5 rounded-full"
        style={{ width: `${Math.min(100, Math.max(0, percent))}%` }}
      />
    </div>
  </div>
));

ShipCargoBar.displayName = 'ShipCargoBar';
