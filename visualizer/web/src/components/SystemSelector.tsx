import { useEffect, useMemo, useState } from 'react';
import { useStore } from '../store/useStore';

interface SystemSelectorProps {
  className?: string;
  buttonClassName?: string;
}

const SystemSelector = ({ className = '', buttonClassName = '' }: SystemSelectorProps) => {
  const { currentSystem, setCurrentSystem, systems: storeSystems } = useStore();
  const [isOpen, setIsOpen] = useState(false);

  const systems = useMemo(() => {
    // Use all systems from the store, not just those with ships
    const systemSymbols = storeSystems.map((system) => system.symbol);
    return systemSymbols.sort();
  }, [storeSystems]);

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
