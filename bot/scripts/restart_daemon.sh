#!/bin/bash
# Restart the SpaceTraders daemon server
# Handles cleanup of zombie processes and stale files

set -e

# Change to script's parent directory (project root)
cd "$(dirname "$0")/.."

echo "🔄 Restarting daemon..."

# Kill all daemon-related processes
echo "  Killing existing daemon processes..."
pkill -9 -f "daemon_server" 2>/dev/null || true
pkill -9 -f "uv run.*daemon" 2>/dev/null || true

# Clean up stale files
echo "  Cleaning up stale files..."
rm -f var/daemon.sock var/daemon.pid

# Wait for cleanup
sleep 1

# Load environment variables from .env file
if [ -f .env ]; then
    echo "  Loading DATABASE_URL from .env..."
    export $(grep -v '^#' .env | xargs)
else
    echo "⚠️  WARNING: .env file not found, using default PostgreSQL connection"
    export DATABASE_URL="postgresql://spacetraders:dev_password@localhost:5432/spacetraders"
fi

# Start daemon
echo "  Starting daemon..."
PYTHONPATH=src:$PYTHONPATH uv run python -m adapters.primary.daemon.daemon_server > /tmp/daemon.log 2>&1 &

# Wait for socket to appear with retry logic (up to 10 seconds)
echo "  Waiting for socket..."
max_attempts=20
attempt=0
while [ $attempt -lt $max_attempts ]; do
    if [ -S var/daemon.sock ]; then
        break
    fi
    sleep 0.5
    attempt=$((attempt + 1))
done

# Verify daemon started successfully
if [ ! -S var/daemon.sock ]; then
    echo "❌ ERROR: Socket file not created after ${max_attempts} attempts"
    echo ""
    echo "Recent logs:"
    tail -10 /tmp/daemon.log
    exit 1
fi

# Verify single instance using lsof
count=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l | tr -d ' ')
if [ "$count" -eq 1 ]; then
    echo "✅ Daemon started successfully"
    echo ""
    echo "Recent logs:"
    tail -5 /tmp/daemon.log
    exit 0
else
    echo "❌ ERROR: Expected 1 process, found $count"
    echo ""
    echo "Processes holding socket:"
    lsof var/daemon.sock 2>/dev/null || echo "(none)"
    echo ""
    echo "Recent logs:"
    tail -10 /tmp/daemon.log
    exit 1
fi
