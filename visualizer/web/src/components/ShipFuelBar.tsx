import { memo } from 'react';

interface ShipFuelBarProps {
  current: number;
  capacity: number;
  percent: number;
  getColor: (percent: number) => string;
}

export const ShipFuelBar = memo(({ current, capacity, percent, getColor }: ShipFuelBarProps) => (
  <div>
    <div className="flex items-center justify-between text-[10px] uppercase text-gray-400">
      <span>Fuel</span>
      <span className="text-xs text-red-200 font-semibold">
        {current} / {capacity} ({percent}%)
      </span>
    </div>
    <div className="w-full bg-red-900/40 h-1.5 rounded-full mt-1">
      <div
        className="h-1.5 rounded-full"
        style={{
          width: `${Math.min(100, Math.max(0, percent))}%`,
          backgroundColor: getColor(percent),
        }}
      />
    </div>
  </div>
));

ShipFuelBar.displayName = 'ShipFuelBar';
