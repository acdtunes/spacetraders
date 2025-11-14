import { useState } from 'react';

interface LegendProps {
  className?: string;
  buttonClassName?: string;
}

const Legend = ({ className, buttonClassName }: LegendProps) => {
  const [isExpanded, setIsExpanded] = useState(false);

  const waypointTypes = [
    { type: 'PLANET', color: '#2980b9' },
    { type: 'GAS_GIANT', color: '#e67e22' },
    { type: 'MOON', color: '#95a5a6' },
    { type: 'ASTEROID', color: '#7f8c8d' },
    { type: 'ORBITAL_STATION', color: '#3498db' },
    { type: 'JUMP_GATE', color: '#9b59b6' },
    { type: 'FUEL_STATION', color: '#f1c40f' },
  ];

  const shipStatuses = [
    { status: 'IN_TRANSIT', color: '#f39c12', label: 'In Transit' },
    { status: 'DOCKED', color: '#27ae60', label: 'Docked' },
    { status: 'IN_ORBIT', color: '#3498db', label: 'In Orbit' },
  ];

  const features = [
    { icon: '⛏️', description: 'Ship is mining' },
    { description: 'Triangle = Ship' },
    { description: 'Circle = Waypoint' },
  ];

  const containerClass = ['relative', className].filter(Boolean).join(' ');
  const triggerClass =
    buttonClassName ??
    'px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-sm font-semibold text-gray-300 hover:text-white hover:bg-gray-700 transition-colors flex items-center gap-2';

  return (
    <div className={containerClass}>
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className={triggerClass}
      >
        <span className="font-bold text-sm">Legend</span>
        <span className="text-xs">{isExpanded ? '▲' : '▼'}</span>
      </button>

      {isExpanded && (
        <div className="absolute right-0 mt-2 w-64 bg-gray-800 border border-gray-700 rounded-lg shadow-2xl p-4 max-h-96 overflow-y-auto z-50">
          {/* Waypoint Types */}
          <div className="mb-4">
            <h3 className="text-xs font-bold text-gray-400 uppercase mb-2">Waypoints</h3>
            <div className="space-y-1">
              {waypointTypes.map(({ type, color }) => (
                <div key={type} className="flex items-center gap-2 text-sm">
                  <div className="w-3 h-3 rounded-full" style={{ backgroundColor: color }} />
                  <span className="text-xs">{type.replace(/_/g, ' ')}</span>
                </div>
              ))}
            </div>
          </div>

          {/* Ship Statuses */}
          <div className="mb-4">
            <h3 className="text-xs font-bold text-gray-400 uppercase mb-2">Ship Status</h3>
            <div className="space-y-1">
              {shipStatuses.map(({ status, color, label }) => (
                <div key={status} className="flex items-center gap-2 text-sm">
                  <div className="w-3 h-3 rounded-full" style={{ backgroundColor: color }} />
                  <span className="text-xs">{label}</span>
                </div>
              ))}
            </div>
          </div>

          {/* Features */}
          <div>
            <h3 className="text-xs font-bold text-gray-400 uppercase mb-2">Visual Guide</h3>
            <div className="space-y-1">
              {features.map(({ icon, description }, index) => (
                <div key={index} className="flex items-center gap-2 text-sm">
                  {icon && <span className="text-xs">{icon}</span>}
                  <span className="text-xs">{description}</span>
                </div>
              ))}
            </div>
          </div>

          {/* Controls Hint */}
          <div className="mt-4 pt-3 border-t border-gray-700">
            <h3 className="text-xs font-bold text-gray-400 uppercase mb-2">Controls</h3>
            <div className="space-y-1 text-xs text-gray-400">
              <div>• Drag to pan</div>
              <div>• Scroll to zoom</div>
              <div>• Click to select</div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default Legend;
