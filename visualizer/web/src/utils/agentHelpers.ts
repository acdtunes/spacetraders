import type { ShipAssignment } from '../types/spacetraders';

/**
 * Extract agent symbol from ship symbol
 * Example: "STORMWARDEN-1" -> "STORMWARDEN"
 * Example: "CMDR_AC_2025-3" -> "CMDR_AC_2025"
 */
export function getAgentSymbolFromShip(shipSymbol: string): string {
  const lastDashIndex = shipSymbol.lastIndexOf('-');
  if (lastDashIndex === -1) {
    return shipSymbol; // No dash found, return as-is
  }
  return shipSymbol.substring(0, lastDashIndex);
}

/**
 * Find player_id for an agent symbol using the player mappings from bot database
 * Returns null if no mapping found for this agent
 */
export function getPlayerIdForAgent(
  agentSymbol: string,
  playerMappings: Map<string, number>
): number | null {
  return playerMappings.get(agentSymbol) ?? null;
}

/**
 * Get all unique agent symbols from assignments (legacy function for compatibility)
 */
export function getAgentSymbolsFromAssignments(
  assignments: Map<string, ShipAssignment>
): string[] {
  const agentSymbols = new Set<string>();
  for (const assignment of assignments.values()) {
    const agentSymbol = getAgentSymbolFromShip(assignment.ship_symbol);
    agentSymbols.add(agentSymbol);
  }
  return Array.from(agentSymbols).sort();
}
