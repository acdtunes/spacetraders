import { Link, useLocation } from 'react-router-dom';
import { Suspense, lazy } from 'react';
import AgentCredits from './AgentCredits';

const AgentManager = lazy(() => import('./AgentManager'));

export function Navigation() {
  const location = useLocation();

  return (
    <nav className="bg-gray-800 border-b border-gray-700 px-4 py-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-6">
          <div className="text-white font-bold text-lg">
            SpaceTraders Fleet
          </div>
          <div className="flex gap-2">
            <Link
              to="/"
              className={`px-4 py-2 rounded transition-colors ${
                location.pathname === '/'
                  ? 'bg-blue-600 text-white'
                  : 'text-gray-300 hover:bg-gray-700'
              }`}
            >
              Map
            </Link>
            <Link
              to="/financial"
              className={`px-4 py-2 rounded transition-colors ${
                location.pathname === '/financial'
                  ? 'bg-blue-600 text-white'
                  : 'text-gray-300 hover:bg-gray-700'
              }`}
            >
              Financial
            </Link>
          </div>
        </div>
        <div className="flex items-center gap-4">
          <AgentCredits />
          <Suspense fallback={<div className="text-gray-500 text-sm">Agents…</div>}>
            <AgentManager />
          </Suspense>
        </div>
      </div>
    </nav>
  );
}
