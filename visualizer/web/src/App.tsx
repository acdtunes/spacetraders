import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react';
import { useStore } from './store/useStore';
import { getAgents } from './services/api';
import { usePolling } from './hooks/usePolling';
import { useBotPolling } from './hooks/useBotPolling';
import ServerStatus from './components/ServerStatus';
import type { SpaceMapRef } from './components/SpaceMap';

const SpaceMap = lazy(() => import('./components/SpaceMap'));
const GalaxyView = lazy(() => import('./components/GalaxyView'));
const AgentManager = lazy(() => import('./components/AgentManager'));
const SystemSelector = lazy(() => import('./components/SystemSelector'));
const AddAgentCard = lazy(() => import('./components/AddAgentCard'));
const Sidebar = lazy(() => import('./components/Sidebar'));
const FleetOperationsSidebar = lazy(() => import('./components/FleetOperationsSidebar').then(m => ({ default: m.FleetOperationsSidebar })));

function App() {
  const { agents, setAgents, viewMode, setViewMode, currentSystem, assignments, showScoutTours, toggleScoutTours, showOperationBadges, toggleOperationBadges } = useStore();
  const spaceMapRef = useRef<SpaceMapRef>(null);
  const [isRightSidebarOpen, setIsRightSidebarOpen] = useState(true);
  const [rightSidebarTab, setRightSidebarTab] = useState<'ships' | 'details' | 'search'>('ships');
  const [isOperationsSidebarOpen, setIsOperationsSidebarOpen] = useState(false);

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

  // Start polling for bot operations
  useBotPolling();

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
          <Suspense fallback={<div className="text-gray-500">Loading…</div>}>
            <AddAgentCard />
          </Suspense>
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
            <Suspense fallback={<div className="text-gray-500 text-sm">Loading…</div>}>
              <SystemSelector />
            </Suspense>
            <Suspense fallback={<div className="text-gray-500 text-sm">Agents…</div>}>
              <AgentManager />
            </Suspense>
            {/* Scout Tours Toggle */}
            <button
              onClick={toggleScoutTours}
              className={`px-3 py-2 rounded transition-colors text-sm font-semibold border ${
                showScoutTours
                  ? 'bg-blue-600 hover:bg-blue-700 text-white border-blue-500'
                  : 'bg-gray-700 hover:bg-gray-600 text-gray-200 border-gray-600'
              }`}
              title="Toggle scout tour visualization"
            >
              🗺️ Tours
            </button>
            {/* Operations Toggle */}
            <button
              onClick={toggleOperationBadges}
              className={`px-3 py-2 rounded transition-colors text-sm font-semibold border ${
                showOperationBadges
                  ? 'bg-blue-600 hover:bg-blue-700 text-white border-blue-500'
                  : 'bg-gray-700 hover:bg-gray-600 text-gray-200 border-gray-600'
              }`}
              title="Toggle operation badges on ships"
            >
              🏷️ Operations
            </button>
          </div>
        </header>

        {/* Main content */}
        <div className="flex-1 relative overflow-hidden">
          {/* Map */}
          <main className="w-full h-full">
            {viewMode === 'system' ? (
              currentSystem ? (
                <Suspense fallback={<div className="w-full h-full flex items-center justify-center text-gray-500">Loading map…</div>}>
                  <SpaceMap ref={spaceMapRef} />
                </Suspense>
              ) : (
                <div className="w-full h-full flex flex-col items-center justify-center text-gray-400 gap-3">
                  <p className="text-sm">Select a system to load the map.</p>
                  <button
                    onClick={() => setViewMode('galaxy')}
                    className="px-4 py-2 bg-gray-800 border border-gray-600 rounded text-sm hover:bg-gray-700"
                  >
                    Browse Galaxy
                  </button>
                </div>
              )
            ) : (
              <Suspense fallback={<div className="w-full h-full flex items-center justify-center text-gray-500">Loading galaxy…</div>}>
                <GalaxyView />
              </Suspense>
            )}
            <Suspense fallback={null}>
              <Sidebar
                isOpen={isRightSidebarOpen}
                activeTab={rightSidebarTab}
                onToggleSidebar={handleToggleRightSidebar}
                onSwitchTab={handleSwitchRightSidebarTab}
                onFocusOn={handleFocusOn}
              />
            </Suspense>
            <Suspense fallback={null}>
              <FleetOperationsSidebar
                assignments={assignments}
                isVisible={isOperationsSidebarOpen}
                onToggle={() => setIsOperationsSidebarOpen(!isOperationsSidebarOpen)}
              />
            </Suspense>
          </main>
        </div>
      </div>
    </>
  );
}

export default App;
