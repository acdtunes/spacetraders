import { useStore } from '../store/useStore';
import { useState } from 'react';

type PresetType = '5m' | '15m' | '30m' | '1h' | '3h' | '6h' | '12h' | '24h' | '2d' | '7d' | '30d' | 'all';

export function DateRangePicker() {
  const { financialDateRange, setFinancialDateRange } = useStore();
  const [showDropdown, setShowDropdown] = useState(false);
  const [showCustom, setShowCustom] = useState(false);
  const [customFrom, setCustomFrom] = useState('now-24h');
  const [customTo, setCustomTo] = useState('now');

  const mainPresets: PresetType[] = ['24h', '7d', '30d', 'all'];
  const allPresets: { value: PresetType; label: string }[] = [
    { value: '5m', label: 'Last 5 minutes' },
    { value: '15m', label: 'Last 15 minutes' },
    { value: '30m', label: 'Last 30 minutes' },
    { value: '1h', label: 'Last 1 hour' },
    { value: '3h', label: 'Last 3 hours' },
    { value: '6h', label: 'Last 6 hours' },
    { value: '12h', label: 'Last 12 hours' },
    { value: '24h', label: 'Last 24 hours' },
    { value: '2d', label: 'Last 2 days' },
    { value: '7d', label: 'Last 7 days' },
    { value: '30d', label: 'Last 30 days' },
    { value: 'all', label: 'All Time' },
  ];

  const parseRelativeTime = (expr: string): Date => {
    if (expr === 'now') return new Date();

    const match = expr.match(/^now-(\d+)([smhd])$/);
    if (!match) return new Date();

    const value = parseInt(match[1]);
    const unit = match[2];
    const now = Date.now();

    switch (unit) {
      case 's': return new Date(now - value * 1000);
      case 'm': return new Date(now - value * 60 * 1000);
      case 'h': return new Date(now - value * 60 * 60 * 1000);
      case 'd': return new Date(now - value * 24 * 60 * 60 * 1000);
      default: return new Date();
    }
  };

  const handlePresetChange = (preset: PresetType) => {
    const end = new Date();
    let start: Date;

    switch (preset) {
      case '5m':
        start = new Date(Date.now() - 5 * 60 * 1000);
        break;
      case '15m':
        start = new Date(Date.now() - 15 * 60 * 1000);
        break;
      case '30m':
        start = new Date(Date.now() - 30 * 60 * 1000);
        break;
      case '1h':
        start = new Date(Date.now() - 60 * 60 * 1000);
        break;
      case '3h':
        start = new Date(Date.now() - 3 * 60 * 60 * 1000);
        break;
      case '6h':
        start = new Date(Date.now() - 6 * 60 * 60 * 1000);
        break;
      case '12h':
        start = new Date(Date.now() - 12 * 60 * 60 * 1000);
        break;
      case '24h':
        start = new Date(Date.now() - 24 * 60 * 60 * 1000);
        break;
      case '2d':
        start = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000);
        break;
      case '7d':
        start = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
        break;
      case '30d':
        start = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000);
        break;
      case 'all':
        start = new Date(0);
        break;
    }

    setFinancialDateRange({ start, end, preset });
    setShowDropdown(false);
  };

  const handleCustomApply = () => {
    const start = parseRelativeTime(customFrom);
    const end = parseRelativeTime(customTo);
    setFinancialDateRange({ start, end, preset: 'custom' });
    setShowCustom(false);
  };

  const getPresetLabel = (preset: PresetType) => {
    const found = allPresets.find(p => p.value === preset);
    return found ? found.label : preset.toUpperCase();
  };

  const getCurrentLabel = () => {
    const found = allPresets.find(p => p.value === financialDateRange.preset);
    if (found) return found.label;
    if (financialDateRange.preset === 'custom') return 'Custom range';
    return 'Select range';
  };

  return (
    <div className="flex items-center gap-3 relative">
      <span className="text-gray-400 text-base">Period:</span>
      <div className="flex gap-2">
        {/* Time Range Dropdown */}
        <div className="relative">
          <button
            onClick={() => setShowDropdown(!showDropdown)}
            className="px-5 py-2 text-sm font-medium rounded-lg bg-blue-600 text-white hover:bg-blue-700 transition-colors min-w-[150px] text-left flex items-center justify-between"
          >
            <span>{getCurrentLabel()}</span>
            <span className="ml-2">▼</span>
          </button>

          {showDropdown && (
            <>
              <div
                className="fixed inset-0 z-10"
                onClick={() => setShowDropdown(false)}
              />
              <div className="absolute left-0 mt-2 w-56 bg-gray-800 border border-gray-700 rounded-lg shadow-lg z-20 max-h-96 overflow-y-auto">
                {allPresets.map((preset) => (
                  <button
                    key={preset.value}
                    onClick={() => handlePresetChange(preset.value)}
                    className={`w-full text-left px-4 py-3 text-sm hover:bg-gray-700 transition-colors border-b border-gray-700 last:border-b-0 ${
                      financialDateRange.preset === preset.value
                        ? 'bg-gray-700 text-blue-400'
                        : 'text-gray-300'
                    }`}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            </>
          )}
        </div>

        {/* Custom Time Range Button */}
        <button
          onClick={() => setShowCustom(!showCustom)}
          className="px-4 py-2 text-sm font-medium rounded-lg bg-gray-700 text-gray-300 hover:bg-gray-600 transition-colors"
        >
          Custom
        </button>
      </div>

      {/* Custom Time Range Panel */}
      {showCustom && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setShowCustom(false)}
          />
          <div className="absolute right-0 mt-2 w-96 bg-gray-800 border border-gray-700 rounded-lg shadow-lg z-20 p-4">
            <h3 className="text-lg font-semibold mb-4 text-gray-200">Absolute time range</h3>

            <div className="space-y-4">
              <div>
                <label className="block text-sm text-gray-400 mb-1">From</label>
                <input
                  type="text"
                  value={customFrom}
                  onChange={(e) => setCustomFrom(e.target.value)}
                  placeholder="now-5m"
                  className="w-full px-3 py-2 bg-gray-900 border border-gray-600 rounded text-white text-sm focus:outline-none focus:border-blue-500"
                />
              </div>

              <div>
                <label className="block text-sm text-gray-400 mb-1">To</label>
                <input
                  type="text"
                  value={customTo}
                  onChange={(e) => setCustomTo(e.target.value)}
                  placeholder="now"
                  className="w-full px-3 py-2 bg-gray-900 border border-gray-600 rounded text-white text-sm focus:outline-none focus:border-blue-500"
                />
              </div>

              <button
                onClick={handleCustomApply}
                className="w-full px-4 py-2 bg-blue-600 text-white font-medium rounded-lg hover:bg-blue-700 transition-colors"
              >
                Apply time range
              </button>

              <div className="text-xs text-gray-500 mt-2">
                <p>Examples: now-5m, now-1h, now-24h, now-7d</p>
                <p>Units: s (seconds), m (minutes), h (hours), d (days)</p>
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
