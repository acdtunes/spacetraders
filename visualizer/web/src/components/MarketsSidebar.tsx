import { useMemo } from 'react';
import type { ScoutTour, TaggedShip } from '../types/spacetraders';
import { getTourId, getTourLabel } from '../utils/tourHelpers';
import { useStore } from '../store/useStore';

interface MarketsSidebarProps {
  tours: ScoutTour[];
  visibleTours: Set<string>;
  onToggleTour: (tourId: string) => void;
  onShowAll: () => void;
  onHideAll: () => void;
  showMarketFreshness: boolean;
  onToggleMarketFreshness: () => void;
  isVisible: boolean;
  onToggle: () => void;
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

export const MarketsSidebar = ({
  tours,
  visibleTours,
  onToggleTour,
  onShowAll,
  onHideAll,
  showMarketFreshness,
  onToggleMarketFreshness,
  isVisible,
  onToggle,
}: MarketsSidebarProps) => {
  const ships = useStore((state) => state.ships);

  // Create a map of ship symbols to ship data for quick lookup
  const shipsBySymbol = useMemo(() => {
    const map = new Map<string, TaggedShip>();
    ships.forEach((ship) => {
      map.set(ship.symbol, ship);
    });
    return map;
  }, [ships]);

  const sortedTours = useMemo(() => {
    return [...tours].sort((a, b) => a.system.localeCompare(b.system));
  }, [tours]);

  const totalTours = tours.length;
  const allVisible = tours.length > 0 && tours.every((t) => visibleTours.has(getTourId(t)));
  const noneVisible = tours.every((t) => !visibleTours.has(getTourId(t)));

  if (!isVisible) {
    // Collapsed tab
    return (
      <div
        className="fixed top-20 right-0 bg-gray-800 border-l border-gray-700 rounded-l-lg cursor-pointer hover:bg-gray-750 transition-colors z-40"
        onClick={onToggle}
        style={{ padding: '12px 8px', marginRight: '48px' }}
      >
        <div className="flex flex-col items-center gap-2">
          <span className="text-gray-400 text-xs" style={{ writingMode: 'vertical-rl' }}>
            MARKETS
          </span>
          <div className="bg-emerald-600 text-white text-xs font-bold rounded-full w-6 h-6 flex items-center justify-center">
            {totalTours}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="fixed top-20 right-0 w-80 max-h-[calc(100vh-100px)] bg-gray-800 border-l border-gray-700 rounded-l-lg shadow-xl overflow-hidden z-40 flex flex-col" style={{ marginRight: '48px' }}>
      {/* Header */}
      <div className="p-4 border-b border-gray-700 flex items-center justify-between bg-gray-750">
        <div className="flex items-center gap-2">
          <h3 className="text-white font-semibold">Market Scout Tours</h3>
          <div className="bg-emerald-600 text-white text-xs font-bold rounded-full w-6 h-6 flex items-center justify-center">
            {totalTours}
          </div>
        </div>
        <button
          onClick={onToggle}
          className="text-gray-400 hover:text-white transition-colors"
          aria-label="Close sidebar"
        >
          ✕
        </button>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4 space-y-3">
        {/* Market Freshness Toggle */}
        <button
          onClick={onToggleMarketFreshness}
          className={`w-full px-3 py-2 rounded border text-sm transition-colors ${
            showMarketFreshness
              ? 'border-emerald-500 text-emerald-300 bg-emerald-500/10'
              : 'border-gray-600 text-gray-400 bg-gray-700/50'
          }`}
        >
          {showMarketFreshness ? '✓' : '○'} Market Freshness Overlay
        </button>

        {/* Tours Section */}
        {tours.length === 0 ? (
          <div className="text-gray-400 text-sm text-center py-8">
            No scout tours active
          </div>
        ) : (
          <>
            <div className="flex items-center justify-between">
              <h4 className="text-white text-sm font-medium">Active Tours</h4>
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

            <div className="space-y-2">
              {sortedTours.map((tour, index) => {
                const tourId = getTourId(tour);
                const isChecked = visibleTours.has(tourId);
                const color = getTourColor(tour.system, index);
                const label = getTourLabel(tour);
                const ship = shipsBySymbol.get(tour.ship_symbol);
                const shipRole = ship?.registration.role;

                return (
                  <label
                    key={tourId}
                    className="flex items-center gap-2 p-2 rounded hover:bg-gray-700 cursor-pointer group"
                  >
                    <input
                      type="checkbox"
                      checked={isChecked}
                      onChange={() => onToggleTour(tourId)}
                      className="w-4 h-4 rounded"
                    />
                    <div
                      className="w-3 h-3 rounded-full flex-shrink-0"
                      style={{ backgroundColor: color }}
                    />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center justify-between gap-2">
                        <div className="text-sm text-white font-mono truncate">
                          {tour.ship_symbol}
                        </div>
                        {shipRole && (
                          <div className="text-xs text-gray-500 uppercase flex-shrink-0">
                            {shipRole}
                          </div>
                        )}
                      </div>
                      <div className="text-xs text-gray-400">
                        {tour.markets.length} market{tour.markets.length !== 1 ? 's' : ''}
                      </div>
                    </div>
                  </label>
                );
              })}
            </div>
          </>
        )}
      </div>

      {/* Footer */}
      <div className="p-3 border-t border-gray-700 bg-gray-750 text-xs text-gray-400">
        <div className="flex justify-between">
          <span>Total Tours</span>
          <span className="text-white font-semibold">{totalTours}</span>
        </div>
      </div>
    </div>
  );
};
