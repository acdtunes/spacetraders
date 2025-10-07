import { useEffect } from 'react';
import { useStore } from '../store/useStore';
import { pollingService } from '../services/polling';

export function usePolling() {
  const { agents, setShips, setPolling, setLastUpdate } = useStore();

  useEffect(() => {
    if (agents.length === 0) {
      pollingService.stop();
      setPolling(false);
      return;
    }

    setPolling(true);

    pollingService.start(
      agents,
      (ships) => {
        setShips(ships);
        setLastUpdate(Date.now());
      },
      (error) => {
        console.error('Polling service error:', error);
        // Continue polling even on error
      }
    );

    return () => {
      pollingService.stop();
      setPolling(false);
    };
  }, [agents, setShips, setPolling, setLastUpdate]);
}
