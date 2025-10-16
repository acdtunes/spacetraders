import { useMemo } from 'react';
import type { ScoutTour } from '../types/spacetraders';
import { getTourId, getTourLabel } from '../utils/tourHelpers';

interface TourFilterPanelProps {
  tours: ScoutTour[];
  visibleTours: Set<string>;
  onToggleTour: (tourId: string) => void;
  onShowAll: () => void;
  onHideAll: () => void;
  showMarketFreshness: boolean;
  onToggleMarketFreshness: () => void;
}

/**
 * Same color palette as ScoutTourLayer for consistency
 */
const TOUR_COLOR_PALETTE = [
  '#FF6B6B', // Red
  '#4ECDC4', // Teal
  '#45B7D1', // Blue
  '#FFA07A', // Light Orange
  '#98D8C8', // Mint
  '#F7DC6F', // Yellow
  '#BB8FCE', // Purple
  '#85C1E2', // Sky Blue
  '#F8B739', // Orange
  '#52B788', // Green
  '#EE6C4D', // Coral
  '#3D5A80', // Navy
  '#E63946', // Crimson
  '#06FFA5', // Bright Green
  '#C77DFF', // Lavender
];

function getTourColor(systemSymbol: string, index: number): string {
  if (index < TOUR_COLOR_PALETTE.length) {
    return TOUR_COLOR_PALETTE[index];
  }

  let hash = 0;
  for (let i = 0; i < systemSymbol.length; i++) {
    hash = systemSymbol.charCodeAt(i) + ((hash << 5) - hash);
  }

  return TOUR_COLOR_PALETTE[Math.abs(hash) % TOUR_COLOR_PALETTE.length];
}

export const TourFilterPanel = ({
  tours,
  visibleTours,
  onToggleTour,
  onShowAll,
  onHideAll,
  showMarketFreshness,
  onToggleMarketFreshness,
}: TourFilterPanelProps) => {
  const sortedTours = useMemo(() => {
    return [...tours].sort((a, b) => a.system.localeCompare(b.system));
  }, [tours]);

  if (tours.length === 0) {
    return (
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <h3 className="text-white font-semibold mb-2 text-sm">Scout Tours</h3>
        <p className="text-gray-400 text-xs">No scout tours active</p>
      </div>
    );
  }

  const allVisible = tours.length > 0 && tours.every((t) => visibleTours.has(getTourId(t)));
  const noneVisible = tours.every((t) => !visibleTours.has(getTourId(t)));

  return (
    <div className="bg-gray-800 rounded-lg p-4 border border-gray-700 max-w-xs">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-white font-semibold text-sm">Scout Tours</h3>
        <div className="flex gap-2">
          <button
            onClick={onShowAll}
            disabled={allVisible}
            className="text-xs px-2 py-1 bg-gray-700 hover:bg-gray-600 disabled:bg-gray-750 disabled:text-gray-500 rounded transition-colors"
          >
            All
          </button>
          <button
            onClick={onHideAll}
            disabled={noneVisible}
            className="text-xs px-2 py-1 bg-gray-700 hover:bg-gray-600 disabled:bg-gray-750 disabled:text-gray-500 rounded transition-colors"
          >
            None
          </button>
        </div>
      </div>

      <button
        onClick={onToggleMarketFreshness}
        className={`w-full mb-3 px-2 py-1 rounded border text-[11px] transition-colors ${
          showMarketFreshness
            ? 'border-emerald-500 text-emerald-300 bg-emerald-500/10'
            : 'border-gray-600 text-gray-400 hover:text-gray-200 hover:border-gray-500'
        }`}
      >
        Market Freshness
      </button>

      <div className="space-y-2 max-h-60 overflow-y-auto">
        {sortedTours.map((tour, index) => {
          const tourId = getTourId(tour);
          const tourLabel = getTourLabel(tour);
          const isVisible = visibleTours.has(tourId);
          const color = getTourColor(tour.system, index);

          return (
            <label
              key={tourId}
              className="flex items-center gap-2 cursor-pointer hover:bg-gray-750 p-2 rounded transition-colors"
            >
              <input
                type="checkbox"
                checked={isVisible}
                onChange={() => onToggleTour(tourId)}
                className="w-4 h-4 rounded border-gray-600 bg-gray-700 text-blue-600 focus:ring-2 focus:ring-blue-500 focus:ring-offset-0"
              />
              <div className="flex items-center gap-2 flex-1 min-w-0">
                <div
                  className="w-3 h-3 rounded-full flex-shrink-0"
                  style={{ backgroundColor: color }}
                />
                <div className="flex flex-col flex-1 min-w-0">
                  <span className="text-sm text-gray-200 font-mono truncate">
                    {tourLabel}
                  </span>
                  <span className="text-[10px] uppercase text-gray-500 tracking-wide">
                    {tour.system}
                  </span>
                </div>
              </div>
              <span className="text-xs text-gray-400 whitespace-nowrap">
                {tour.tour_order.length} pts
              </span>
            </label>
          );
        })}
      </div>

      <div className="mt-3 pt-3 border-t border-gray-700 text-xs text-gray-400">
        {visibleTours.size} of {tours.length} visible
      </div>
    </div>
  );
};
