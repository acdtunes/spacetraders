import { useEffect, useRef } from 'react';
import { useStore } from '../store/useStore';
import { BotPollingService } from '../services/botPolling';

export function useBotPolling() {
  const {
    currentSystem,
    selectedPlayerId,
    setAssignments,
    setMarketFreshness,
    setMarketIntel,
    setScoutTours,
    setTradeOpportunities,
    setAvailablePlayers,
    setPlayerMappings,
  } = useStore();

  const serviceRef = useRef<BotPollingService | null>(null);

  useEffect(() => {
    console.log('[useBotPolling] Effect triggered', {
      currentSystem,
      selectedPlayerId,
      serviceExists: !!serviceRef.current,
    });

    // Create service if it doesn't exist
    if (!serviceRef.current) {
      console.log('[useBotPolling] Creating new BotPollingService');
      serviceRef.current = new BotPollingService(10000); // 10 second poll interval
    }

    const service = serviceRef.current;

    console.log('[useBotPolling] Starting polling service with selectedPlayerId:', selectedPlayerId);

    // CRITICAL: Stop any existing polling first to avoid race conditions
    // This ensures the old interval is cleared before starting a new one with updated parameters
    service.stop();

    // Start polling with updated parameters
    service.start(
      currentSystem,
      selectedPlayerId,
      setAssignments,
      setMarketFreshness,
      setMarketIntel,
      setScoutTours,
      setTradeOpportunities,
      setAvailablePlayers,
      setPlayerMappings
    );

    // Cleanup on unmount or when dependencies change
    return () => {
      console.log('[useBotPolling] Stopping polling service (cleanup)');
      service.stop();
    };
  }, [currentSystem, selectedPlayerId, setAssignments, setMarketFreshness, setMarketIntel, setScoutTours, setTradeOpportunities, setAvailablePlayers, setPlayerMappings]);

  return serviceRef.current;
}
