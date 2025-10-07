import { useCallback, useEffect, useRef, useState } from 'react';
import { useStore } from './store/useStore';
import { getAgents } from './services/api';
import { usePolling } from './hooks/usePolling';
import SpaceMap, { SpaceMapRef } from './components/SpaceMap';
import GalaxyView from './components/GalaxyView';
import AgentManager from './components/AgentManager';
import SystemSelector from './components/SystemSelector';
import ShipFilters from './components/ShipFilters';
import AddAgentCard from './components/AddAgentCard';
import ServerStatus from './components/ServerStatus';
import Sidebar from './components/Sidebar';
import Legend from './components/Legend';
import KeyboardShortcuts from './components/KeyboardShortcuts';

function App() {
  const { agents, setAgents, viewMode, setViewMode } = useStore();
  const spaceMapRef = useRef<SpaceMapRef>(null);
  const [isLeftSidebarOpen, setIsLeftSidebarOpen] = useState(true);
  const [isRightSidebarOpen, setIsRightSidebarOpen] = useState(true);
  const [rightSidebarTab, setRightSidebarTab] = useState<'ships' | 'details' | 'search'>('ships');

  const handleZoomIn = useCallback(() => {
    spaceMapRef.current?.zoomIn();
  }, []);

  const handleZoomOut = useCallback(() => {
    spaceMapRef.current?.zoomOut();
  }, []);

  const handleResetView = useCallback(() => {
    spaceMapRef.current?.resetView();
  }, []);

  const handleFitView = useCallback(() => {
    spaceMapRef.current?.fitView();
  }, []);

  const handleFocusOn = useCallback((x: number, y: number, scale?: number) => {
    spaceMapRef.current?.focusOn(x, y, scale);
  }, []);

  const handleToggleRightSidebar = useCallback(() => {
    setIsRightSidebarOpen((prev) => !prev);
  }, []);

  const handleSwitchRightSidebarTab = useCallback((tab: 'ships' | 'details' | 'search') => {
    setRightSidebarTab(tab);
    setIsRightSidebarOpen(true);
  }, []);

  // Load agents on mount
  useEffect(() => {
    getAgents()
      .then((agents) => {
        setAgents(agents);
      })
      .catch((error) => {
        console.error('Failed to load agents:', error);
      });
  }, [setAgents]);

  // Start polling for ship updates
  usePolling();

  // Show welcome screen if no agents
  if (agents.length === 0) {
    return (
      <>
        <ServerStatus />
        <div className="h-screen w-screen flex flex-col items-center justify-center bg-gray-900 text-white p-8">
          <div className="mb-8 text-center">
            <h1 className="text-4xl font-bold mb-2">🚀 SpaceTraders Fleet Visualization</h1>
            <p className="text-gray-400">Real-time tracking of your space fleet</p>
          </div>
          <AddAgentCard />
        </div>
      </>
    );
  }

  return (
    <>
      <ServerStatus />
      <div className="h-screen w-screen flex flex-col bg-gray-900 text-white">
        {/* Header */}
        <header className="bg-gray-800 border-b border-gray-700 p-4 flex items-center justify-between">
          <h1 className="text-2xl font-bold">SpaceTraders Fleet Visualization</h1>
          <div className="flex items-center gap-4">
            {/* View Mode Toggle */}
            <button
              onClick={() => setViewMode(viewMode === 'system' ? 'galaxy' : 'system')}
              className="px-4 py-2 bg-gray-700 hover:bg-gray-600 rounded transition-colors text-sm font-medium"
            >
              {viewMode === 'system' ? '🌌 Galaxy View' : '🗺️ System View'}
            </button>
            <SystemSelector />
            <AgentManager />
            <Legend buttonClassName="px-3 py-2 bg-gray-700 hover:bg-gray-600 rounded transition-colors text-sm font-semibold text-gray-200 border border-gray-600 flex items-center gap-2" />
            <KeyboardShortcuts
              onZoomIn={handleZoomIn}
              onZoomOut={handleZoomOut}
              onReset={handleResetView}
              onFitView={handleFitView}
              onToggleSidebar={handleToggleRightSidebar}
              onSwitchTab={handleSwitchRightSidebarTab}
              buttonClassName="px-3 py-2 bg-gray-700 hover:bg-gray-600 rounded transition-colors text-sm font-semibold text-gray-200 border border-gray-600 flex items-center gap-2"
            />
          </div>
        </header>

        {/* Main content */}
        <div className="flex-1 relative overflow-hidden">
          {/* Left Sidebar Toggle Button */}
          <button
            onClick={() => setIsLeftSidebarOpen(!isLeftSidebarOpen)}
            className={`fixed top-1/2 -translate-y-1/2 bg-gray-800 border-2 border-gray-700 rounded-r-lg p-2 shadow-lg hover:bg-gray-700 transition-all z-20 ${
              isLeftSidebarOpen ? 'left-56' : 'left-0'
            }`}
            title={isLeftSidebarOpen ? 'Close Filters' : 'Open Filters'}
          >
            <span className="text-lg">{isLeftSidebarOpen ? '←' : '→'}</span>
          </button>

          {/* Left Sidebar */}
          <aside
            className={`fixed left-0 top-[73px] bottom-0 w-56 bg-gray-800 border-r-2 border-gray-700 shadow-2xl transition-transform duration-300 z-10 ${
              isLeftSidebarOpen ? 'translate-x-0' : '-translate-x-full'
            }`}
          >
            {/* Header */}
            <div className="bg-gray-750 border-b-2 border-gray-700">
              <div className="px-4 py-3 text-sm font-semibold border-b-2 border-blue-500 text-blue-400">
                <span className="mr-2">⚙️</span>
                Filters
              </div>
            </div>

            {/* Content */}
            <div className="overflow-y-auto h-[calc(100%-57px)] p-4">
              <ShipFilters />
            </div>
          </aside>

          {/* Map */}
          <main className="w-full h-full">
            {viewMode === 'system' ? <SpaceMap ref={spaceMapRef} /> : <GalaxyView />}
            <Sidebar
              isOpen={isRightSidebarOpen}
              activeTab={rightSidebarTab}
              onToggleSidebar={handleToggleRightSidebar}
              onSwitchTab={handleSwitchRightSidebarTab}
              onFocusOn={handleFocusOn}
            />
          </main>
        </div>
      </div>
    </>
  );
}

export default App;
