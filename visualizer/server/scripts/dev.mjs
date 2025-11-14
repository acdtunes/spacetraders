#!/usr/bin/env node

import { spawn, exec } from 'child_process';
import { promisify } from 'util';

const execAsync = promisify(exec);

let serverProcess = null;
let isShuttingDown = false;
let isStarting = false;
let hasInitialCompile = false;

async function killPortProcesses() {
  try {
    await execAsync('lsof -ti:4000 | xargs kill -9 2>/dev/null || true');
  } catch (err) {
    // Ignore errors - port might already be free
  }
}

async function killServer() {
  if (serverProcess) {
    console.log('Stopping server...');
    try {
      serverProcess.kill('SIGTERM');
      // Wait a bit for graceful shutdown
      await new Promise(resolve => setTimeout(resolve, 500));
      // Force kill if still running
      if (serverProcess && !serverProcess.killed) {
        serverProcess.kill('SIGKILL');
      }
    } catch (err) {
      // Process might already be dead
    }
    serverProcess = null;
  }
  // Ensure port is free
  await killPortProcesses();
}

async function startServer() {
  if (isShuttingDown || isStarting) return;

  isStarting = true;
  await killServer();

  console.log('Starting server...');
  serverProcess = spawn('node', ['build/server/index.js'], {
    stdio: 'inherit',
    shell: false,
    detached: false
  });

  serverProcess.on('exit', (code) => {
    if (!isShuttingDown && code !== null && code !== 0 && code !== 143 && code !== 130) {
      console.error(`Server exited with code ${code}`);
    }
    serverProcess = null;
    isStarting = false;
  });

  // Server is started, clear flag after a short delay
  setTimeout(() => {
    isStarting = false;
  }, 1000);
}

// Start TypeScript compiler in watch mode
console.log('Starting TypeScript compiler in watch mode...');
const tscProcess = spawn('tsc', ['--watch'], {
  stdio: 'pipe',
  shell: false
});

let outputBuffer = '';

tscProcess.stdout.on('data', (data) => {
  const text = data.toString();
  process.stdout.write(text);
  outputBuffer += text;

  // Check for compilation complete
  if (outputBuffer.includes('Found 0 errors')) {
    outputBuffer = '';

    if (!hasInitialCompile) {
      // First compilation - start server
      hasInitialCompile = true;
      console.log('✓ Initial TypeScript compilation complete, starting server...');
      setTimeout(() => startServer(), 500);
    } else {
      // Subsequent compilations - restart server
      console.log('✓ TypeScript compilation complete, restarting server...');
      setTimeout(() => startServer(), 500);
    }
  }

  // Clear buffer periodically
  if (outputBuffer.length > 10000) {
    outputBuffer = outputBuffer.slice(-5000);
  }
});

tscProcess.stderr.on('data', (data) => {
  process.stderr.write(data);
});

// Handle graceful shutdown
async function shutdown() {
  if (isShuttingDown) return;
  isShuttingDown = true;

  console.log('\nShutting down...');

  await killServer();

  if (tscProcess) {
    tscProcess.kill('SIGTERM');
    setTimeout(() => {
      if (tscProcess && !tscProcess.killed) {
        tscProcess.kill('SIGKILL');
      }
    }, 1000);
  }

  process.exit(0);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
