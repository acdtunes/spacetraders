import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { Navigation } from './components/Navigation';
import { MapView } from './pages/MapView';
import { FinancialDashboard } from './pages/FinancialDashboard';

function App() {
  return (
    <BrowserRouter>
      <div className="h-screen w-screen flex flex-col">
        <Navigation />
        <div className="flex-1 overflow-hidden">
          <Routes>
            <Route path="/" element={<MapView />} />
            <Route path="/financial" element={<FinancialDashboard />} />
          </Routes>
        </div>
      </div>
    </BrowserRouter>
  );
}

export default App;
