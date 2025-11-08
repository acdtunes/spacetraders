#!/bin/bash
# Check status of SpaceTraders daemon server

set -e

# Change to script's parent directory (project root)
cd "$(dirname "$0")/.."

echo "📊 Daemon Status"
echo "================"
echo ""

# Check socket file
if [ ! -S var/daemon.sock ]; then
    echo "Status: ❌ NOT RUNNING (socket file not found)"
    exit 1
fi

# Check processes
count=$(lsof var/daemon.sock 2>/dev/null | grep -v COMMAND | wc -l)

if [ "$count" -eq 0 ]; then
    echo "Status: ❌ NOT RUNNING (stale socket file)"
    echo ""
    echo "Clean up with: rm var/daemon.sock"
    exit 1
elif [ "$count" -eq 1 ]; then
    echo "Status: ✅ RUNNING"
    echo ""
    echo "Process:"
    lsof var/daemon.sock | head -2
    echo ""
    echo "Recent activity:"
    tail -10 /tmp/daemon.log | grep -E "(INFO|WARNING|ERROR)" || echo "  (no recent logs)"
    echo ""
    echo "Containers:"
    PYTHONPATH=src:$PYTHONPATH uv run python << 'EOF' 2>/dev/null || echo "  (unable to query containers)"
import sys
sys.path.insert(0, 'src')
from adapters.primary.cli.daemon_cli import daemon_list_command
from argparse import Namespace
args = Namespace(player_id=None)
daemon_list_command(args)
EOF
    exit 0
else
    echo "Status: ⚠️  MULTIPLE INSTANCES ($count processes)"
    echo ""
    echo "Processes:"
    lsof var/daemon.sock
    echo ""
    echo "Fix with: ./scripts/restart_daemon.sh"
    exit 2
fi
