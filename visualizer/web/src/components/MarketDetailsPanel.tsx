import { useMemo } from 'react';
import { useStore } from '../store/useStore';
import type { Waypoint, MarketGood, MarketSupply } from '../types/spacetraders';

interface MarketDetailsPanelProps {
  waypoint: Waypoint;
}

const formatTimeAgo = (timestamp: string) => {
  const diffMs = Date.now() - new Date(timestamp).getTime();
  if (Number.isNaN(diffMs)) {
    return 'unknown';
  }

  const diffMinutes = Math.floor(diffMs / 60000);
  if (diffMinutes < 1) return 'just now';
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  return `${diffDays}d ago`;
};

const getSupplyColor = (supply: MarketSupply): string => {
  switch (supply) {
    case 'SCARCE':
      return 'text-red-400';
    case 'LIMITED':
      return 'text-orange-400';
    case 'MODERATE':
      return 'text-yellow-400';
    case 'HIGH':
      return 'text-green-400';
    case 'ABUNDANT':
      return 'text-emerald-400';
    default:
      return 'text-gray-400';
  }
};

const formatSupplyLabel = (supply: MarketSupply): string =>
  supply
    .toLowerCase()
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (char) => char.toUpperCase());

const getGoodIcon = (symbol: string): string => {
  // Map trade good symbols to emoji icons
  const iconMap: Record<string, string> = {
    // Ores & Raw Materials
    PRECIOUS_STONES: '💎',
    QUARTZ_SAND: '⏳',
    SILICON_CRYSTALS: '🔮',
    IRON_ORE: '⛏️',
    COPPER_ORE: '🟠',
    ALUMINUM_ORE: '⚪',
    SILVER_ORE: '🪙',
    GOLD_ORE: '🟡',
    PLATINUM_ORE: '💿',
    URANITE_ORE: '☢️',
    MERITIUM_ORE: '✨',
    DIAMONDS: '💎',

    // Energy & Fuel
    FUEL: '⛽',
    LIQUID_HYDROGEN: '💧',
    LIQUID_NITROGEN: '❄️',
    ICE_WATER: '🧊',
    ANTIMATTER: '⚛️',
    REACTOR_SOLAR_I: '☀️',
    REACTOR_FUSION_I: '⚛️',
    REACTOR_FISSION_I: '☢️',
    REACTOR_CHEMICAL_I: '⚗️',
    REACTOR_ANTIMATTER_I: '⚛️',
    MICRO_FUSION_GENERATORS: '⚡',

    // Food & Agriculture
    FOOD: '🍎',
    SUPERGRAINS: '🌾',
    FERTILIZERS: '🌱',
    HYDROPONICS: '🌿',

    // Manufactured Goods
    CLOTHING: '👕',
    FABRICS: '🧵',
    TEXTILES: '🧶',
    EQUIPMENT: '🔧',
    MACHINERY: '⚙️',
    TOOLS: '🔨',

    // Electronics & Tech
    ELECTRONICS: '💻',
    ADVANCED_CIRCUITRY: '🔌',
    AI_MAINFRAMES: '🧠',
    QUANTUM_DRIVES: '⚛️',
    NEURAL_CHIPS: '🧠',
    CYBER_IMPLANTS: '🦾',
    MICRO_PROCESSORS: '💾',

    // Weapons & Military
    FIREARMS: '🔫',
    AMMUNITION: '💣',
    EXPLOSIVES: '💥',
    MILITARY_EQUIPMENT: '🎖️',
    ASSAULT_RIFLES: '🔫',

    // Medical & Pharmaceuticals
    PHARMACEUTICALS: '💊',
    MEDICINE: '💊',
    DRUGS: '💉',
    MOOD_REGULATORS: '💊',
    GENE_THERAPEUTICS: '🧬',
    VIRAL_AGENTS: '🦠',

    // Scientific & Research
    LAB_INSTRUMENTS: '🔬',
    POLYNUCLEOTIDES: '🧬',
    BIOCOMPOSITES: '🦠',
    NANOBOTS: '🤖',
    ROBOTIC_DRONES: '🤖',

    // Exotic & Special
    EXOTIC_MATTER: '✨',
    GRAVITON_EMITTERS: '🌀',
    QUANTUM_STABILIZERS: '⚛️',

    // Construction & Building
    PLASTEEL: '🏗️',
    BUILDING_MATERIALS: '🧱',
    SHIP_PARTS: '🚀',
    SHIP_PLATING: '🛡️',
  };

  return iconMap[symbol] || '📦';
};

interface GoodCardProps {
  good: MarketGood & { spread: number };
}

const GoodCard = ({ good }: GoodCardProps) => {
  return (
    <div className="bg-gray-800 border border-gray-700 rounded p-3 space-y-2">
      {/* Good Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-lg">{getGoodIcon(good.symbol)}</span>
          <h4 className="text-sm font-bold text-white">{good.symbol}</h4>
        </div>
        {good.spread > 0 && (
          <span className="text-xs font-mono text-emerald-400">
            +{good.spread.toLocaleString()}₡
          </span>
        )}
      </div>

      {/* Prices */}
      <div className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <div className="text-gray-500">Buy</div>
          <div className="text-white font-mono">{good.purchasePrice.toLocaleString()}₡</div>
        </div>
        <div>
          <div className="text-gray-500">Sell</div>
          <div className="text-white font-mono">{good.sellPrice.toLocaleString()}₡</div>
        </div>
      </div>

      {/* Supply & Activity */}
      <div className="flex items-center justify-between text-xs">
        <div>
          <span className="text-gray-500">Supply: </span>
          <span className={`font-semibold ${getSupplyColor(good.supply)}`}>
            {formatSupplyLabel(good.supply)}
          </span>
        </div>
        {good.activity && (
          <div className="text-gray-400 text-[10px] uppercase">
            {good.activity}
          </div>
        )}
      </div>

      {/* Trade Volume */}
      <div className="text-xs">
        <span className="text-gray-500">Volume: </span>
        <span className="text-white">{good.tradeVolume.toLocaleString()}</span>
      </div>
    </div>
  );
};

const MarketDetailsPanel = ({ waypoint }: MarketDetailsPanelProps) => {
  const { marketIntel, marketFreshness, markets } = useStore();

  const marketData = marketIntel.get(waypoint.symbol);
  const freshness = marketFreshness.get(waypoint.symbol);
  const market = markets.get(waypoint.symbol);

  // Categorize goods by imports, exports, and exchange
  const categorizedGoods = useMemo(() => {
    if (!marketData || !marketData.goods.length) {
      return { imports: [], exports: [], exchange: [] };
    }

    // Get import/export symbols from the market object
    const importSymbols = new Set(
      market?.imports?.map((i) => i.symbol) || []
    );
    const exportSymbols = new Set(
      market?.exports?.map((e) => e.symbol) || []
    );

    // Add spread calculation and sort by spread
    const goodsWithSpread = marketData.goods.map((good) => ({
      ...good,
      spread: good.sellPrice - good.purchasePrice,
    })).sort((a, b) => b.spread - a.spread);

    const imports: typeof goodsWithSpread = [];
    const exports: typeof goodsWithSpread = [];
    const exchange: typeof goodsWithSpread = [];

    goodsWithSpread.forEach((good) => {
      if (importSymbols.has(good.symbol)) {
        imports.push(good);
      } else if (exportSymbols.has(good.symbol)) {
        exports.push(good);
      } else {
        exchange.push(good);
      }
    });

    return { imports, exports, exchange };
  }, [marketData, market]);

  if (!marketData || marketData.goods.length === 0) {
    return (
      <div className="space-y-4">
        <div className="p-4 bg-gray-800/50 border border-gray-700 rounded">
          <div className="text-center space-y-2">
            <div className="text-4xl">🏪</div>
            <h3 className="text-sm font-semibold text-gray-400">No Market Data Available</h3>
            <p className="text-xs text-gray-500">
              Market intelligence not yet collected for this waypoint.
            </p>
            {freshness && (
              <p className="text-xs text-gray-600">
                Last checked: {formatTimeAgo(freshness.last_updated)}
              </p>
            )}
          </div>
        </div>
      </div>
    );
  }

  const isStale = freshness && (Date.now() - new Date(freshness.last_updated).getTime()) > 3600000; // 1 hour

  return (
    <div className="space-y-4">
      {/* Freshness Header */}
      <div className={`p-2 rounded border ${isStale ? 'bg-yellow-900/20 border-yellow-600/50' : 'bg-gray-800/50 border-gray-700'}`}>
        <div className="flex items-center justify-between text-xs">
          <span className="text-gray-400">Market Data</span>
          <span className={isStale ? 'text-yellow-400' : 'text-green-400'}>
            {isStale && '⚠️ '}Updated {formatTimeAgo(marketData.lastUpdated)}
          </span>
        </div>
      </div>

      {/* Imports Section */}
      {categorizedGoods.imports.length > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2 flex items-center gap-2">
            <span>📥 Imports</span>
            <span className="text-sky-400">({categorizedGoods.imports.length})</span>
          </h3>
          <div className="grid grid-cols-1 gap-2">
            {categorizedGoods.imports.map((good) => (
              <GoodCard key={good.symbol} good={good} />
            ))}
          </div>
        </div>
      )}

      {/* Exports Section */}
      {categorizedGoods.exports.length > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2 flex items-center gap-2">
            <span>📤 Exports</span>
            <span className="text-rose-400">({categorizedGoods.exports.length})</span>
          </h3>
          <div className="grid grid-cols-1 gap-2">
            {categorizedGoods.exports.map((good) => (
              <GoodCard key={good.symbol} good={good} />
            ))}
          </div>
        </div>
      )}

      {/* Exchange Section */}
      {categorizedGoods.exchange.length > 0 && (
        <div>
          <h3 className="text-xs font-bold text-gray-500 uppercase mb-2 flex items-center gap-2">
            <span>🔄 Exchange</span>
            <span className="text-amber-400">({categorizedGoods.exchange.length})</span>
          </h3>
          <div className="grid grid-cols-1 gap-2">
            {categorizedGoods.exchange.map((good) => (
              <GoodCard key={good.symbol} good={good} />
            ))}
          </div>
        </div>
      )}

      {/* Summary */}
      <div className="p-3 bg-gray-800/30 border border-gray-700 rounded text-xs">
        <div className="flex items-center justify-between">
          <span className="text-gray-400">Total Goods</span>
          <span className="text-white font-semibold">{marketData.goods.length}</span>
        </div>
      </div>
    </div>
  );
};

export default MarketDetailsPanel;
