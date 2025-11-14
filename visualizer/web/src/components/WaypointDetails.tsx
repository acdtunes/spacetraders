import { useState } from 'react';
import type { Waypoint } from '../types/spacetraders';
import MarketDetailsPanel from './MarketDetailsPanel';

interface WaypointDetailsProps {
  waypoint: Waypoint;
}

type WaypointTab = 'overview' | 'market';

const WaypointDetails = ({ waypoint }: WaypointDetailsProps) => {
  const [activeTab, setActiveTab] = useState<WaypointTab>('overview');

  const getTypeColor = (type: string) => {
    switch (type) {
      case 'PLANET': return 'text-blue-400';
      case 'GAS_GIANT': return 'text-orange-400';
      case 'MOON': return 'text-gray-400';
      case 'ASTEROID': return 'text-gray-500';
      case 'ASTEROID_FIELD': return 'text-gray-500';
      case 'ORBITAL_STATION': return 'text-cyan-400';
      case 'JUMP_GATE': return 'text-purple-400';
      case 'FUEL_STATION': return 'text-yellow-400';
      default: return 'text-gray-400';
    }
  };

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="border-b border-gray-700 pb-3">
        <h2 className="text-lg font-bold text-white">{waypoint.symbol}</h2>
        <p className={`text-sm font-semibold ${getTypeColor(waypoint.type)}`}>
          {waypoint.type.replace(/_/g, ' ')}
        </p>
      </div>

      {/* Tab Navigation */}
      {waypoint.hasMarketplace && (
        <div className="flex gap-2 border-b border-gray-700">
          <button
            onClick={() => setActiveTab('overview')}
            className={`px-3 py-2 text-sm font-semibold transition-colors ${
              activeTab === 'overview'
                ? 'text-white border-b-2 border-blue-500'
                : 'text-gray-400 hover:text-gray-300'
            }`}
          >
            Overview
          </button>
          <button
            onClick={() => setActiveTab('market')}
            className={`px-3 py-2 text-sm font-semibold transition-colors flex items-center gap-1 ${
              activeTab === 'market'
                ? 'text-white border-b-2 border-blue-500'
                : 'text-gray-400 hover:text-gray-300'
            }`}
          >
            <span>Market</span>
            <span className="text-xs">🏪</span>
          </button>
        </div>
      )}

      {/* Tab Content */}
      {activeTab === 'overview' && (
        <div className="space-y-4">
          {/* Location */}
      <div>
        <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Location</h3>
        <div className="space-y-1 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-400">System:</span>
            <span className="text-white text-xs">{waypoint.systemSymbol}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-gray-400">Coordinates:</span>
            <span className="text-white">({waypoint.x}, {waypoint.y})</span>
          </div>
        </div>
      </div>

      {/* Faction */}
      {waypoint.faction && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Faction</h3>
          <div className="text-sm">
            <span className="text-white">{waypoint.faction.symbol}</span>
          </div>
        </div>
      )}

      {/* Orbits */}
      {waypoint.orbits && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Orbits</h3>
          <div className="text-sm">
            <span className="text-white text-xs">{waypoint.orbits}</span>
          </div>
        </div>
      )}

      {/* Orbitals */}
      {waypoint.orbitals.length > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Orbitals</h3>
          <div className="space-y-1">
            {waypoint.orbitals.map((orbital, idx) => (
              <div key={idx} className="text-xs bg-gray-750 p-1.5 rounded text-gray-300">
                {orbital.symbol}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Traits */}
      {waypoint.traits.length > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2">Traits</h3>
          <div className="space-y-2">
            {waypoint.traits.map((trait, idx) => (
              <div key={idx} className="bg-gray-750 p-2 rounded border border-gray-700">
                <div className="font-semibold text-sm text-white">{trait.name}</div>
                <div className="text-xs text-gray-400 mt-1">{trait.description}</div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Status */}
      {waypoint.isUnderConstruction && (
        <div className="p-2 bg-yellow-900/20 border border-yellow-600/50 rounded">
          <div className="text-xs text-yellow-400 font-semibold">
            🚧 Under Construction
          </div>
        </div>
      )}

      {waypoint.hasMarketplace && (
        <div className="p-2 bg-green-900/20 border border-green-600/50 rounded">
          <div className="text-xs text-green-400 font-semibold">
            🏪 Marketplace Available
          </div>
        </div>
      )}
        </div>
      )}

      {/* Market Tab */}
      {activeTab === 'market' && waypoint.hasMarketplace && (
        <MarketDetailsPanel waypoint={waypoint} />
      )}
    </div>
  );
};

export default WaypointDetails;
