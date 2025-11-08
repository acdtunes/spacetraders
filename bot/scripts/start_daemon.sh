#!/bin/bash
# Start the SpaceTraders daemon server
# Checks if already running before starting

set -e

# Change to script's parent directory (project root)
cd "$(dirname "$0")/.."

echo "🚀 Starting daemon..."

# Check if already running
if [ -f var/daemon.sock ]; then
    count=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l)
    if [ "$count" -gt 0 ]; then
        echo "⚠️  Daemon already running ($count process(es))"
        echo ""
        echo "Processes:"
        lsof var/daemon.sock
        echo ""
        echo "Use './scripts/restart_daemon.sh' to force restart"
        exit 1
    else
        echo "  Cleaning up stale socket..."
        rm -f var/daemon.sock
    fi
fi

# Clean up stale PID file
rm -f var/daemon.pid

# Start daemon
echo "  Starting daemon..."
PYTHONPATH=src:$PYTHONPATH uv run python -m adapters.primary.daemon.daemon_server > /tmp/daemon.log 2>&1 &

# Wait for startup (poll for socket)
for i in {1..10}; do
    if [ -f var/daemon.sock ]; then
        sleep 0.2  # Small delay to ensure ready
        break
    fi
    sleep 0.1
done

# Verify started
if [ -f var/daemon.sock ]; then
    count=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l)
    if [ "$count" -eq 1 ]; then
        echo "✅ Daemon started successfully"
        echo ""
        echo "Recent logs:"
        tail -5 /tmp/daemon.log
        exit 0
    else
        echo "❌ ERROR: Expected 1 process, found $count"
        lsof var/daemon.sock
        exit 1
    fi
else
    echo "❌ ERROR: Socket file not created"
    echo ""
    echo "Daemon logs:"
    tail -20 /tmp/daemon.log
    exit 1
fi
