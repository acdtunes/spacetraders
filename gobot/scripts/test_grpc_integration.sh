#!/bin/bash
#
# Integration test for gRPC daemon and CLI
# Tests: Start daemon -> Health check -> Stop daemon
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SOCKET_PATH="/tmp/spacetraders-test-daemon.sock"
DAEMON_BIN="$PROJECT_ROOT/bin/spacetraders-daemon"
CLI_BIN="$PROJECT_ROOT/bin/spacetraders"
DAEMON_PID=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "======================================"
echo "gRPC Integration Test"
echo "======================================"
echo ""

# Cleanup function
cleanup() {
    if [ -n "$DAEMON_PID" ] && kill -0 $DAEMON_PID 2>/dev/null; then
        echo -e "${YELLOW}Stopping daemon (PID: $DAEMON_PID)...${NC}"
        kill $DAEMON_PID 2>/dev/null || true
        wait $DAEMON_PID 2>/dev/null || true
    fi

    # Remove socket file
    rm -f "$SOCKET_PATH"

    echo -e "${GREEN}Cleanup complete${NC}"
}

# Set trap for cleanup
trap cleanup EXIT INT TERM

# Step 1: Check binaries exist
echo "Step 1: Checking binaries..."
if [ ! -f "$DAEMON_BIN" ]; then
    echo -e "${RED}Error: Daemon binary not found at $DAEMON_BIN${NC}"
    exit 1
fi

if [ ! -f "$CLI_BIN" ]; then
    echo -e "${RED}Error: CLI binary not found at $CLI_BIN${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Binaries found${NC}"
echo ""

# Step 2: Start daemon in background
echo "Step 2: Starting daemon..."
export SPACETRADERS_SOCKET="$SOCKET_PATH"
export DB_TYPE="sqlite"

# Start daemon in background and redirect output
$DAEMON_BIN > /tmp/daemon-test.log 2>&1 &
DAEMON_PID=$!

echo "Daemon started (PID: $DAEMON_PID)"
echo "Waiting for daemon to initialize..."

# Wait for socket to be created (max 10 seconds)
for i in {1..20}; do
    if [ -S "$SOCKET_PATH" ]; then
        echo -e "${GREEN}✓ Daemon socket created${NC}"
        break
    fi

    # Check if daemon is still running
    if ! kill -0 $DAEMON_PID 2>/dev/null; then
        echo -e "${RED}Error: Daemon process died${NC}"
        echo "Daemon log:"
        cat /tmp/daemon-test.log
        exit 1
    fi

    sleep 0.5
done

if [ ! -S "$SOCKET_PATH" ]; then
    echo -e "${RED}Error: Daemon socket not created within timeout${NC}"
    echo "Daemon log:"
    cat /tmp/daemon-test.log
    exit 1
fi

# Give it a moment to fully initialize
sleep 2
echo ""

# Step 3: Test health check (via gRPC)
echo "Step 3: Testing health check..."

# Note: We would need a CLI command that uses the DaemonClient
# For now, we'll just check if the daemon is responsive by checking the socket
if [ -S "$SOCKET_PATH" ]; then
    echo -e "${GREEN}✓ Daemon socket is accessible${NC}"

    # Check daemon process is still running
    if kill -0 $DAEMON_PID 2>/dev/null; then
        echo -e "${GREEN}✓ Daemon process is running${NC}"
    else
        echo -e "${RED}✗ Daemon process is not running${NC}"
        exit 1
    fi
else
    echo -e "${RED}✗ Daemon socket is not accessible${NC}"
    exit 1
fi
echo ""

# Step 4: Check daemon logs
echo "Step 4: Daemon output (first 20 lines)..."
echo "----------------------------------------"
head -n 20 /tmp/daemon-test.log
echo "----------------------------------------"
echo ""

# Success!
echo -e "${GREEN}======================================"
echo "All integration tests passed!"
echo "======================================${NC}"
echo ""
echo "Note: Full CLI integration with health check command"
echo "      will be available when CLI commands are wired up."

exit 0
