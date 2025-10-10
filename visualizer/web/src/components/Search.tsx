import { useState, useMemo } from 'react';
import { useStore } from '../store/useStore';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';

interface SearchProps {
  onFocusOn: (x: number, y: number, scale?: number) => void;
}

const Search = ({ onFocusOn }: SearchProps) => {
  const [query, setQuery] = useState('');
  const {
    ships,
    waypoints,
    setSelectedShip,
    setSelectedWaypoint,
    currentSystem,
    requestShipFocus,
  } = useStore();

  // Search results
  const results = useMemo(() => {
    if (!query.trim()) return { ships: [], waypoints: [] };

    const lowerQuery = query.toLowerCase();

    // Search ships (filtered by current system)
    const matchingShips = ships.filter(ship => {
      // Filter by current system
      if (currentSystem && ship.nav.systemSymbol !== currentSystem) return false;

      return ship.symbol.toLowerCase().includes(lowerQuery) ||
        ship.registration.name.toLowerCase().includes(lowerQuery);
    });

    // Search waypoints (filtered by current system)
    const matchingWaypoints = Array.from(waypoints.values()).filter(waypoint => {
      // Filter by current system
      if (currentSystem && waypoint.systemSymbol !== currentSystem) return false;

      return waypoint.symbol.toLowerCase().includes(lowerQuery) ||
        waypoint.type.toLowerCase().includes(lowerQuery);
    });

    return {
      ships: matchingShips.slice(0, 10), // Limit to 10 results
      waypoints: matchingWaypoints.slice(0, 10),
    };
  }, [query, ships, waypoints, currentSystem]);

  const handleSelectShip = (ship: any) => {
    setSelectedShip(ship);
    setSelectedWaypoint(null);
    requestShipFocus(ship.symbol, VIEWPORT_CONSTANTS.MAX_ZOOM);
  };

  const handleSelectWaypoint = (waypoint: any) => {
    setSelectedWaypoint(waypoint);
    setSelectedShip(null);
    onFocusOn(waypoint.x, waypoint.y);
  };

  const totalResults = results.ships.length + results.waypoints.length;

  return (
    <div className="space-y-3">
      {/* Search Input */}
      <div className="relative">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search ships or waypoints..."
          className="w-full px-3 py-2 bg-gray-750 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-blue-500 transition-colors"
        />
        {query && (
          <button
            onClick={() => setQuery('')}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-gray-300 transition-colors"
          >
            ✕
          </button>
        )}
      </div>

      {/* Results */}
      {query && (
        <div className="space-y-3">
          {totalResults === 0 ? (
            <div className="text-center text-gray-500 text-sm py-8">
              No results found for "{query}"
            </div>
          ) : (
            <>
              {/* Ships Results */}
              {results.ships.length > 0 && (
                <div>
                  <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">
                    Ships ({results.ships.length})
                  </h3>
                  <div className="space-y-1">
                    {results.ships.map((ship) => (
                      <button
                        key={ship.symbol}
                        onClick={() => handleSelectShip(ship)}
                        className="w-full text-left p-2 bg-gray-750 border border-gray-700 rounded hover:bg-gray-700 hover:border-gray-600 transition-colors"
                      >
                        <div className="flex items-center justify-between">
                          <div className="flex-1 min-w-0">
                            <div className="text-xs font-semibold text-white truncate">
                              {ship.symbol}
                            </div>
                            <div className="text-xs text-gray-400 truncate">
                              {ship.nav.waypointSymbol}
                            </div>
                          </div>
                          <div className="ml-2 text-xs text-gray-500">🚀</div>
                        </div>
                      </button>
                    ))}
                  </div>
                </div>
              )}

              {/* Waypoints Results */}
              {results.waypoints.length > 0 && (
                <div>
                  <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">
                    Waypoints ({results.waypoints.length})
                  </h3>
                  <div className="space-y-1">
                    {results.waypoints.map((waypoint) => (
                      <button
                        key={waypoint.symbol}
                        onClick={() => handleSelectWaypoint(waypoint)}
                        className="w-full text-left p-2 bg-gray-750 border border-gray-700 rounded hover:bg-gray-700 hover:border-gray-600 transition-colors"
                      >
                        <div className="flex items-center justify-between">
                          <div className="flex-1 min-w-0">
                            <div className="text-xs font-semibold text-white truncate">
                              {waypoint.symbol}
                            </div>
                            <div className="text-xs text-gray-400 truncate">
                              {waypoint.type.replace(/_/g, ' ')}
                            </div>
                          </div>
                          <div className="ml-2 text-xs text-gray-500">
                            ({waypoint.x}, {waypoint.y})
                          </div>
                        </div>
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      )}

      {/* Search Tips */}
      {!query && (
        <div className="text-xs text-gray-500 space-y-1 pt-2">
          <div className="font-semibold text-gray-400">Search Tips:</div>
          <div>• Type ship symbol or name</div>
          <div>• Type waypoint symbol or type</div>
          <div>• Results update as you type</div>
        </div>
      )}
    </div>
  );
};

export default Search;
