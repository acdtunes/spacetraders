import { memo } from 'react';
import { Group, Line, Circle, Text, Arrow } from 'react-konva';
import type { TradeOpportunityData, Waypoint as WaypointType } from '../types/spacetraders';

interface TradeRouteLayerProps {
  opportunities: TradeOpportunityData[];
  waypoints: Map<string, WaypointType>;
  currentScale: number;
  animationFrame: number;
}

/**
 * Get color based on profit per unit
 */
function getProfitColor(profitPerUnit: number): string {
  if (profitPerUnit < 100) return '#6B7280'; // gray - Low profit
  if (profitPerUnit < 500) return '#10B981'; // green - Good profit
  if (profitPerUnit < 1000) return '#F59E0B'; // amber - High profit
  return '#EF4444'; // red/pink - Excellent profit
}

/**
 * Format profit for display
 */
function formatProfit(profit: number): string {
  if (profit >= 1000) return `${(profit / 1000).toFixed(1)}k`;
  return profit.toString();
}

export const TradeRouteLayer = memo(function TradeRouteLayer({
  opportunities,
  waypoints,
  currentScale,
  animationFrame,
}: TradeRouteLayerProps) {
  if (opportunities.length === 0) return null;

  return (
    <Group listening={false}>
      {opportunities.map((opp, index) => {
        const buyWaypoint = waypoints.get(opp.buy_waypoint);
        const sellWaypoint = waypoints.get(opp.sell_waypoint);

        if (!buyWaypoint || !sellWaypoint) return null;

        const startX = buyWaypoint.x;
        const startY = buyWaypoint.y;
        const endX = sellWaypoint.x;
        const endY = sellWaypoint.y;

        // Calculate midpoint for label
        const midX = (startX + endX) / 2;
        const midY = (startY + endY) / 2;

        const color = getProfitColor(opp.profit_per_unit);
        const strokeWidth = Math.max(1.5, 3 / currentScale);
        const arrowSize = Math.max(3, 6 / currentScale);
        const fontSize = Math.max(8, 12 / currentScale);

        // Animated dash for flow effect
        const dashLength = 10 / currentScale;
        const gapLength = 5 / currentScale;
        const dashOffset = (animationFrame * 0.8) % (dashLength + gapLength);

        // Calculate angle for arrow rotation
        const dx = endX - startX;
        const dy = endY - startY;
        const angle = Math.atan2(dy, dx);

        // Position arrow at 75% along the line
        const arrowT = 0.75;
        const arrowX = startX + dx * arrowT;
        const arrowY = startY + dy * arrowT;

        return (
          <Group key={`trade-${index}-${opp.buy_waypoint}-${opp.sell_waypoint}`}>
            {/* Trade route line */}
            <Line
              points={[startX, startY, endX, endY]}
              stroke={color}
              strokeWidth={strokeWidth}
              opacity={0.7}
              dash={[dashLength, gapLength]}
              dashOffset={dashOffset}
              listening={false}
              lineCap="round"
            />

            {/* Direction arrow */}
            <Arrow
              x={arrowX}
              y={arrowY}
              rotation={(angle * 180) / Math.PI}
              points={[0, 0, arrowSize * 2, 0]}
              pointerLength={arrowSize}
              pointerWidth={arrowSize}
              fill={color}
              stroke={color}
              strokeWidth={strokeWidth * 0.8}
              opacity={0.8}
              listening={false}
            />

            {/* Buy marker */}
            <Circle
              x={startX}
              y={startY}
              radius={Math.max(2, 4 / currentScale)}
              fill={color}
              opacity={0.6}
              listening={false}
            />

            {/* Sell marker */}
            <Circle
              x={endX}
              y={endY}
              radius={Math.max(2, 4 / currentScale)}
              fill={color}
              opacity={0.6}
              listening={false}
            />

            {/* Profit label at midpoint */}
            <Group x={midX} y={midY - 8 / currentScale}>
              {/* Background for readability */}
              <Circle
                radius={Math.max(6, 10 / currentScale)}
                fill="rgba(17, 24, 39, 0.85)"
                listening={false}
              />
              <Text
                text={`${formatProfit(opp.profit_per_unit)}`}
                fontSize={fontSize}
                fill={color}
                align="center"
                verticalAlign="middle"
                offsetX={fontSize * 1.5}
                offsetY={fontSize / 2}
                listening={false}
              />
            </Group>

            {/* Good symbol label (smaller, above profit) */}
            {currentScale > 0.5 && (
              <Text
                text={opp.good_symbol.replace(/_/g, ' ')}
                x={midX}
                y={midY - 18 / currentScale}
                fontSize={fontSize * 0.7}
                fill="#9CA3AF"
                align="center"
                offsetX={(opp.good_symbol.length * fontSize * 0.7) / 3}
                listening={false}
                opacity={0.8}
              />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
