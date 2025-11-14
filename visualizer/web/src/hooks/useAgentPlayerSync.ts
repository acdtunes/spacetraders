import { useEffect } from 'react';
import { useStore } from '../store/useStore';
import { getPlayerIdForAgent } from '../utils/agentHelpers';

/**
 * Automatically sync selectedPlayerId with the agent filter selection
 *
 * When exactly one agent is selected in filterAgents, automatically
 * set selectedPlayerId to that agent's player_id (looked up from playerMappings).
 *
 * When no agents or multiple agents are selected, clear selectedPlayerId
 * to show all tours.
 */
export function useAgentPlayerSync() {
  const {
    filterAgents,
    agents,
    playerMappings,
    selectedPlayerId,
    setSelectedPlayerId,
    toggleAgentFilter,
  } = useStore();

  useEffect(() => {
    console.log('[useAgentPlayerSync] Effect triggered', {
      filterAgentsSize: filterAgents.size,
      filterAgents: Array.from(filterAgents),
      currentSelectedPlayerId: selectedPlayerId,
      playerMappingsSize: playerMappings.size,
      agentsCount: agents.length,
    });

    // Case 1: No agents selected
    if (filterAgents.size === 0) {
      // Special case: If there's exactly ONE agent, auto-select it
      if (agents.length === 1 && playerMappings.size > 0) {
        const singleAgent = agents[0];
        console.log('[useAgentPlayerSync] Auto-selecting single agent:', singleAgent.symbol);
        toggleAgentFilter(singleAgent.id);
        return; // Will re-trigger with the agent selected
      }

      // Otherwise, clear player filter (show all)
      if (selectedPlayerId !== null) {
        console.log('[useAgentPlayerSync] Case 1: Clearing player filter (no agents selected)');
        setSelectedPlayerId(null);
      }
      return;
    }

    // Case 2: Multiple agents selected → clear player filter (show all)
    if (filterAgents.size > 1) {
      if (selectedPlayerId !== null) {
        console.log('[useAgentPlayerSync] Case 2: Clearing player filter (multiple agents selected)');
        setSelectedPlayerId(null);
      }
      return;
    }

    // Case 3: Exactly one agent selected → find and set its player_id
    const selectedAgentId = Array.from(filterAgents)[0];
    const selectedAgent = agents.find((a) => a.id === selectedAgentId);

    console.log('[useAgentPlayerSync] Case 3: Single agent selected', {
      selectedAgentId,
      selectedAgent: selectedAgent?.symbol,
    });

    if (!selectedAgent) {
      // Agent not found, clear player filter
      if (selectedPlayerId !== null) {
        console.log('[useAgentPlayerSync] Agent not found, clearing player filter');
        setSelectedPlayerId(null);
      }
      return;
    }

    // Look up player_id from playerMappings using agent symbol
    const playerId = getPlayerIdForAgent(selectedAgent.symbol, playerMappings);

    console.log('[useAgentPlayerSync] Looked up player_id', {
      agentSymbol: selectedAgent.symbol,
      foundPlayerId: playerId,
      currentSelectedPlayerId: selectedPlayerId,
      playerMappingsSize: playerMappings.size,
      mappingKeys: Array.from(playerMappings.keys()),
    });

    if (playerId !== null && playerId !== selectedPlayerId) {
      // Found player_id and it's different from current → update
      console.log('[useAgentPlayerSync] Setting selectedPlayerId to', playerId);
      setSelectedPlayerId(playerId);
    } else if (playerId === null && selectedPlayerId !== null) {
      // No player_id found for this agent → clear filter
      console.log('[useAgentPlayerSync] No player_id found, clearing filter');
      setSelectedPlayerId(null);
    }
  }, [filterAgents, agents, playerMappings, selectedPlayerId, setSelectedPlayerId, toggleAgentFilter]);
}
