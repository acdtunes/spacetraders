import type { AppState } from '../store/useStore';
import {
  getAssignments,
  getMarketFreshness,
  getScoutTours,
  getTradeOpportunities,
} from './api';

/**
 * Bot operations polling service
 * Fetches bot operation data and updates the store
 */
export class BotPollingService {
  private intervalId: number | null = null;
  private isRunning = false;
  private pollInterval: number;

  constructor(pollInterval: number = 10000) {
    // 10 second default interval
    this.pollInterval = pollInterval;
  }

  /**
   * Start polling for bot operations data
   */
  start(
    currentSystem: string | null,
    setAssignments: AppState['setAssignments'],
    setMarketFreshness: AppState['setMarketFreshness'],
    setScoutTours: AppState['setScoutTours'],
    setTradeOpportunities: AppState['setTradeOpportunities']
  ): void {
    if (this.isRunning) {
      console.warn('Bot polling is already running');
      return;
    }

    this.isRunning = true;
    console.log('Starting bot operations polling...');

    // Initial fetch
    this.poll(currentSystem, setAssignments, setMarketFreshness, setScoutTours, setTradeOpportunities);

    // Set up interval
    this.intervalId = window.setInterval(() => {
      this.poll(currentSystem, setAssignments, setMarketFreshness, setScoutTours, setTradeOpportunities);
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
   * Single poll cycle
   */
  private async poll(
    currentSystem: string | null,
    setAssignments: AppState['setAssignments'],
    setMarketFreshness: AppState['setMarketFreshness'],
    setScoutTours: AppState['setScoutTours'],
    setTradeOpportunities: AppState['setTradeOpportunities']
  ): Promise<void> {
    try {
      // Fetch assignments (not system-specific)
      const assignments = await getAssignments();
      setAssignments(assignments);

      // If we have a current system, fetch system-specific data
      if (currentSystem) {
        // Fetch in parallel
        const [freshness, tours, opportunities] = await Promise.all([
          getMarketFreshness(currentSystem).catch((err) => {
            console.warn('Failed to fetch market freshness:', err);
            return [];
          }),
          getScoutTours(currentSystem).catch((err) => {
            console.warn('Failed to fetch scout tours:', err);
            return [];
          }),
          getTradeOpportunities(currentSystem, 200).catch((err) => {
            console.warn('Failed to fetch trade opportunities:', err);
            return [];
          }),
        ]);

        setMarketFreshness(freshness);
        setScoutTours(tours);
        setTradeOpportunities(opportunities);
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
