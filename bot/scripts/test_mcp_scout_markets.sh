#!/bin/bash
# Test MCP scout_markets direct socket integration
# This validates the Node.js DaemonClient bypass of Python CLI

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
BOT_DIR="$( cd "${SCRIPT_DIR}/.." && pwd )"

echo "=== Testing MCP scout_markets Direct Socket Integration ==="
echo ""

# Check daemon is running
if ! pgrep -f daemon_server > /dev/null; then
    echo "❌ Daemon server is not running. Start it first:"
    echo "   uv run python -m spacetraders.adapters.primary.daemon.daemon_server > /tmp/daemon.log 2>&1 &"
    exit 1
fi

echo "✅ Daemon server is running"

# Check socket exists
if [ ! -S "${BOT_DIR}/var/daemon.sock" ]; then
    echo "❌ Daemon socket not found at ${BOT_DIR}/var/daemon.sock"
    exit 1
fi

echo "✅ Daemon socket exists"

# Test: Send scout_markets request via Node.js DaemonClient
# This simulates what the MCP server does

cd "${BOT_DIR}"

cat > /tmp/test_scout_markets_mcp.mjs << EOF
import { DaemonClient } from '${BOT_DIR}/mcp/build/daemonClient.js';

const client = new DaemonClient();

console.log('📡 Sending scout_markets request via DaemonClient...');
const startTime = Date.now();

try {
    const result = await client.scoutMarkets(
        ['ENDURANCE-1', 'ENDURANCE-2'],  // ships
        1,                                 // player_id
        'X1-HZ85',                        // system
        ['X1-HZ85-A1', 'X1-HZ85-B2'],    // markets
        1                                  // iterations
    );

    const elapsed = Date.now() - startTime;

    console.log(\`✅ Request completed in \${elapsed}ms\`);
    console.log('Response:', JSON.stringify(result, null, 2));

    if (elapsed < 500) {
        console.log('🚀 SUCCESS: Response time < 500ms (target met!)');
        process.exit(0);
    } else {
        console.log(\`⚠️  WARNING: Response time \${elapsed}ms > 500ms target\`);
        process.exit(1);
    }
} catch (error) {
    const elapsed = Date.now() - startTime;
    console.error(\`❌ FAILED after \${elapsed}ms:\`, error.message);
    process.exit(1);
}
EOF

echo ""
echo "Running MCP integration test..."
echo ""

node /tmp/test_scout_markets_mcp.mjs

echo ""
echo "=== Test Complete ==="
