/**
 * TARS App - Main React component for the Captain UI
 */

import React, { useState, useEffect, useRef } from 'react';
import { query } from '@anthropic-ai/claude-agent-sdk';
import type { SDKMessage, Options } from '@anthropic-ai/claude-agent-sdk';
import { ConversationMemory } from '../conversationMemory.js';
import { App } from './App.js';

export interface AfkConfig {
  durationHours: number;
  checkinMinutes: number;
  mission?: string;
}

export interface TarsAppProps {
  options: Partial<Options>;
  memory: ConversationMemory;
  afkMode?: AfkConfig;
}

export const TarsApp: React.FC<TarsAppProps> = ({ options, memory, afkMode }) => {
  // Load stored messages from memory on mount
  const [messages, setMessages] = useState<SDKMessage[]>(memory.getMessages());
  const [isProcessing, setIsProcessing] = useState(false);
  const [shouldExit, setShouldExit] = useState(false);
  const shouldCancelRef = useRef<boolean>(false);
  const [isAfkMode, setIsAfkMode] = useState(!!afkMode);
  const [afkConfig, setAfkConfig] = useState<AfkConfig | undefined>(afkMode);
  const afkIntervalRef = useRef<NodeJS.Timeout | undefined>();
  const [userCommands, setUserCommands] = useState<string[]>([]);

  const memoryStatus = memory.hasPreviousSession()
    ? `Session #${memory.getSessionId()?.substring(0, 8)}... (${memory.turnCount} turns)`
    : 'Fresh start';

  const handleCommand = async (command: string) => {
    // Handle special commands
    if (command.toLowerCase() === '/clear-memory') {
      memory.clear();
      setMessages([]);
      return;
    }

    // Add user command to history display
    setUserCommands(prev => [...prev, command]);

    if (command.toLowerCase().startsWith('/afk')) {
      // Parse /afk command: /afk [duration] [checkin]
      const parts = command.split(/\s+/);
      const durationHours = parts.length >= 2 ? parseFloat(parts[1]) : 4;
      const checkinMinutes = parts.length >= 3 ? parseInt(parts[2]) : 30;

      if (isNaN(durationHours) || durationHours <= 0) {
        const msg: SDKMessage = {
          type: 'system',
          content: '⚠️ Invalid duration. Using default 4 hours.'
        };
        setMessages(prev => [...prev, msg]);
        memory.addMessage(msg);
      }

      if (isNaN(checkinMinutes) || checkinMinutes <= 0) {
        const msg: SDKMessage = {
          type: 'system',
          content: '⚠️ Invalid check-in interval. Using default 30 minutes.'
        };
        setMessages(prev => [...prev, msg]);
        memory.addMessage(msg);
      }

      const config: AfkConfig = {
        durationHours: isNaN(durationHours) || durationHours <= 0 ? 4 : durationHours,
        checkinMinutes: isNaN(checkinMinutes) || checkinMinutes <= 0 ? 30 : checkinMinutes,
        mission: undefined
      };

      const afkMsg: SDKMessage = {
        type: 'system',
        content: `🤖 Entering AFK mode: ${config.durationHours}h, check-in every ${config.checkinMinutes}min\n💡 Press ESC to return to interactive mode`
      };
      setMessages(prev => [...prev, afkMsg]);
      memory.addMessage(afkMsg);

      setAfkConfig(config);
      setIsAfkMode(true);
      return;
    }

    setIsProcessing(true);
    shouldCancelRef.current = false;

    try {
      // Build query options with session resume if available
      const queryOptions = { ...options };
      const sessionId = memory.getSessionId();
      if (sessionId) {
        queryOptions.resume = sessionId;
      }

      const result = query({
        prompt: command,
        options: queryOptions
      });

      for await (const message of result) {
        // Check if user requested cancellation
        if (shouldCancelRef.current) {
          const interruptMsg: SDKMessage = {
            type: 'system',
            content: '⚠️ Processing interrupted by user (ESC pressed)'
          };
          setMessages(prev => [...prev, interruptMsg]);
          memory.addMessage(interruptMsg);
          break;
        }

        setMessages(prev => [...prev, message]);
        memory.addMessage(message);

        // Capture and save session ID from any message
        if ('session_id' in message && message.session_id) {
          memory.setSessionId(message.session_id);
        }
      }

      // Increment turn counter only if not cancelled
      if (!shouldCancelRef.current) {
        memory.incrementTurns();
      }
    } catch (error) {
      console.error('Error:', error);
    } finally {
      setIsProcessing(false);
      shouldCancelRef.current = false;
    }
  };

  const handleExit = () => {
    setShouldExit(true);
  };

  useEffect(() => {
    if (shouldExit) {
      process.exit(0);
    }
  }, [shouldExit]);

  // AFK Mode management
  useEffect(() => {
    if (isAfkMode && afkConfig) {
      const mission = afkConfig.mission ||
        `🤖 AFK MODE ACTIVATED - FULLY AUTONOMOUS OPERATION

You are now in AFK (Away From Keyboard) mode for ${afkConfig.durationHours} hours.

CRITICAL: You are operating FULLY AUTONOMOUSLY. The Admiral is AFK (away).
DO NOT ask for approval. DO NOT ask questions. DO NOT request input.
Make all decisions yourself and REPORT what you did.

════════════════════════════════════════════════════════════════════════
INITIAL MISSION SETUP - CHECK-IN #0
════════════════════════════════════════════════════════════════════════

Your tasks for this initial setup:

1. **CREATE STRATEGIC PLAN**
   - Analyze current fleet state (ship_list, player_info, daemon_list)
   - Create a ${afkConfig.durationHours}-hour strategic plan following Early Game Playbook
   - Define phases with specific goals and timings
   - Write plan to mission log

2. **START INITIAL OPERATIONS**
   - Assess what ships are idle
   - Delegate to specialists to start operations (contract-coordinator, scout-coordinator, etc.)
   - Get operations running immediately
   - Record container IDs for monitoring

3. **REPORT SETUP COMPLETE**
   - Summarize what operations were started
   - Show your strategic plan phases
   - State when next check-in will occur

After completing setup, you will enter autonomous operation mode.
Check-ins will occur every ${afkConfig.checkinMinutes} minutes where you will:
- Review fleet/operations status
- Execute next phase of your plan
- Delegate to specialists as needed
- Report what you did

BEGIN SETUP NOW.`;

      handleCommand(mission);

      // Set up check-in interval
      const checkinInterval = afkConfig.checkinMinutes * 60 * 1000;
      const startTime = Date.now();
      const endTime = startTime + (afkConfig.durationHours * 3600 * 1000);
      let checkinCount = 1;

      const intervalId = setInterval(() => {
        const now = Date.now();
        if (now >= endTime) {
          clearInterval(intervalId);
          setMessages(prev => [...prev, {
            type: 'system',
            content: '✅ AFK mode complete. Returning to interactive mode.'
          }]);
          setIsAfkMode(false);
          setAfkConfig(undefined);
          return;
        }

        const elapsedHours = (now - startTime) / (3600 * 1000);
        const remainingHours = (endTime - now) / (3600 * 1000);

        const checkinQuery = `🔔 AFK MODE - CHECK-IN #${checkinCount}

⏱️  Time elapsed: ${elapsedHours.toFixed(1)} hours
⏱️  Time remaining: ${remainingHours.toFixed(1)} hours

════════════════════════════════════════════════════════════════════════
AUTONOMOUS WORK CYCLE - TAKE ACTION NOW
════════════════════════════════════════════════════════════════════════

REMINDER: You are in FULLY AUTONOMOUS AFK mode. Do NOT ask questions.
Make decisions. Delegate to agents. Execute. Report.

Your tasks for this check-in:

1. **ASSESS CURRENT STATE**
   - Query fleet status: ship_list()
   - Query operations status: daemon_list(), daemon_inspect()
   - Query credits: player_info()
   - Check for any failures or idle ships

2. **EXECUTE NEXT PHASE OF YOUR STRATEGIC PLAN**
   - Based on your plan and current state, what phase are you in?
   - What actions need to happen now?
   - Delegate to specialists to execute:
     * contract-coordinator for contract operations
     * scout-coordinator for market intelligence
     * fleet-manager for composition analysis
     * procurement-coordinator for ship purchases (if planned)
   - Start/stop daemons as needed
   - Assign idle ships to work

3. **RESOLVE ANY ISSUES**
   - If operations failed: delegate to bug-reporter, then decide alternative approach
   - If ships idle: assign them work
   - If low credits: prioritize revenue operations
   - If utilization low: start more operations

4. **REPORT WHAT YOU DID**
   - Starting state: Credits, fleet status, operations
   - Actions taken: Which specialists you delegated to, what operations started/stopped
   - Ending state: New credits, new operations running, new container IDs
   - Current phase: Where are you in your strategic plan?
   - Next check-in: What will you do in ${afkConfig.checkinMinutes} minutes?

COMPLETE ALL 4 STEPS NOW. Take action, don't just report.
After reporting, you will wait for the next check-in at ${elapsedHours.toFixed(1) + (afkConfig.checkinMinutes / 60)} hours.`;

        handleCommand(checkinQuery);
        checkinCount++;
      }, checkinInterval);

      afkIntervalRef.current = intervalId;

      return () => {
        if (afkIntervalRef.current) {
          clearInterval(afkIntervalRef.current);
        }
      };
    }
  }, [isAfkMode, afkConfig]);

  // Handle ESC in AFK mode to return to interactive
  useEffect(() => {
    const handleEscInAfk = () => {
      if (isAfkMode && shouldCancelRef.current) {
        if (afkIntervalRef.current) {
          clearInterval(afkIntervalRef.current);
        }

        setMessages(prev => [...prev, {
          type: 'system',
          content: '⚠️ AFK mode interrupted. Returning to interactive mode.'
        }]);

        setIsAfkMode(false);
        setAfkConfig(undefined);
        shouldCancelRef.current = false;
      }
    };

    if (shouldCancelRef.current && isAfkMode) {
      handleEscInAfk();
    }
  }, [isAfkMode, shouldCancelRef.current]);

  return (
    <App
      onCommand={handleCommand}
      messages={messages}
      isProcessing={isProcessing}
      memoryStatus={memoryStatus}
      onExit={handleExit}
      cancelRef={shouldCancelRef}
      isAfkMode={isAfkMode}
      userCommands={userCommands}
    />
  );
};
