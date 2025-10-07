import { useCallback, useEffect, useRef, useState } from 'react';
import { useStore } from './store/useStore';
import { getAgents } from './services/api';
import { usePolling } from './hooks/usePolling';
import SpaceMap, { SpaceMapRef } from './components/SpaceMap';
import GalaxyView from './components/GalaxyView';
import AgentManager from './components/AgentManager';
import SystemSelector from './components/SystemSelector';
import AddAgentCard from './components/AddAgentCard';
import ServerStatus from './components/ServerStatus';
import Sidebar from './components/Sidebar';
import Legend from './components/Legend';
import KeyboardShortcuts from './components/KeyboardShortcuts';

function App() {
  const { agents, setAgents, viewMode, setViewMode } = useStore();
  const spaceMapRef = useRef<SpaceMapRef>(null);
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
