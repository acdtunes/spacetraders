import { useEffect, useState } from 'react';
import { useStore } from '../store/useStore';
import { getAgentShips } from '../services/api';

const SystemSelector = () => {
  const { agents, currentSystem, setCurrentSystem } = useStore();
  const [systems, setSystems] = useState<Set<string>>(new Set());
  const [isOpen, setIsOpen] = useState(false);

  // Discover systems from agent ships
  useEffect(() => {
    const fetchSystems = async () => {
      const systemSet = new Set<string>();

      for (const agent of agents.filter((a) => a.visible)) {
        try {
          const ships = await getAgentShips(agent.id);
          ships.forEach((ship) => {
            systemSet.add(ship.nav.systemSymbol);
          });
        } catch (error) {
          console.error(`Failed to fetch ships for agent ${agent.id}:`, error);
        }
      }

      setSystems(systemSet);

      // Auto-select first system if none selected
      if (!currentSystem && systemSet.size > 0) {
        setCurrentSystem(Array.from(systemSet)[0]);
      }
    };

    if (agents.length > 0) {
      fetchSystems();
    }
  }, [agents, currentSystem, setCurrentSystem]);

  const handleSelectSystem = (systemSymbol: string) => {
    setCurrentSystem(systemSymbol);
    setIsOpen(false);
  };

  return (
    <div className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded text-white font-medium flex items-center gap-2"
      >
        <span>🌌</span>
        <span>{currentSystem || 'Select System'}</span>
        <span>{isOpen ? '▲' : '▼'}</span>
      </button>

      {isOpen && (
        <div className="absolute top-full mt-2 right-0 bg-gray-800 border border-gray-700 rounded shadow-lg w-64 max-h-96 overflow-y-auto z-10">
          {systems.size === 0 ? (
            <div className="p-4 text-gray-400 text-center">No systems found</div>
          ) : (
            <div className="py-2">
              {Array.from(systems).map((system) => (
                <button
                  key={system}
                  onClick={() => handleSelectSystem(system)}
                  className={`w-full px-4 py-2 text-left hover:bg-gray-700 ${
                    currentSystem === system ? 'bg-blue-900 text-blue-200' : ''
                  }`}
                >
                  {system}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default SystemSelector;
