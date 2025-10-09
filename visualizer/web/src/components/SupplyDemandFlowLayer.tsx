import { memo } from 'react';
import { Group, Circle, Line } from 'react-konva';
import type { TradeOpportunityData, Waypoint as WaypointType, MarketSupply } from '../types/spacetraders';

interface SupplyDemandFlowLayerProps {
  opportunities: TradeOpportunityData[];
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
}

interface MarketActivity {
  symbol: string;
  exportCount: number;
  importCount: number;
  totalVolume: number;
}

/**
 * Get color based on market activity type
 */
function getActivityColor(exportCount: number, importCount: number): string {
  if (exportCount > importCount) return '#10B981'; // Green - Net exporter (supplier)
  if (importCount > exportCount) return '#3B82F6'; // Blue - Net importer (buyer)
  return '#6B7280'; // Gray - Balanced
}

/**
 * Get glow intensity based on volume
 */
function getGlowIntensity(volume: number): number {
  if (volume > 10) return 1.0;
  if (volume > 5) return 0.7;
  if (volume > 2) return 0.5;
  return 0.3;
}

/**
 * Get supply strength based on supply level
 */
function getSupplyStrength(supply: MarketSupply): number {
  const levels: Record<MarketSupply, number> = {
    'SCARCE': 1,
    'LIMITED': 2,
    'MODERATE': 3,
    'HIGH': 4,
    'ABUNDANT': 5,
  };
  return levels[supply] || 3;
}

export const SupplyDemandFlowLayer = memo(function SupplyDemandFlowLayer({
  opportunities,
  waypoints,
  currentScale,
  animationFrame,
}: SupplyDemandFlowLayerProps) {
  if (opportunities.length === 0) return null;

  // Aggregate market activity
  const marketActivity = new Map<string, MarketActivity>();

  opportunities.forEach((opp) => {
    // Export activity (selling)
    const buyMarket = marketActivity.get(opp.buy_waypoint) || {
      symbol: opp.buy_waypoint,
      exportCount: 0,
      importCount: 0,
      totalVolume: 0,
    };
    buyMarket.exportCount += 1;
    buyMarket.totalVolume += getSupplyStrength(opp.supply);
    marketActivity.set(opp.buy_waypoint, buyMarket);

    // Import activity (buying)
    const sellMarket = marketActivity.get(opp.sell_waypoint) || {
      symbol: opp.sell_waypoint,
      exportCount: 0,
      importCount: 0,
      totalVolume: 0,
    };
    sellMarket.importCount += 1;
    sellMarket.totalVolume += 1;
    marketActivity.set(opp.sell_waypoint, sellMarket);
  });

  // Animated pulse effect
  const pulsePhase = (animationFrame % 60) / 60;
  const pulseScale = 1 + Math.sin(pulsePhase * Math.PI * 2) * 0.3;

  return (
    <Group listening={false}>
      {/* Market activity indicators */}
      {Array.from(marketActivity.entries()).map(([symbol, activity]) => {
        const waypoint = waypoints.get(symbol);
        if (!waypoint) return null;

        const color = getActivityColor(activity.exportCount, activity.importCount);
        const intensity = getGlowIntensity(activity.totalVolume);
        const baseRadius = Math.max(4, 8 / currentScale);
        const glowRadius = baseRadius * pulseScale * 1.5;

        return (
          <Group key={`activity-${symbol}`}>
            {/* Outer glow ring */}
            <Circle
              x={waypoint.x}
              y={waypoint.y}
              radius={glowRadius}
              fill={color}
              opacity={intensity * 0.2}
              listening={false}
            />

            {/* Middle ring */}
            <Circle
              x={waypoint.x}
              y={waypoint.y}
              radius={baseRadius * 1.3}
              stroke={color}
              strokeWidth={Math.max(1, 2 / currentScale)}
              opacity={intensity * 0.5}
              listening={false}
            />

            {/* Inner core */}
            <Circle
              x={waypoint.x}
              y={waypoint.y}
              radius={baseRadius}
              fill={color}
              opacity={intensity * 0.8}
              listening={false}
            />
          </Group>
        );
      })}

      {/* Flow lines between active markets */}
      {opportunities.slice(0, 15).map((opp, index) => {
        const buyWaypoint = waypoints.get(opp.buy_waypoint);
        const sellWaypoint = waypoints.get(opp.sell_waypoint);

        if (!buyWaypoint || !sellWaypoint) return null;

        const activity = marketActivity.get(opp.buy_waypoint);
        if (!activity) return null;

        const color = getActivityColor(activity.exportCount, activity.importCount);

        // Animated flowing particles
        const flowProgress = ((animationFrame * 0.02) + (index * 0.1)) % 1;
        const particleX = buyWaypoint.x + (sellWaypoint.x - buyWaypoint.x) * flowProgress;
        const particleY = buyWaypoint.y + (sellWaypoint.y - buyWaypoint.y) * flowProgress;

        return (
          <Group key={`flow-${index}-${opp.buy_waypoint}-${opp.sell_waypoint}`}>
            {/* Faint connection line */}
            <Line
              points={[buyWaypoint.x, buyWaypoint.y, sellWaypoint.x, sellWaypoint.y]}
              stroke={color}
              strokeWidth={Math.max(0.5, 1 / currentScale)}
              opacity={0.15}
              listening={false}
            />

            {/* Animated particle */}
            <Circle
              x={particleX}
              y={particleY}
              radius={Math.max(1, 2 / currentScale)}
              fill={color}
              opacity={0.8}
              listening={false}
            />
          </Group>
        );
      })}
    </Group>
  );
});
