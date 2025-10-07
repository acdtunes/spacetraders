import { useStore } from '../store/useStore';

const ShipFilters = () => {
  const {
    showLabels,
    toggleLabels,
    showMarkets,
    toggleMarkets,
    filterStatus,
    toggleStatusFilter,
    filterAgents,
    toggleAgentFilter,
    filterWaypointTypes,
    toggleWaypointTypeFilter,
    selectAllWaypointTypes,
    clearAllWaypointTypes,
    waypoints,
    agents,
    isPolling,
    lastUpdate,
  } = useStore();

  const statusOptions = ['IN_TRANSIT', 'DOCKED', 'IN_ORBIT'];

  // Get unique waypoint types from current waypoints
  const waypointTypes = Array.from(new Set(
    Array.from(waypoints.values()).map(w => w.type)
  )).sort();

  return (
    <div className="space-y-4">
      {/* Connection Status */}
      <div>
        <div className="flex items-center gap-2">
          <div
            className={`w-2 h-2 rounded-full ${
              isPolling ? 'bg-green-500 animate-pulse' : 'bg-gray-500'
            }`}
          />
          <span className="text-xs">{isPolling ? 'Live' : 'Idle'}</span>
          {lastUpdate && (
            <span className="text-xs text-gray-500">
              {new Date(lastUpdate).toLocaleTimeString()}
            </span>
          )}
        </div>
      </div>

      {/* Display Options */}
      <div>
        <h3 className="font-semibold mb-1.5 text-xs uppercase text-gray-400">Display</h3>
        <div className="space-y-1.5">
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={showLabels}
              onChange={toggleLabels}
              className="w-3 h-3"
            />
            <span className="text-xs">Ship Labels</span>
          </label>
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={showMarkets}
              onChange={toggleMarkets}
              className="w-3 h-3"
            />
            <span className="text-xs">Markets</span>
          </label>
        </div>
      </div>

      {/* Filters */}
      <div>
        <h3 className="font-semibold mb-1.5 text-xs uppercase text-gray-400">Filters</h3>

        {/* Status */}
        <div className="space-y-1.5 mb-3">
          {statusOptions.map((status) => (
            <label key={status} className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={filterStatus.has(status)}
                onChange={() => toggleStatusFilter(status)}
                className="w-3 h-3"
              />
              <span className="text-xs">{status.replace('_', ' ')}</span>
            </label>
          ))}
        </div>

        {/* Agents */}
        {agents.length > 1 && (
          <div className="space-y-1.5 mb-3">
            {agents.map((agent) => (
              <label key={agent.id} className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={!filterAgents.has(agent.id)}
                  onChange={() => toggleAgentFilter(agent.id)}
                  className="w-3 h-3"
                />
                <div
                  className="w-2.5 h-2.5 rounded-full"
                  style={{ backgroundColor: agent.color }}
                />
                <span className="text-xs">{agent.symbol}</span>
              </label>
            ))}
          </div>
        )}
      </div>

      {/* Waypoint Types */}
      {waypointTypes.length > 0 && (
        <div>
          <label className="flex items-center gap-2 cursor-pointer mb-1.5">
            <input
              type="checkbox"
              checked={waypointTypes.every(type => filterWaypointTypes.has(type))}
              onChange={(e) => {
                if (e.target.checked) {
                  selectAllWaypointTypes(waypointTypes);
                } else {
                  clearAllWaypointTypes();
                }
              }}
              className="w-3 h-3"
            />
            <h3 className="font-semibold text-xs uppercase text-gray-400">
              Waypoints
            </h3>
          </label>
          <div className="space-y-1.5 ml-5">
            {waypointTypes.map((type) => (
              <div key={type} className="flex items-center gap-2">
                <label className="flex items-center gap-2 cursor-pointer flex-1">
                  <input
                    type="checkbox"
                    checked={filterWaypointTypes.has(type)}
                    onChange={() => toggleWaypointTypeFilter(type)}
                    className="w-3 h-3"
                  />
                  <span className="text-xs">{type.replace(/_/g, ' ')}</span>
                </label>
                <button
                  onClick={() => selectAllWaypointTypes([type])}
                  className="text-xs text-blue-400 hover:text-blue-300 px-1"
                  title="Show only this type"
                >
                  only
                </button>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
};

export default ShipFilters;
