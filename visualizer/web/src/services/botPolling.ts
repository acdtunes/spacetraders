import type { AppState } from '../store/useStore';
import { useStore } from '../store/useStore';
import { GATE_WAYPOINT } from '../constants/api';
import {
  getAssignments,
  getMarketData,
  getMarketFreshness,
  getScoutTours,
  getTradeOpportunities,
  getPlayerMappings,
  getFleetEvents,
  getGateProgress,
} from './api';

// Exponential-backoff ceiling: a persistently unreachable backend polls no
// slower than once per minute so recovery is detected promptly.
const MAX_BACKOFF_MS = 60_000;

/**
 * Bot operations polling service
 * Fetches bot operation data and updates the store.
 *
 * The loop is a self-rescheduling setTimeout (not a fixed setInterval): each
 * cycle schedules the next only after it finishes, so a slow or failed cycle
 * can widen the interval via exponential backoff instead of stacking overlapping
 * requests. The fleet-events probe is the connection heartbeat — its success or
 * failure drives store.connection and the backoff.
 */
export class BotPollingService {
  private timeoutId: number | null = null;
  private isRunning = false;
  private baseInterval: number;
  private currentDelay: number;

  // Bumped on every start()/stop(). An in-flight async cycle captures the
  // generation it began under and refuses to apply results or reschedule once it
  // no longer matches — this is what keeps useBotPolling's stop()+start() on
  // every system/player change from leaking or double-firing timers.
  private generation = 0;

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
    this.baseInterval = pollInterval;
    this.currentDelay = pollInterval;
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
    // A fresh start resets the backoff so a prior loss does not carry over.
    this.currentDelay = this.baseInterval;
    const generation = ++this.generation;

    console.log('[BotPollingService] Starting bot operations polling with selectedPlayerId:', selectedPlayerId);

    // Kick off immediately; runCycle self-reschedules via setTimeout.
    void this.runCycle(generation);
  }

  /**
   * Stop polling
   */
  stop(): void {
    if (!this.isRunning) {
      return;
    }

    this.isRunning = false;
    // Invalidate any in-flight cycle so its awaited work will not reschedule a
    // timer after we clear the pending one below.
    this.generation++;

    if (this.timeoutId !== null) {
      clearTimeout(this.timeoutId);
      this.timeoutId = null;
    }

    console.log('Stopped bot operations polling');
  }

  /**
   * Run one cycle and schedule the next. Success resets the interval and marks
   * the connection healthy; ANY failure widens the backoff and marks it lost.
   */
  private async runCycle(generation: number): Promise<void> {
    // A stop()/restart between scheduling and firing invalidates this run.
    if (!this.isRunning || generation !== this.generation) {
      return;
    }

    let ok = false;
    try {
      await this.pollCycle();
      ok = true;
    } catch (error) {
      console.error('[BotPollingService] Poll cycle failed:', error);
    }

    // The awaited cycle may have straddled a stop()/restart; bail if now stale so
    // we neither touch the store for a dead run nor schedule an orphaned timer.
    if (!this.isRunning || generation !== this.generation) {
      return;
    }

    if (ok) {
      this.currentDelay = this.baseInterval; // success resets backoff
      useStore.getState().setConnection({ status: 'ok', lastContactAt: Date.now() });
    } else {
      // Failure: exponential backoff, doubling up to the 60s ceiling.
      this.currentDelay = Math.min(this.currentDelay * 2, MAX_BACKOFF_MS);
      useStore.getState().setConnection({ status: 'lost' });
    }

    this.timeoutId = window.setTimeout(() => {
      void this.runCycle(generation);
    }, this.currentDelay);
  }

  /**
   * Single poll cycle. The events + gate heartbeat runs first: getFleetEvents
   * throwing (server 503 / network drop) propagates and fails the whole cycle,
   * which is what flips connection to 'lost'. Everything after it is best-effort
   * overlay data whose individual failures never trip the connection.
   */
  private async pollCycle(): Promise<void> {
    const store = useStore.getState();

    // Cursor = highest id already ingested. fleetEvents is newest-first, so the
    // head holds the max id; undefined on a cold start fetches the latest page.
    const afterId = store.fleetEvents[0]?.id;

    const [events, gate] = await Promise.all([
      getFleetEvents(afterId, 50),
      // Gate is era-coupled and supplementary — swallow its failure so a missing
      // or mismatched construction site can never trap the client in 'lost'.
      getGateProgress(GATE_WAYPOINT).catch((err) => {
        console.warn('Failed to fetch gate progress:', err);
        return null;
      }),
    ]);

    store.ingestEvents(events);
    if (gate) {
      store.setGate(gate);
    }

    // Best-effort overlay data (assignments, tours, market intel, ...).
    await this.pollOverlays();
  }

  /**
   * Fetch the legacy overlay datasets. Fully self-contained: every fetch has its
   * own fallback and the whole thing is wrapped so it never throws, keeping
   * overlay failures from marking the heartbeat's connection lost.
   */
  private async pollOverlays(): Promise<void> {
    // Defensive checks — setters are always provided in production via start().
    if (!this.setAssignments || !this.setMarketFreshness || !this.setScoutTours ||
        !this.setTradeOpportunities || !this.setAvailablePlayers || !this.setPlayerMappings ||
        !this.setMarketIntel) {
      console.error('[BotPollingService.pollOverlays] Store setters not initialized');
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

      // Extract unique player IDs from assignments (filter out null values)
      const playerIds = Array.from(
        new Set(
          assignments
            .map((a) => a.player_id)
            .filter((id): id is number => id !== null)
        )
      ).sort((a, b) => a - b);
      this.setAvailablePlayers(playerIds);

      // If we have a current system, fetch system-specific data
      if (this.currentSystem) {
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

        this.setMarketFreshness(freshness);
        this.setScoutTours(tours);
        this.setTradeOpportunities(opportunities);
        this.setMarketIntel(marketIntel);
      } else {
        this.setMarketIntel([]);
      }
    } catch (error) {
      console.error('Error during bot overlay polling:', error);
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
    this.baseInterval = interval;
  }
}
