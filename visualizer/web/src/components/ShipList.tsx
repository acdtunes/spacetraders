import { useMemo } from 'react';
import { useStore } from '../store/useStore';
import type { TaggedShip } from '../types/spacetraders';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';
import OverlayToggle from './OverlayToggle';
import { getCargoIcon, getCargoShortLabel } from '../utils/cargo';
import { getFuelBarColor } from '../utils/fuel';
import { formatShipType } from '../utils/shipDisplay';

type OverlayOptions = {
  showDestinationRoutes: boolean;
  toggleDestinationRoutes: () => void;
  showWaypointNames: boolean;
  toggleWaypointNames: () => void;
  showShipNames: boolean;
  toggleShipNames: () => void;
  showMapOverlays: boolean;
  toggleMapOverlays: () => void;
};

const OVERLAY_CONFIG = [
  {
    label: 'Route Vectors',
    tone: 'orange',
    isActive: (options: OverlayOptions) => options.showDestinationRoutes,
    toggle: (options: OverlayOptions) => options.toggleDestinationRoutes(),
  },
  {
    label: 'Overlays',
    tone: 'amber',
    isActive: (options: OverlayOptions) => options.showMapOverlays,
    toggle: (options: OverlayOptions) => options.toggleMapOverlays(),
  },
  {
    label: 'Waypoint Names',
    tone: 'sky',
    isActive: (options: OverlayOptions) => options.showWaypointNames,
    toggle: (options: OverlayOptions) => options.toggleWaypointNames(),
  },
  {
    label: 'Ship Names',
    tone: 'rose',
    isActive: (options: OverlayOptions) => options.showShipNames,
    toggle: (options: OverlayOptions) => options.toggleShipNames(),
  },
] as const;

const STATUS_COLORS: Record<string, string> = {
  IN_TRANSIT: 'bg-orange-500',
  DOCKED: 'bg-green-500',
  IN_ORBIT: 'bg-blue-500',
};

const ShipList = () => {
  const {
    ships,
    agents,
    selectedShip,
    setSelectedShip,
    setSelectedWaypoint,
    filterStatus,
    filterShipRoles,
    toggleShipRoleFilter,
    clearShipRoleFilters,
    currentSystem,
    requestShipFocus,
    showDestinationRoutes,
    toggleDestinationRoutes,
    showWaypointNames,
    toggleWaypointNames,
    showShipNames,
    toggleShipNames,
    showMapOverlays,
    toggleMapOverlays,
    shipNameFilter,
    setShipNameFilter,
  } = useStore();

  const getStatusColor = (status: string) => STATUS_COLORS[status] ?? 'bg-gray-500';

  const nameFilter = shipNameFilter.trim().toLowerCase();

  const overlayOptions = {
    showDestinationRoutes,
    toggleDestinationRoutes,
    showMapOverlays,
    toggleMapOverlays,
    showWaypointNames,
    toggleWaypointNames,
    showShipNames,
    toggleShipNames,
  } satisfies OverlayOptions;

  const filteredShips = useMemo(() => {
    return ships.filter((ship: TaggedShip) => {
      if (currentSystem && ship.nav.systemSymbol !== currentSystem) return false;
      if (!filterStatus.has(ship.nav.status)) return false;
      if (filterShipRoles.size > 0) {
        const roleKey = ship.registration?.role?.toUpperCase() ?? 'UNKNOWN';
        if (!filterShipRoles.has(roleKey)) {
          return false;
        }
      }
      if (nameFilter && !ship.symbol.toLowerCase().includes(nameFilter)) {
        return false;
      }
      return true;
    });
  }, [ships, currentSystem, filterStatus, filterShipRoles, nameFilter]);

  const shipRoles = useMemo(() => {
    const roles = new Set<string>();
    ships.forEach((ship) => {
      const roleKey = ship.registration?.role?.toUpperCase() ?? 'UNKNOWN';
      roles.add(roleKey);
    });
    return Array.from(roles).sort();
  }, [ships]);

  const shipsByAgent = useMemo(() => {
    return filteredShips.reduce((acc, ship: TaggedShip) => {
      const agentId = ship.agentId || 'unknown';
      if (!acc[agentId]) acc[agentId] = [];
      acc[agentId].push(ship);
      return acc;
    }, {} as Record<string, TaggedShip[]>);
  }, [filteredShips]);

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-[11px] font-semibold uppercase tracking-wider text-gray-500 mb-1">
          Ship Filter
        </label>
        <div className="flex gap-2">
          <input
            type="text"
            value={shipNameFilter}
            onChange={(event) => setShipNameFilter(event.target.value)}
            placeholder="Search symbol..."
            className="flex-1 bg-gray-800 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 placeholder:text-gray-500 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
          />
          {shipNameFilter && (
            <button
              onClick={() => setShipNameFilter('')}
              className="text-xs px-2 py-1 border border-gray-700 rounded text-gray-400 hover:text-white hover:border-gray-500"
            >
              Clear
            </button>
          )}
        </div>
      </div>

      {/* Ship Type Filter */}
      {shipRoles.length > 1 && (
        <div className="pb-2 border-b border-gray-800">
          <div className="flex items-center justify-between mb-2">
            <span className="block text-[11px] font-semibold uppercase tracking-wider text-gray-500">
              Ship Type
            </span>
            <button
              onClick={clearShipRoleFilters}
              className="text-[10px] text-gray-500 hover:text-gray-200"
            >
              Reset
            </button>
          </div>
          <div className="grid grid-cols-2 gap-1">
            {shipRoles.map((role) => {
              const isActive = filterShipRoles.size === 0 || filterShipRoles.has(role);
              return (
                <button
                  key={role}
                  onClick={() => toggleShipRoleFilter(role)}
                  className={`w-full text-left px-2 py-1.5 rounded border transition-colors text-xs ${
                    isActive
                      ? 'bg-gray-700 border-gray-600 text-white'
                      : 'bg-gray-800 border-gray-700 text-gray-500'
                  }`}
                >
                  {formatShipType(role)}
                </button>
              );
            })}
          </div>
        </div>
      )}

      {filteredShips.length === 0 && (
        <div className="text-center text-gray-500 text-sm py-6">
          No ships match the current filters
        </div>
      )}

      <div className="pb-2 border-b border-gray-800">
        <span className="block text-[11px] font-semibold uppercase tracking-wider text-gray-500 mb-2">
          Map Overlays
        </span>
        <div className="grid grid-cols-2 gap-2 text-xs">
          {OVERLAY_CONFIG.map(({ label, tone, isActive, toggle }) => (
            <OverlayToggle
              key={label}
              label={label}
              active={isActive(overlayOptions)}
              onToggle={() => toggle(overlayOptions)}
              activeTone={tone}
            />
          ))}
        </div>
      </div>

      {Object.entries(shipsByAgent).map(([agentId, agentShips]) => {
        const agent = agents.find(a => a.id === agentId);

        return (
          <div key={agentId}>
            {agent && (
              <div className="flex items-center gap-2 mb-2">
                <div
                  className="w-3 h-3 rounded-full"
                  style={{ backgroundColor: agent.color }}
                />
                <span className="text-xs font-bold text-gray-400 uppercase">
                  {agent.symbol}
                </span>
                <span className="text-xs text-gray-600">({agentShips.length})</span>
              </div>
            )}

            <div className="space-y-1">
              {agentShips.map((ship) => (
                <button
                  key={ship.symbol}
                  onClick={() => {
                    setSelectedShip(ship);
                    setSelectedWaypoint(null);
                    requestShipFocus(ship.symbol, VIEWPORT_CONSTANTS.SHIP_FOCUS_ZOOM);
                  }}
                  className={`w-full text-left p-2 rounded border transition-colors ${
                    selectedShip?.symbol === ship.symbol
                      ? 'bg-blue-900/30 border-blue-500'
                      : 'bg-gray-750 border-gray-700 hover:bg-gray-700 hover:border-gray-600'
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <div className="flex-1 min-w-0">
                      <div className="text-xs font-semibold text-white truncate">
                        {ship.symbol}
                      </div>
                      <div className="text-xs text-gray-400 truncate mt-0.5">
                        {ship.nav.waypointSymbol}
                      </div>
                    </div>
                    <div className="flex items-center gap-1.5 ml-2">
                      <div
                        className={`w-2 h-2 rounded-full ${getStatusColor(ship.nav.status)}`}
                        title={ship.nav.status}
                      />
                      {ship.cooldown && ship.cooldown.remainingSeconds > 0 && (
                        <span className="text-xs">⛏️</span>
                      )}
                    </div>
                  </div>

                  <div className="flex gap-2 mt-1.5">
                    <div className="flex-1">
                      <div className="flex justify-between text-xs text-gray-500 mb-0.5">
                        <span>Cargo</span>
                        <span>{ship.cargo.units}/{ship.cargo.capacity}</span>
                      </div>
                      <div className="w-full bg-gray-700 rounded-full h-1">
                        <div
                          className="bg-blue-500 h-1 rounded-full"
                          style={{ width: `${(ship.cargo.units / ship.cargo.capacity) * 100}%` }}
                        />
                      </div>
                    </div>

                    <div className="flex-1">
                      {(() => {
                        const fuelPercent = ship.fuel.capacity > 0 ? (ship.fuel.current / ship.fuel.capacity) * 100 : 0;
                        return (
                          <>
                            <div className="flex justify-between text-xs text-gray-500 mb-0.5">
                              <span>Fuel</span>
                              <span>{ship.fuel.current}/{ship.fuel.capacity}</span>
                            </div>
                            <div className="w-full bg-gray-700 rounded-full h-1">
                              <div
                                className="h-1 rounded-full"
                                style={{
                                  width: `${Math.min(100, Math.max(0, fuelPercent))}%`,
                                  backgroundColor: getFuelBarColor(fuelPercent),
                                }}
                              />
                            </div>
                          </>
                        );
                      })()}
                    </div>
                  </div>

                  {ship.cargo.inventory.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5 mt-2">
                      {ship.cargo.inventory.slice(0, 4).map((item, index) => (
                        <div
                          key={`${ship.symbol}-cargo-${item.symbol}-${index}`}
                          className="flex items-center gap-1 bg-gray-800/80 border border-gray-700 rounded px-1.5 py-1"
                        >
                          <span className="text-sm leading-none">{getCargoIcon(item.symbol)}</span>
                          <span className="text-[11px] text-gray-300 leading-none">
                            {getCargoShortLabel(item.symbol)}
                          </span>
                          <span className="text-[11px] text-gray-400 leading-none">×{item.units}</span>
                        </div>
                      ))}
                      {ship.cargo.inventory.length > 4 && (
                        <span className="text-[11px] text-gray-500 px-1.5 py-1">
                          +{ship.cargo.inventory.length - 4} more
                        </span>
                      )}
                    </div>
                  ) : (
                    <div className="text-[11px] text-gray-600 mt-2">Empty hold</div>
                  )}
                </button>
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
};

export default ShipList;
