#!/usr/bin/env bash
# Captain-invokable daemon restart. launchd-aware (kickstart -k restarts the
# managed service); falls back to make restart-daemon outside launchd.
# Ends by polling health so the caller gets a definitive verdict.
set -u
GOBOT="$(cd "$(dirname "$0")/../../gobot" && pwd)"
SVC="com.spacetraders.daemon"
if launchctl list "$SVC" >/dev/null 2>&1; then
  launchctl kickstart -k "gui/$(id -u)/$SVC"
  echo "daemon kickstarted via launchd"
else
  (cd "$GOBOT" && make restart-daemon)
fi
exec "$(dirname "$0")/wait-daemon.sh" 60
