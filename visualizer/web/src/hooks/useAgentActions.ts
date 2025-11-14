import { useCallback, useState } from 'react';
import { useStore } from '../store/useStore';
import {
  addAgent as requestAddAgent,
  deleteAgent as requestDeleteAgent,
} from '../services/api';
import type { Agent } from '../types/spacetraders';

interface AgentFormState {
  token: string;
  setToken: (value: string) => void;
  error: string | null;
  isSubmitting: boolean;
  submit: () => Promise<void>;
  clearError: () => void;
}

interface AgentActions {
  registerAgent: (token: string) => Promise<Agent>;
  removeAgent: (agentId: string) => Promise<void>;
}

export function useAgentActions(): AgentActions {
  const { addAgent, removeAgent } = useStore();

  const registerAgent = useCallback(
    async (token: string) => {
      const sanitizedToken = token.trim();

      if (!sanitizedToken) {
        throw new Error('Token is required');
      }

      const agent = await requestAddAgent(sanitizedToken);
      addAgent(agent);
      return agent;
    },
    [addAgent]
  );

  const removeAgentAndRefresh = useCallback(
    async (agentId: string) => {
      await requestDeleteAgent(agentId);
      removeAgent(agentId);
    },
    [removeAgent]
  );

  return {
    registerAgent,
    removeAgent: removeAgentAndRefresh,
  };
}

export function useAgentForm(): AgentFormState {
  const { registerAgent } = useAgentActions();
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setSubmitting] = useState(false);

  const clearError = useCallback(() => {
    setError(null);
  }, []);

  const handleTokenChange = useCallback(
    (value: string) => {
      setToken(value);
      if (error) {
        clearError();
      }
    },
    [error, clearError]
  );

  const submit = useCallback(async () => {
    if (isSubmitting) {
      return;
    }

    const sanitizedToken = token.trim();
    if (!sanitizedToken) {
      setError('Agent token is required');
      return;
    }

    setSubmitting(true);

    try {
      await registerAgent(sanitizedToken);
      setToken('');
      clearError();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to add agent.';
      setError(message);
    } finally {
      setSubmitting(false);
    }
  }, [clearError, isSubmitting, registerAgent, token]);

  return {
    token,
    setToken: handleTokenChange,
    error,
    isSubmitting,
    submit,
    clearError,
  };
}
