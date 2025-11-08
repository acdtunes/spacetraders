#!/bin/bash
# Stop the SpaceTraders daemon server gracefully

set -e

# Change to script's parent directory (project root)
cd "$(dirname "$0")/.."

echo "🛑 Stopping daemon..."

# Check if running
if [ ! -f var/daemon.sock ]; then
    echo "⚠️  Daemon not running (socket file not found)"
    exit 0
fi

count=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l)
if [ "$count" -eq 0 ]; then
    echo "⚠️  Daemon not running (no processes holding socket)"
    rm -f var/daemon.sock var/daemon.pid
    exit 0
fi

# Get PIDs
pids=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | awk '{print $2}')

echo "  Sending SIGTERM to daemon processes..."
for pid in $pids; do
    kill -TERM $pid 2>/dev/null || true
done

# Wait for graceful shutdown
sleep 2

# Check if still running
remaining=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l)
if [ "$remaining" -gt 0 ]; then
    echo "  Processes didn't stop gracefully, forcing kill..."
    for pid in $pids; do
        kill -9 $pid 2>/dev/null || true
    done
    sleep 1
fi

# Clean up
rm -f var/daemon.sock var/daemon.pid

# Verify stopped
if [ -f var/daemon.sock ]; then
    echo "❌ ERROR: Failed to stop daemon"
    lsof var/daemon.sock
    exit 1
else
    echo "✅ Daemon stopped successfully"
    exit 0
fi
