import { useStore } from '../store/useStore';

export function DateRangePicker() {
  const { financialDateRange, setFinancialDateRange } = useStore();

  const handlePresetChange = (preset: '24h' | '7d' | '30d' | 'all') => {
    const end = new Date();
    let start: Date;

    switch (preset) {
      case '24h':
        start = new Date(Date.now() - 24 * 60 * 60 * 1000);
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
  };

  return (
    <div className="flex items-center gap-2">
      <span className="text-sm text-gray-400">Period:</span>
      <div className="flex gap-1">
        {(['24h', '7d', '30d', 'all'] as const).map((preset) => (
          <button
            key={preset}
            onClick={() => handlePresetChange(preset)}
            className={`px-3 py-1 text-sm rounded transition-colors ${
              financialDateRange.preset === preset
                ? 'bg-blue-600 text-white'
                : 'bg-gray-700 text-gray-300 hover:bg-gray-600'
            }`}
          >
            {preset === 'all' ? 'All Time' : preset.toUpperCase()}
          </button>
        ))}
      </div>
    </div>
  );
}
