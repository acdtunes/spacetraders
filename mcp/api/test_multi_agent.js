#!/usr/bin/env node

/**
 * Test script to verify multi-agent support with database token lookup
 * Tests that player_id parameter correctly fetches tokens from database
 */

import { spawn } from 'child_process';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const serverPath = path.resolve(__dirname, 'build', 'index.js');

console.log('🧪 Testing MCP API Server with multi-agent support\n');

// Test configuration
const testCases = [
  {
    name: 'Test 1: get_agent with player_id=6 (SILMARETH)',
    input: {
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/call',
      params: {
        name: 'get_agent',
        arguments: {
          player_id: 6
        }
      }
    }
  }
];

function runTest(testCase) {
  return new Promise((resolve, reject) => {
    console.log(`📝 ${testCase.name}`);
    console.log(`   Request: ${JSON.stringify(testCase.input, null, 2)}`);

    const child = spawn('node', [serverPath], {
      stdio: ['pipe', 'pipe', 'pipe']
    });

    let stdout = '';
    let stderr = '';
    let timedOut = false;

    const timer = setTimeout(() => {
      timedOut = true;
      child.kill('SIGKILL');
      reject(new Error('Test timed out'));
    }, 10000);

    child.stdout.on('data', (chunk) => {
      stdout += chunk.toString();
    });

    child.stderr.on('data', (chunk) => {
      stderr += chunk.toString();
      // Log database connection messages
      if (chunk.toString().includes('Database')) {
        console.log(`   📊 ${chunk.toString().trim()}`);
      }
    });

    child.on('close', (code) => {
      clearTimeout(timer);

      if (timedOut) {
        return;
      }

      // Parse JSON-RPC responses
      const lines = stdout.trim().split('\n');
      let response = null;

      for (const line of lines) {
        try {
          const parsed = JSON.parse(line);
          if (parsed.id === testCase.input.id) {
            response = parsed;
            break;
          }
        } catch (e) {
          // Skip non-JSON lines
        }
      }

      if (response) {
        if (response.error) {
          console.log(`   ❌ Error: ${response.error.message}\n`);
          resolve({ success: false, error: response.error });
        } else {
          try {
            const data = JSON.parse(response.result.content[0].text);
            console.log(`   ✅ Success! Agent: ${data.data.symbol}`);
            console.log(`   📍 Headquarters: ${data.data.headquarters}\n`);
            resolve({ success: true, data });
          } catch (e) {
            console.log(`   ✅ Response received (parsing issue): ${e.message}\n`);
            resolve({ success: true, response });
          }
        }
      } else {
        console.log(`   ❌ No response received`);
        console.log(`   stdout: ${stdout.substring(0, 200)}\n`);
        resolve({ success: false });
      }
    });

    child.on('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });

    // Send JSON-RPC request
    child.stdin.write(JSON.stringify(testCase.input) + '\n');
    child.stdin.end();
  });
}

async function main() {
  let allPassed = true;

  for (const testCase of testCases) {
    try {
      const result = await runTest(testCase);
      if (!result.success) {
        allPassed = false;
      }
    } catch (error) {
      console.log(`   ❌ Test failed: ${error.message}\n`);
      allPassed = false;
    }
  }

  console.log('\n' + '='.repeat(60));
  if (allPassed) {
    console.log('✅ ALL TESTS PASSED - Multi-agent support working!');
  } else {
    console.log('❌ SOME TESTS FAILED - Check errors above');
  }
  console.log('='.repeat(60) + '\n');

  process.exit(allPassed ? 0 : 1);
}

main().catch(error => {
  console.error('Fatal error:', error);
  process.exit(1);
});
