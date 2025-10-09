import { useEffect, useRef } from 'react';
import { useStore } from '../store/useStore';
import { BotPollingService } from '../services/botPolling';

export function useBotPolling() {
  const {
    currentSystem,
    setAssignments,
    setMarketFreshness,
    setScoutTours,
    setTradeOpportunities,
  } = useStore();

  const serviceRef = useRef<BotPollingService | null>(null);

  useEffect(() => {
    // Create service if it doesn't exist
    if (!serviceRef.current) {
      serviceRef.current = new BotPollingService(10000); // 10 second poll interval
    }

    const service = serviceRef.current;

    // Start polling
    service.start(
      currentSystem,
      setAssignments,
      setMarketFreshness,
      setScoutTours,
      setTradeOpportunities
    );

    // Cleanup on unmount or when current system changes
    return () => {
      service.stop();
    };
  }, [currentSystem, setAssignments, setMarketFreshness, setScoutTours, setTradeOpportunities]);

  return serviceRef.current;
}
