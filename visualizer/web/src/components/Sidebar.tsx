import { useStore } from '../store/useStore';
import ShipList from './ShipList';
import ShipDetails from './ShipDetails';
import WaypointDetails from './WaypointDetails';
import Search from './Search';

interface SidebarProps {
  children?: React.ReactNode;
  isOpen: boolean;
  activeTab: 'ships' | 'details' | 'search';
  onToggleSidebar: () => void;
  onSwitchTab: (tab: 'ships' | 'details' | 'search') => void;
  onFocusOn: (x: number, y: number, scale?: number) => void;
}

const Sidebar = ({
  children,
  isOpen,
  activeTab,
  onToggleSidebar,
  onSwitchTab,
  onFocusOn,
}: SidebarProps) => {
  const { selectedShip, selectedWaypoint, currentSystem } = useStore();

  const tabs = [
    { id: 'ships' as const, label: 'Ships', icon: '🚀' },
    { id: 'details' as const, label: 'Details', icon: '📋' },
    { id: 'search' as const, label: 'Search', icon: '🔍' },
  ];

  return (
    <>
      {/* Toggle Button */}
      <button
        onClick={onToggleSidebar}
        className={`fixed top-1/2 -translate-y-1/2 bg-gray-800 border-2 border-gray-700 rounded-r-lg p-2 shadow-lg hover:bg-gray-700 transition-all z-20 ${
          isOpen ? 'left-80' : 'left-0'
        }`}
        title={isOpen ? 'Close Sidebar' : 'Open Sidebar'}
      >
        <span className="text-lg">{isOpen ? '←' : '→'}</span>
      </button>

      {/* Sidebar */}
      <div
        className={`fixed top-0 left-0 h-full bg-gray-800 border-r-2 border-gray-700 shadow-2xl transition-transform duration-300 z-10 ${
          isOpen ? 'translate-x-0' : '-translate-x-full'
        }`}
        style={{ width: '320px' }}
      >
        {/* Header with Tabs */}
        <div className="bg-gray-750 border-b-2 border-gray-700">
          {/* Current System Indicator */}
          {currentSystem && (
            <div className="px-4 py-2 border-b border-gray-700 bg-gray-800">
              <div className="text-xs text-gray-500 uppercase">Current System</div>
              <div className="text-sm font-semibold text-blue-400 truncate">{currentSystem}</div>
            </div>
          )}

          <div className="flex">
            {tabs.map((tab) => (
              <button
                key={tab.id}
                onClick={() => onSwitchTab(tab.id)}
                className={`flex-1 px-4 py-3 text-sm font-semibold transition-colors border-b-2 ${
                  activeTab === tab.id
                    ? 'bg-gray-700 border-blue-500 text-blue-400'
                    : 'bg-gray-750 border-transparent text-gray-400 hover:bg-gray-700 hover:text-gray-200'
                }`}
              >
                <span className="mr-2">{tab.icon}</span>
                {tab.label}
              </button>
            ))}
          </div>
        </div>

        {/* Tab Content */}
        <div className={`overflow-y-auto p-4 ${currentSystem ? 'h-[calc(100%-113px)]' : 'h-[calc(100%-57px)]'}`}>
          {activeTab === 'ships' && <ShipList />}

          {activeTab === 'details' && (
            <>
              {selectedShip && <ShipDetails ship={selectedShip} />}
              {selectedWaypoint && !selectedShip && <WaypointDetails waypoint={selectedWaypoint} />}
              {!selectedShip && !selectedWaypoint && (
                <div className="text-center text-gray-500 text-sm py-8">
                  Click on a ship or waypoint to view details
                </div>
              )}
            </>
          )}

          {activeTab === 'search' && <Search onFocusOn={onFocusOn} />}
          {children}
        </div>
      </div>

    </>
  );
};

export default Sidebar;
