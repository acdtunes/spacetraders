/**
 * Filesystem-based logging for subagent invocations
 * Organizes logs by sessionId: agent-logs/<sessionId>/agent-<timestamp>.log
 */

import { mkdirSync, appendFileSync, existsSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import type { SDKMessage } from '@anthropic-ai/claude-agent-sdk';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const LOGS_BASE_DIR = join(__dirname, '..', 'agent-logs');

export interface AgentInvocation {
  agentName: string;
  timestamp: Date;
  prompt: string;
  sessionId: string;
}

export interface AgentResult {
  agentName: string;
  timestamp: Date;
  result: string;
  duration: number;
  sessionId: string;
}

export class AgentLogger {
  private pendingInvocations: Map<string, AgentInvocation> = new Map();

  /**
   * Ensure log directory exists for a session
   */
  private ensureSessionDir(sessionId: string): string {
    const sessionDir = join(LOGS_BASE_DIR, sessionId);
    if (!existsSync(sessionDir)) {
      mkdirSync(sessionDir, { recursive: true });
    }
    return sessionDir;
  }

  /**
   * Log a subagent invocation (when Task tool is called)
   */
  logInvocation(sessionId: string, agentName: string, prompt: string): void {
    const timestamp = new Date();
    const invocation: AgentInvocation = {
      agentName,
      timestamp,
      prompt,
      sessionId
    };

    // Store pending invocation
    const key = `${sessionId}:${agentName}`;
    this.pendingInvocations.set(key, invocation);

    // Write to filesystem immediately
    const sessionDir = this.ensureSessionDir(sessionId);
    const filename = `${agentName}_${timestamp.getTime()}.log`;
    const filepath = join(sessionDir, filename);

    const header = `
================================================================================
SUBAGENT INVOCATION
================================================================================
Agent: ${agentName}
Session: ${sessionId}
Timestamp: ${timestamp.toISOString()}

INPUT PROMPT:
--------------------------------------------------------------------------------
${prompt}

WAITING FOR RESULT...
================================================================================

`;

    appendFileSync(filepath, header, 'utf-8');
  }

  /**
   * Log a subagent result (when Task tool returns)
   */
  logResult(sessionId: string, agentName: string, result: string): void {
    const timestamp = new Date();
    const key = `${sessionId}:${agentName}`;
    const invocation = this.pendingInvocations.get(key);

    if (!invocation) {
      console.warn(`No pending invocation found for ${key}`);
      return;
    }

    const duration = timestamp.getTime() - invocation.timestamp.getTime();
    this.pendingInvocations.delete(key);

    // Append result to the same log file
    const sessionDir = this.ensureSessionDir(sessionId);
    const filename = `${agentName}_${invocation.timestamp.getTime()}.log`;
    const filepath = join(sessionDir, filename);

    const resultSection = `
RESULT RECEIVED:
--------------------------------------------------------------------------------
Timestamp: ${timestamp.toISOString()}
Duration: ${(duration / 1000).toFixed(2)}s

OUTPUT:
--------------------------------------------------------------------------------
${result}

================================================================================
END OF INVOCATION
================================================================================
`;

    appendFileSync(filepath, resultSection, 'utf-8');
  }

  /**
   * Extract Task tool calls from SDK messages
   */
  extractTaskCalls(message: SDKMessage): Array<{ agentName: string; prompt: string }> {
    const taskCalls: Array<{ agentName: string; prompt: string }> = [];

    // Check if message contains tool_calls
    if ('tool_calls' in message && Array.isArray(message.tool_calls)) {
      for (const toolCall of message.tool_calls) {
        if (toolCall.name === 'Task' && toolCall.parameters) {
          const params = toolCall.parameters as any;
          if (params.subagent_type && params.prompt) {
            taskCalls.push({
              agentName: params.subagent_type,
              prompt: params.prompt
            });
          }
        }
      }
    }

    return taskCalls;
  }

  /**
   * Extract Task tool results from SDK messages
   */
  extractTaskResults(message: SDKMessage): Array<{ agentName: string; result: string }> {
    const taskResults: Array<{ agentName: string; result: string }> = [];

    // Check if message contains tool_results
    if ('tool_results' in message && Array.isArray(message.tool_results)) {
      for (const toolResult of message.tool_results) {
        if (toolResult.name === 'Task' && toolResult.result) {
          // Try to extract agent name from result or description
          const resultText = typeof toolResult.result === 'string'
            ? toolResult.result
            : JSON.stringify(toolResult.result);

          // Agent name might be in the tool_call_id or we need to match with pending invocations
          // For now, we'll try to parse it from the result
          const agentMatch = resultText.match(/agent[:\s]+([a-z-]+)/i);
          const agentName = agentMatch ? agentMatch[1] : 'unknown-agent';

          taskResults.push({
            agentName,
            result: resultText
          });
        }
      }
    }

    return taskResults;
  }

  /**
   * Process an SDK message for logging
   */
  processMessage(message: SDKMessage, sessionId: string): void {
    // Log Task invocations
    const taskCalls = this.extractTaskCalls(message);
    for (const { agentName, prompt } of taskCalls) {
      this.logInvocation(sessionId, agentName, prompt);
    }

    // Log Task results
    const taskResults = this.extractTaskResults(message);
    for (const { agentName, result } of taskResults) {
      this.logResult(sessionId, agentName, result);
    }
  }
}

// Singleton instance
export const agentLogger = new AgentLogger();
