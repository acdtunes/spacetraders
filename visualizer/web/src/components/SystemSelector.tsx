import { useEffect, useMemo, useState } from 'react';
import { useStore } from '../store/useStore';

interface SystemSelectorProps {
  className?: string;
  buttonClassName?: string;
}

const SystemSelector = ({ className = '', buttonClassName = '' }: SystemSelectorProps) => {
  const { currentSystem, setCurrentSystem, ships } = useStore();
  const [isOpen, setIsOpen] = useState(false);

  const systems = useMemo(() => {
    const uniqueSystems = new Set<string>();

    ships.forEach((ship) => {
      if (ship.nav?.systemSymbol) {
        uniqueSystems.add(ship.nav.systemSymbol);
      }
    });

    if (uniqueSystems.size === 0 && currentSystem) {
      uniqueSystems.add(currentSystem);
    }

    return Array.from(uniqueSystems).sort();
  }, [ships, currentSystem]);

  useEffect(() => {
    if (systems.length === 0) {
      return;
    }

    if (!currentSystem || !systems.includes(currentSystem)) {
      setCurrentSystem(systems[0]);
    }
  }, [currentSystem, setCurrentSystem, systems]);

  const handleSelectSystem = (systemSymbol: string) => {
    setCurrentSystem(systemSymbol);
    setIsOpen(false);
  };

  return (
    <div className={`relative ${className}`}>
      <button
        onClick={() => setIsOpen(!isOpen)}
        className={`px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded text-white font-medium flex items-center gap-2 ${buttonClassName}`}
      >
        <span>🌌</span>
        <span>{currentSystem || 'Select System'}</span>
        <span>{isOpen ? '▲' : '▼'}</span>
      </button>

      {isOpen && (
        <div className="absolute top-full mt-2 right-0 bg-gray-800 border border-gray-700 rounded shadow-lg w-64 max-h-96 overflow-y-auto z-10">
          {systems.length === 0 ? (
            <div className="p-4 text-gray-400 text-center">No systems found</div>
          ) : (
            <div className="py-2">
              {systems.map((system) => (
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
