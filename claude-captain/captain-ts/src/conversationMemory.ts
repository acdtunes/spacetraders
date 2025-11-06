/**
 * Session memory management for TARS using SDK's native session persistence
 * Stores both session_id (for SDK context) and messages (for UI display)
 */

import { readFileSync, writeFileSync, existsSync, unlinkSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import type { SDKMessage } from '@anthropic-ai/claude-agent-sdk';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const SESSION_FILE = join(__dirname, '..', '.tars_session.json');

export class ConversationMemory {
  private sessionId: string | null = null;
  private conversationTurns: number = 0;
  private createdAt: Date;
  private messages: SDKMessage[] = [];

  constructor() {
    this.createdAt = new Date();
    this.loadFromFile();
  }

  /**
   * Load previous session from file
   */
  private loadFromFile(): void {
    try {
      if (existsSync(SESSION_FILE)) {
        const data = JSON.parse(readFileSync(SESSION_FILE, 'utf-8'));
        this.sessionId = data.session_id;
        this.conversationTurns = data.conversation_turns || 0;
        this.createdAt = new Date(data.created_at);
        this.messages = data.messages || [];
      }
    } catch (error) {
      // Silently ignore - will start fresh session
    }
  }

  /**
   * Save session to file
   */
  private saveToFile(): void {
    if (!this.sessionId) return;

    try {
      const data = {
        session_id: this.sessionId,
        created_at: this.createdAt.toISOString(),
        last_active: new Date().toISOString(),
        conversation_turns: this.conversationTurns,
        messages: this.messages
      };
      writeFileSync(SESSION_FILE, JSON.stringify(data, null, 2), 'utf-8');
    } catch (error) {
      // Silently ignore write errors
    }
  }

  /**
   * Set session ID (from SDK messages)
   */
  setSessionId(sessionId: string): void {
    this.sessionId = sessionId;
    this.saveToFile();
  }

  /**
   * Increment conversation turn counter
   */
  incrementTurns(): void {
    this.conversationTurns++;
    this.saveToFile();
  }

  /**
   * Get session ID for resuming
   */
  getSessionId(): string | null {
    return this.sessionId;
  }

  /**
   * Add a message to history
   */
  addMessage(message: SDKMessage): void {
    this.messages.push(message);
    this.saveToFile();
  }

  /**
   * Get all stored messages
   */
  getMessages(): SDKMessage[] {
    return this.messages;
  }

  /**
   * Clear session memory
   */
  clear(): void {
    this.sessionId = null;
    this.conversationTurns = 0;
    this.messages = [];
    if (existsSync(SESSION_FILE)) {
      unlinkSync(SESSION_FILE);
    }
  }

  /**
   * Check if there's a previous session to resume
   */
  hasPreviousSession(): boolean {
    return this.sessionId !== null;
  }

  /**
   * Get conversation turn count
   */
  get turnCount(): number {
    return this.conversationTurns;
  }
}
