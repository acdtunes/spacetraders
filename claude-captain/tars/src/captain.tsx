#!/usr/bin/env node
/**
 * SpaceTraders Autonomous Captain - Main Entry Point
 */

import React from 'react';
import { render } from 'ink';
import { Command } from 'commander';
import { PassThrough } from 'stream';
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
    .option('--debug', 'Enable debug rendering mode (shows all updates, disables throttling)')
    .parse(process.argv);

  const opts = program.opts();

  // Initialize conversation memory
  const memory = new ConversationMemory();

  // Configure Agent SDK
  const agentOptions = createCaptainOptions();

  // Prepare render options
  // Note: When stdin doesn't support raw mode (e.g., when running as a subprocess),
  // Ink will throw errors. We provide a PassThrough stream as a dummy stdin.
  let stdinStream = process.stdin;
  if (!process.stdin.isTTY) {
    // Create a dummy stdin stream that doesn't support raw mode
    const dummyStdin = new PassThrough();
    // @ts-ignore - Add isTTY property to make Ink happy
    dummyStdin.isTTY = false;
    // @ts-ignore - Add setRawMode that does nothing
    dummyStdin.setRawMode = () => dummyStdin;
    stdinStream = dummyStdin as any;
  }

  const renderOptions = {
    debug: opts.debug || false,
    stdin: stdinStream,
    stdout: process.stdout,
    stderr: process.stderr
  };

  // Render the Ink app
  if (opts.afk) {
    render(<TarsApp options={agentOptions} memory={memory} afkMode={{
      durationHours: opts.duration,
      checkinMinutes: opts.checkin,
      mission: opts.mission
    }} />, renderOptions);
  } else {
    render(<TarsApp options={agentOptions} memory={memory} />, renderOptions);
  }
}

// Run main function
main().catch((error) => {
  console.error('Fatal error:', error);
  process.exit(1);
});
