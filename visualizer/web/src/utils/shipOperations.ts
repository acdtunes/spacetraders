import type { OperationType, ShipAssignment } from '../types/spacetraders';

/**
 * Get operation emoji for ship based on operation type
 */
export function getOperationEmoji(operationType: OperationType | null | undefined): string | null {
  if (!operationType) return null;

  const emojiMap: Record<OperationType, string> = {
    'scout-markets': '🔍',
    'trade': '📦',
    'mine': '⛏️',
    'transport': '🚚',
    'contract': '📜',
    'factory': '⚙️',
    'shipyard': '🏭',
    'arbitrage': '💹',
    'manufacturing': '🔧',
    'manual': '✋',
    'idle': '💤',
  };

  return emojiMap[operationType] || null;
}

/**
 * Get operation display name
 */
export function getOperationName(operationType: OperationType | null | undefined): string {
  if (!operationType) return 'Idle';

  const nameMap: Record<OperationType, string> = {
    'scout-markets': 'Scouting',
    'trade': 'Trading',
    'mine': 'Mining',
    'transport': 'Transport',
    'contract': 'Contract',
    'factory': 'Factory',
    'shipyard': 'Shipyard',
    'arbitrage': 'Arbitrage',
    'manufacturing': 'Manufacturing',
    'manual': 'Manual',
    'idle': 'Idle',
  };

  return nameMap[operationType] || 'Unknown';
}

/**
 * Get operation color for UI elements
 */
export function getOperationColor(operationType: OperationType | null | undefined): string {
  if (!operationType) return '#6B7280'; // gray-500

  const colorMap: Record<OperationType, string> = {
    'scout-markets': '#3B82F6', // blue-500
    'trade': '#10B981', // green-500
    'mine': '#F59E0B', // amber-500
    'transport': '#14B8A6', // teal-500
    'contract': '#8B5CF6', // purple-500
    'factory': '#EF4444', // red-500
    'shipyard': '#EC4899', // pink-500
    'arbitrage': '#06B6D4', // cyan-500
    'manufacturing': '#F97316', // orange-500
    'manual': '#A855F7', // violet-500
    'idle': '#6B7280', // gray-500
  };

  return colorMap[operationType] || '#6B7280';
}

/**
 * Get ship assignment from assignments map
 */
export function getShipOperation(
  shipSymbol: string,
  assignments: Map<string, ShipAssignment>
): ShipAssignment | null {
  return assignments.get(shipSymbol) || null;
}
