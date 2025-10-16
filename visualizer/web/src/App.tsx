import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react';
import { useStore } from './store/useStore';
import { getAgents } from './services/api';
import { usePolling } from './hooks/usePolling';
import { useBotPolling } from './hooks/useBotPolling';
import { useAgentPlayerSync } from './hooks/useAgentPlayerSync';
import ServerStatus from './components/ServerStatus';
import AgentCredits from './components/AgentCredits';
import type { SpaceMapRef } from './components/SpaceMap';
import LoaderScreen from './components/LoaderScreen';

const SpaceMap = lazy(() => import('./components/SpaceMap'));
const GalaxyView = lazy(() => import('./components/GalaxyView'));
const AgentManager = lazy(() => import('./components/AgentManager'));
const SystemSelector = lazy(() => import('./components/SystemSelector'));
const AddAgentCard = lazy(() => import('./components/AddAgentCard'));
const Sidebar = lazy(() => import('./components/Sidebar'));

function App() {
  const { agents, setAgents, viewMode, setViewMode, currentSystem, showScoutTours, toggleScoutTours } = useStore();
  const spaceMapRef = useRef<SpaceMapRef>(null);
  const [isRightSidebarOpen, setIsRightSidebarOpen] = useState(true);
  const [rightSidebarTab, setRightSidebarTab] = useState<'ships' | 'details' | 'search'>('ships');

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

  // Auto-sync player filter with agent filter selection
  useAgentPlayerSync();

  // Show welcome screen if no agents
  if (agents.length === 0) {
    return (
      <>
        <ServerStatus />
        <div className="h-screen w-screen flex flex-col items-center justify-center bg-black text-white p-8">
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
      <div className="h-screen w-screen flex flex-col bg-black text-white">
        {/* Header */}
        <header className="bg-gray-800 border-b border-gray-700 p-4 relative flex items-center justify-end">
          <div className="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 flex justify-center">
            <AgentCredits />
          </div>
          <div className="flex items-center gap-4">
            {/* View Mode Toggle */}
            <Suspense fallback={<div className="text-gray-500 text-sm">Agents…</div>}>
              <AgentManager />
            </Suspense>
            {/* Markets Toggle */}
            <button
              onClick={toggleScoutTours}
              className={`px-3 py-2 rounded transition-colors text-sm font-semibold border ${
                showScoutTours
                  ? 'bg-blue-600 hover:bg-blue-700 text-white border-blue-500'
                  : 'bg-gray-700 hover:bg-gray-600 text-gray-200 border-gray-600'
              }`}
              title="Toggle market overlays and tours"
            >
              🛒 Markets
            </button>
          </div>
        </header>

        {/* Main content */}
        <div className="flex-1 relative overflow-hidden">
          {/* Map */}
          <main className="w-full h-full">
            {viewMode === 'system' ? (
              currentSystem ? (
                <Suspense
                  fallback={
                    <LoaderScreen
                      title="Preparing Map"
                      message="Fetching system layout and ship telemetry"
                    />
                  }
                >
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
          </main>
        </div>
      </div>
    </>
  );
}

export default App;
