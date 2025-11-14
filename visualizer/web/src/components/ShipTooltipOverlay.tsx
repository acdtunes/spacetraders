import type { CSSProperties } from 'react';
import type { ShipTooltipData } from '../hooks/useShipTooltip';
import { ShipTooltipHeader } from './ShipTooltipHeader';
import { ShipRouteInfo } from './ShipRouteInfo';
import { ShipFuelBar } from './ShipFuelBar';
import { ShipCargoBar } from './ShipCargoBar';
import { ShipCargoList } from './ShipCargoList';
import { getFuelBarColor } from '../utils/fuel';

export interface ShipTooltipOverlayProps {
  tooltip: ShipTooltipData;
  position: { left: number; top: number };
}

export const ShipTooltipOverlay = ({ tooltip, position }: ShipTooltipOverlayProps) => {
  const style: CSSProperties = {
    left: `${position.left}px`,
    top: `${position.top}px`,
    transform: 'translate(-100%, -100%)',
  };

  return (
    <div className="absolute pointer-events-none z-30" style={style}>
      <div className="bg-gray-900 bg-opacity-70 border border-red-500/70 rounded-lg p-2.5 text-xs min-w-[220px] max-w-[300px] pointer-events-none shadow-xl backdrop-blur-sm">
        <ShipTooltipHeader
          symbol={tooltip.symbol}
          role={tooltip.role}
          statusText={tooltip.statusText}
          flightMode={tooltip.flightMode}
        />

        <div className="space-y-3 text-gray-200">
          {tooltip.cooldownSeconds !== null && (
            <div>
              <div className="text-[10px] uppercase text-gray-400">Cooldown</div>
              <div className="text-xs">{tooltip.cooldownSeconds}s</div>
            </div>
          )}

          <ShipRouteInfo routeSummary={tooltip.routeSummary} etaText={tooltip.etaText} />

          <ShipFuelBar
            current={tooltip.fuelCurrent}
            capacity={tooltip.fuelCapacity}
            percent={tooltip.fuelPercent}
            getColor={getFuelBarColor}
          />

          <ShipCargoBar
            units={tooltip.cargoUnits}
            capacity={tooltip.cargoCapacity}
            percent={tooltip.cargoPercent}
          />
        </div>

        <ShipCargoList entries={tooltip.cargoEntries} extraCount={tooltip.extraCargoCount} />
      </div>
    </div>
  );
};
