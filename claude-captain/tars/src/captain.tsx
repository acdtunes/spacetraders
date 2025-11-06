#!/usr/bin/env node
/**
 * SpaceTraders Autonomous Captain - Main Entry Point
 */

import React from 'react';
import { render } from 'ink';
import { Command } from 'commander';
import { ConversationMemory } from './conversationMemory.js';
import { createCaptainOptions } from './agentConfig.js';
import { TarsApp } from './ui/TarsApp.js';

/**
 * Main entry point
 */
async function main() {
  const program = new Command();

  program
    .name('tars-captain')
    .description('TARS - Tactical Autonomous Resource Strategist')
    .option('--afk', 'Run in autonomous AFK mode')
    .option('--duration <hours>', 'Duration in hours for AFK mode', parseFloat, 4)
    .option('--checkin <minutes>', 'Check-in interval in minutes', parseInt, 30)
    .option('--mission <text>', 'Mission directive for AFK mode')
    .parse(process.argv);

  const opts = program.opts();

  // Initialize conversation memory
  const memory = new ConversationMemory();

  // Configure Agent SDK
  const agentOptions = createCaptainOptions();

  // Render the Ink app
  if (opts.afk) {
    render(<TarsApp options={agentOptions} memory={memory} afkMode={{
      durationHours: opts.duration,
      checkinMinutes: opts.checkin,
      mission: opts.mission
    }} />);
  } else {
    render(<TarsApp options={agentOptions} memory={memory} />);
  }
}

// Run main function
main().catch((error) => {
  console.error('Fatal error:', error);
  process.exit(1);
});
