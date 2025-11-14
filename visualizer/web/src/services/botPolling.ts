import type { AppState } from '../store/useStore';
import {
  getAssignments,
  getMarketData,
  getMarketFreshness,
  getScoutTours,
  getTradeOpportunities,
  getPlayerMappings,
} from './api';

/**
 * Bot operations polling service
 * Fetches bot operation data and updates the store
 */
export class BotPollingService {
  private intervalId: number | null = null;
  private isRunning = false;
  private pollInterval: number;

  // Store current polling parameters to avoid stale closures
  private currentSystem: string | null = null;
  private selectedPlayerId: number | null = null;
  private setAssignments: AppState['setAssignments'] | null = null;
  private setMarketFreshness: AppState['setMarketFreshness'] | null = null;
  private setMarketIntel: AppState['setMarketIntel'] | null = null;
  private setScoutTours: AppState['setScoutTours'] | null = null;
  private setTradeOpportunities: AppState['setTradeOpportunities'] | null = null;
  private setAvailablePlayers: AppState['setAvailablePlayers'] | null = null;
  private setPlayerMappings: AppState['setPlayerMappings'] | null = null;

  constructor(pollInterval: number = 10000) {
    // 10 second default interval
    this.pollInterval = pollInterval;
  }

  /**
   * Start polling for bot operations data
   */
  start(
    currentSystem: string | null,
    selectedPlayerId: number | null,
    setAssignments: AppState['setAssignments'],
    setMarketFreshness: AppState['setMarketFreshness'],
    setMarketIntel: AppState['setMarketIntel'],
    setScoutTours: AppState['setScoutTours'],
    setTradeOpportunities: AppState['setTradeOpportunities'],
    setAvailablePlayers: AppState['setAvailablePlayers'],
    setPlayerMappings: AppState['setPlayerMappings']
  ): void {
    if (this.isRunning) {
      console.warn('[BotPollingService] Bot polling is already running, stopping old instance');
      this.stop();
    }

    // Store parameters as instance variables to avoid stale closures
    const previousSystem = this.currentSystem;
    this.currentSystem = currentSystem;
    this.selectedPlayerId = selectedPlayerId;
    this.setAssignments = setAssignments;
    this.setMarketFreshness = setMarketFreshness;
    this.setMarketIntel = setMarketIntel;
    this.setScoutTours = setScoutTours;
    this.setTradeOpportunities = setTradeOpportunities;
    this.setAvailablePlayers = setAvailablePlayers;
    this.setPlayerMappings = setPlayerMappings;

    if (!currentSystem || previousSystem !== currentSystem) {
      this.setMarketIntel([]);
    }

    this.isRunning = true;
    console.log('[BotPollingService] Starting bot operations polling with selectedPlayerId:', selectedPlayerId);

    // Initial fetch
    this.pollCycle();

    // Set up interval - now uses instance variables instead of closure
    this.intervalId = window.setInterval(() => {
      this.pollCycle();
    }, this.pollInterval);
  }

  /**
   * Stop polling
   */
  stop(): void {
    if (!this.isRunning) {
      return;
    }

    this.isRunning = false;

    if (this.intervalId !== null) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }

    console.log('Stopped bot operations polling');
  }

  /**
   * Single poll cycle - uses instance variables to avoid stale closures
   */
  private async pollCycle(): Promise<void> {
    console.log('[BotPollingService.pollCycle] Starting poll cycle', {
      currentSystem: this.currentSystem,
      selectedPlayerId: this.selectedPlayerId,
    });

    // Defensive checks
    if (!this.setAssignments || !this.setMarketFreshness || !this.setScoutTours ||
        !this.setTradeOpportunities || !this.setAvailablePlayers || !this.setPlayerMappings ||
        !this.setMarketIntel) {
      console.error('[BotPollingService.pollCycle] Store setters not initialized');
      return;
    }

    try {
      // Fetch player mappings and assignments in parallel
      const [playerMappings, assignments] = await Promise.all([
        getPlayerMappings().catch((err) => {
          console.warn('Failed to fetch player mappings:', err);
          return new Map<string, number>();
        }),
        getAssignments().catch((err) => {
          console.warn('Failed to fetch assignments:', err);
          return [];
        }),
      ]);

      this.setPlayerMappings(playerMappings);
      this.setAssignments(assignments);

      // Extract unique player IDs from assignments
      const playerIds = Array.from(new Set(assignments.map((a) => a.player_id))).sort((a, b) => a - b);
    this.setAvailablePlayers(playerIds);

    // If we have a current system, fetch system-specific data
    if (this.currentSystem) {
      console.log('[BotPollingService.pollCycle] Fetching scout tours for system:', this.currentSystem, 'with player_id:', this.selectedPlayerId);

      // Fetch in parallel
      const [freshness, tours, opportunities, marketIntel] = await Promise.all([
        getMarketFreshness(this.currentSystem).catch((err) => {
          console.warn('Failed to fetch market freshness:', err);
          return [];
        }),
        getScoutTours(this.currentSystem, this.selectedPlayerId ?? undefined).catch((err) => {
          console.warn('Failed to fetch scout tours:', err);
          return [];
        }),
        getTradeOpportunities(this.currentSystem, 200).catch((err) => {
          console.warn('Failed to fetch trade opportunities:', err);
          return [];
        }),
        getMarketData(this.currentSystem).catch((err) => {
          console.warn('Failed to fetch market data:', err);
          return [];
        }),
      ]);

      console.log('[BotPollingService.pollCycle] Scout tours fetched:', tours.length, 'tours');

      this.setMarketFreshness(freshness);
      this.setScoutTours(tours);
      this.setTradeOpportunities(opportunities);
      this.setMarketIntel(marketIntel);
    } else {
      this.setMarketIntel([]);
    }
  } catch (error) {
      console.error('Error during bot polling:', error);
    }
  }

  /**
   * Check if service is running
   */
  isPolling(): boolean {
    return this.isRunning;
  }

  /**
   * Update poll interval (will take effect on next start)
   */
  setPollInterval(interval: number): void {
    this.pollInterval = interval;
  }
}
