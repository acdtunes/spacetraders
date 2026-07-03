#!/usr/bin/env bash
# Poll daemon health for up to $1 seconds (default 45).
# Prints SOCKET-OK or SOCKET-DEAD — distinguishes "wedged but recovering"
# from "dead" without needing shell loops in the session allowlist.
set -u
GOBOT="$(cd "$(dirname "$0")/../../gobot" && pwd)"
deadline=$(( $(date +%s) + ${1:-45} ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  if "$GOBOT/bin/spacetraders" health >/dev/null 2>&1; then
    echo "SOCKET-OK"; exit 0
  fi
  sleep 3
done
echo "SOCKET-DEAD after ${1:-45}s"; exit 1
