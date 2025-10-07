import { useStore } from '../store/useStore';
import type { Ship as ShipType } from '../types/spacetraders';
import { Ship } from '../domain';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';

interface ShipListProps {
  onFocusOn: (x: number, y: number, scale?: number) => void;
}

const ShipList = ({ onFocusOn }: ShipListProps) => {
  const { ships, agents, selectedShip, setSelectedShip, filterStatus, filterAgents, currentSystem, waypoints } = useStore();

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'IN_TRANSIT': return 'bg-orange-500';
      case 'DOCKED': return 'bg-green-500';
      case 'IN_ORBIT': return 'bg-blue-500';
      default: return 'bg-gray-500';
    }
  };

  const getAgentColor = (ship: Ship & { agentId?: string; agentColor?: string }) => {
    if (ship.agentColor) return ship.agentColor;
    const agent = agents.find(a => a.id === ship.agentId);
    return agent?.color || '#6b7280';
  };

  // Filter ships
  const filteredShips = ships.filter((ship: Ship & { agentId?: string }) => {
    // Filter by current system
    if (currentSystem && ship.nav.systemSymbol !== currentSystem) return false;

    // Filter by status
    if (!filterStatus.has(ship.nav.status)) return false;

    // Filter by agent (only if filterAgents is not empty)
    if (filterAgents.size > 0 && ship.agentId && !filterAgents.has(ship.agentId)) {
      return false;
    }

    return true;
  });

  // Group ships by agent
  const shipsByAgent = filteredShips.reduce((acc, ship: Ship & { agentId?: string }) => {
    const agentId = ship.agentId || 'unknown';
    if (!acc[agentId]) acc[agentId] = [];
    acc[agentId].push(ship);
    return acc;
  }, {} as Record<string, Ship[]>);

  if (filteredShips.length === 0) {
    return (
      <div className="text-center text-gray-500 text-sm py-8">
        No ships match the current filters
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {Object.entries(shipsByAgent).map(([agentId, agentShips]) => {
        const agent = agents.find(a => a.id === agentId);

        return (
          <div key={agentId}>
            {/* Agent Header */}
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

            {/* Ship List */}
            <div className="space-y-1">
              {agentShips.map((ship) => (
                <button
                  key={ship.symbol}
                  onClick={() => {
                    setSelectedShip(ship);
                    const position = Ship.getPosition(ship, waypoints);
                    onFocusOn(position.x, position.y, VIEWPORT_CONSTANTS.MAX_ZOOM);
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
                      {/* Status Indicator */}
                      <div
                        className={`w-2 h-2 rounded-full ${getStatusColor(ship.nav.status)}`}
                        title={ship.nav.status}
                      />
                      {/* Mining Indicator */}
                      {ship.cooldown && ship.cooldown.remainingSeconds > 0 && (
                        <span className="text-xs">⛏️</span>
                      )}
                    </div>
                  </div>

                  {/* Cargo/Fuel Indicators */}
                  <div className="flex gap-2 mt-1.5">
                    {/* Cargo */}
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

                    {/* Fuel */}
                    <div className="flex-1">
                      <div className="flex justify-between text-xs text-gray-500 mb-0.5">
                        <span>Fuel</span>
                        <span>{ship.fuel.current}/{ship.fuel.capacity}</span>
                      </div>
                      <div className="w-full bg-gray-700 rounded-full h-1">
                        <div
                          className="bg-yellow-500 h-1 rounded-full"
                          style={{ width: `${(ship.fuel.current / ship.fuel.capacity) * 100}%` }}
                        />
                      </div>
                    </div>
                  </div>
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
