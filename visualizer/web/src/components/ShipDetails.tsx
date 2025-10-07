import type { Ship } from '../types/spacetraders';
import { getCargoIcon, getCargoLabel, getCargoShortLabel } from '../utils/cargo';

interface ShipDetailsProps {
  ship: Ship;
}

const ShipDetails = ({ ship }: ShipDetailsProps) => {
  const getStatusColor = (status: string) => {
    switch (status) {
      case 'IN_TRANSIT': return 'text-orange-400';
      case 'DOCKED': return 'text-green-400';
      case 'IN_ORBIT': return 'text-blue-400';
      default: return 'text-gray-400';
    }
  };

  const formatDate = (dateString: string) => {
    const date = new Date(dateString);
    return date.toLocaleString();
  };

  const calculateProgress = () => {
    if (ship.nav.status !== 'IN_TRANSIT') return 0;

    const now = Date.now();
    const departure = new Date(ship.nav.route.departureTime).getTime();
    const arrival = new Date(ship.nav.route.arrival).getTime();
    const progress = ((now - departure) / (arrival - departure)) * 100;

    return Math.min(100, Math.max(0, progress));
  };

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="border-b border-gray-700 pb-3">
        <h2 className="text-lg font-bold text-white">{ship.symbol}</h2>
        <p className="text-sm text-gray-400">{ship.registration.name}</p>
      </div>

      {/* Registration */}
      <div>
        <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Registration</h3>
        <div className="space-y-1 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-400">Role:</span>
            <span className="text-white">{ship.registration.role}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-gray-400">Faction:</span>
            <span className="text-white">{ship.registration.factionSymbol}</span>
          </div>
        </div>
      </div>

      {/* Navigation */}
      <div>
        <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Navigation</h3>
        <div className="space-y-1 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-400">Status:</span>
            <span className={`font-semibold ${getStatusColor(ship.nav.status)}`}>
              {ship.nav.status}
            </span>
          </div>
          <div className="flex justify-between">
            <span className="text-gray-400">Location:</span>
            <span className="text-white text-xs">{ship.nav.waypointSymbol}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-gray-400">Flight Mode:</span>
            <span className="text-white">{ship.nav.flightMode}</span>
          </div>
        </div>

        {/* Route Info (if in transit) */}
        {ship.nav.status === 'IN_TRANSIT' && (
          <div className="mt-3 p-2 bg-gray-750 rounded border border-gray-700">
            <div className="space-y-1 text-xs">
              <div className="flex justify-between">
                <span className="text-gray-400">From:</span>
                <span className="text-white">{ship.nav.route.origin.symbol}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-gray-400">To:</span>
                <span className="text-white">{ship.nav.route.destination.symbol}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-gray-400">Arrival:</span>
                <span className="text-white">{formatDate(ship.nav.route.arrival)}</span>
              </div>

              {/* Progress Bar */}
              <div className="mt-2">
                <div className="flex justify-between mb-1">
                  <span className="text-gray-500">Progress</span>
                  <span className="text-gray-400">{calculateProgress().toFixed(1)}%</span>
                </div>
                <div className="w-full bg-gray-700 rounded-full h-1.5">
                  <div
                    className="bg-orange-500 h-1.5 rounded-full transition-all"
                    style={{ width: `${calculateProgress()}%` }}
                  />
                </div>
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Cargo */}
      <div>
        <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Cargo</h3>
        <div className="space-y-2 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-400">Capacity:</span>
            <span className="text-white">
              {ship.cargo.units} / {ship.cargo.capacity}
            </span>
          </div>

          {/* Cargo Bar */}
          <div className="w-full bg-gray-700 rounded-full h-2">
            <div
              className="bg-blue-500 h-2 rounded-full transition-all"
              style={{ width: `${(ship.cargo.units / ship.cargo.capacity) * 100}%` }}
            />
          </div>

          {/* Inventory */}
          {ship.cargo.inventory.length > 0 && (
            <div className="mt-2 space-y-1">
              <div className="text-xs text-gray-500 uppercase">Inventory:</div>
              {ship.cargo.inventory.map((item, idx) => (
                <div key={idx} className="flex items-center justify-between text-xs bg-gray-750 p-1.5 rounded">
                  <div className="flex items-center gap-2">
                    <span className="text-base leading-none">{getCargoIcon(item.symbol)}</span>
                    <span className="text-gray-300">{getCargoLabel(item.symbol)}</span>
                  </div>
                  <span className="text-gray-400 font-semibold">×{item.units}</span>
                </div>
              ))}
              <div className="pt-2 border-t border-gray-700">
                <div className="text-xs text-gray-500 uppercase mb-1">Summary:</div>
                <ul className="text-xs text-gray-300 space-y-0.5">
                  {ship.cargo.inventory.slice(0, 3).map((item, idx) => (
                    <li key={`summary-${idx}`} className="flex items-center gap-1.5">
                      <span className="leading-none">{getCargoIcon(item.symbol)}</span>
                      <span>{getCargoShortLabel(item.symbol)}: {item.units}</span>
                    </li>
                  ))}
                  {ship.cargo.inventory.length > 3 && (
                    <li className="text-gray-500">
                      ...and {ship.cargo.inventory.length - 3} more
                    </li>
                  )}
                </ul>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Fuel */}
      <div>
        <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Fuel</h3>
        <div className="space-y-2 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-400">Level:</span>
            <span className="text-white">
              {ship.fuel.current} / {ship.fuel.capacity}
            </span>
          </div>

          {/* Fuel Bar */}
          <div className="w-full bg-gray-700 rounded-full h-2">
            <div
              className="bg-yellow-500 h-2 rounded-full transition-all"
              style={{ width: `${(ship.fuel.current / ship.fuel.capacity) * 100}%` }}
            />
          </div>
        </div>
      </div>

      {/* Cooldown */}
      {ship.cooldown && ship.cooldown.remainingSeconds > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Cooldown</h3>
          <div className="p-2 bg-gray-750 rounded border border-gray-700">
            <div className="flex justify-between text-sm">
              <span className="text-gray-400">Remaining:</span>
              <span className="text-orange-400 font-semibold">
                {ship.cooldown.remainingSeconds}s
              </span>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default ShipDetails;
