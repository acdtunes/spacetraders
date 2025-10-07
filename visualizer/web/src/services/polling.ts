import { getAgentShips, getAgents } from './api';
import type { TaggedShip, Agent } from '../types/spacetraders';
import { API_CONSTANTS } from '../constants/api';

class ShipPollingService {
  private intervalId: number | null = null;
  private isRunning = false;

  async fetchAllShips(agents: Agent[]): Promise<TaggedShip[]> {
    const allShips: TaggedShip[] = [];
    const visibleAgents = agents.filter((a) => a.visible);

    for (const agent of visibleAgents) {
      try {
        const ships = await getAgentShips(agent.id);
        // Tag ships with agent info
        const taggedShips: TaggedShip[] = ships.map((ship) => ({
          ...ship,
          agentId: agent.id,
          agentColor: agent.color,
        }));
        allShips.push(...taggedShips);

        // Rate limit: delay between agents
        if (visibleAgents.indexOf(agent) < visibleAgents.length - 1) {
          await new Promise((resolve) => setTimeout(resolve, API_CONSTANTS.REQUEST_DELAY));
        }
      } catch (error) {
        console.error(`Failed to fetch ships for agent ${agent.symbol}:`, error);
      }
    }

    return allShips;
  }

  start(
    onAgentsUpdate: (agents: Agent[]) => void,
    onShipsUpdate: (ships: TaggedShip[]) => void,
    onError?: (error: Error) => void
  ) {
    if (this.isRunning) {
      console.warn('Polling service already running');
      return;
    }

    this.isRunning = true;

    const poll = async () => {
      try {
        const agents = await getAgents();
        onAgentsUpdate(agents);
        const ships = await this.fetchAllShips(agents);
        onShipsUpdate(ships);
      } catch (error) {
        console.error('Polling error:', error);
        onError?.(error as Error);
      }
    };

    // Initial fetch
    poll();

    // Set up interval
    this.intervalId = window.setInterval(poll, API_CONSTANTS.POLL_INTERVAL);
  }

  stop() {
    if (this.intervalId !== null) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }
    this.isRunning = false;
  }

  isActive(): boolean {
    return this.isRunning;
  }
}

export const pollingService = new ShipPollingService();
