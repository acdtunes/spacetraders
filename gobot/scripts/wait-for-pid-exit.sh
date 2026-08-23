#!/usr/bin/env bash
# Actively wait for a PID to exit, bounded by a timeout (sp-tb5px).
#
# THE INCIDENT this fixes: restart-daemon's non-launchd fallback (hit whenever
# com.spacetraders.daemon isn't loaded in launchd) used to `kill $PID; sleep 2`
# before starting a replacement daemon with nohup. The daemon's OWN graceful
# shutdown (GracefulShutdownTimeout, internal/adapters/grpc/daemon_server_lifecycle.go)
# waits up to 30s for active containers to finish their current operation before
# it actually exits and releases its PID-file lock. A fixed 2s sleep is nowhere
# near enough once the fleet has more than a handful of active containers
# (reproduced live with both 13 and 29 active containers): the replacement's
# nohup start collided with the still-alive old process, failed immediately with
# "Failed to acquire PID file lock: daemon is already running (PID N)" (no
# retry, no --force), and that failure was swallowed into daemon.log while the
# old daemon kept running another 20-30+s on its own schedule. Net effect: the
# restart silently left NOTHING running, discovered only because a human was
# watching and manually polled for the old process to die before re-running
# the restart.
#
# This script replaces the fixed sleep with an ACTIVE poll of the old PID
# (`kill -0`), mirroring the pattern already used inside the daemon binary
# itself for its own --force path (internal/infrastructure/pidfile.go's
# KillExisting: signal, then poll, never a blind sleep). It lives in its own
# file rather than inline in the Makefile recipe -- like reload-unit.sh -- so
# the wait/timeout/fail-loud logic is reviewable and independently testable
# against a real (fake) process, not just eyeballed inside Make's `$$`
# escaping.
#
# Behavior:
#   - Already dead (or never existed): returns 0 immediately. No sleep at all
#     -- a fast-exiting old daemon must never be penalized with a fixed delay.
#   - Still alive: polls once per POLL_INTERVAL (default 1s) until it exits.
#   - Still alive when TIMEOUT elapses: fails LOUDLY -- non-zero exit, a
#     specific stderr message naming the still-live PID and why we refuse to
#     proceed. The caller (gobot/Makefile's restart-daemon target) MUST treat
#     a non-zero exit here as fatal and skip starting a replacement daemon:
#     starting one against a PID-file lock the old process still holds would
#     just repeat the "Failed to acquire PID file lock" failure, silently.
#
# Usage: wait-for-pid-exit.sh <pid> [timeout_secs]
# Env (test-only override): WAIT_POLL_INTERVAL -- seconds between polls (default 1)
set -euo pipefail

PID="${1:?usage: wait-for-pid-exit.sh <pid> [timeout_secs]}"
TIMEOUT="${2:-40}"
POLL_INTERVAL="${WAIT_POLL_INTERVAL:-1}"

is_alive() { kill -0 "$PID" 2>/dev/null; }

if ! is_alive; then
  echo "wait-for-pid-exit: PID $PID already gone"
  exit 0
fi

echo "wait-for-pid-exit: waiting up to ${TIMEOUT}s for PID $PID to exit (it may still be draining active work on its own schedule)..."
waited=0
while is_alive; do
  if [ "$waited" -ge "$TIMEOUT" ]; then
    echo "WAIT-FOR-PID-EXIT FAILED: PID $PID is still running after ${TIMEOUT}s." >&2
    echo "  Refusing to start a replacement process while the old one still holds its PID-file lock" >&2
    echo "  -- it would just fail to acquire the lock and exit, silently leaving nothing running." >&2
    echo "  The old process may still be gracefully draining; confirm with 'ps -p $PID' and re-run" >&2
    echo "  once it is gone (or investigate why it did not exit)." >&2
    exit 1
  fi
  sleep "$POLL_INTERVAL"
  waited=$(( waited + POLL_INTERVAL ))
done

echo "wait-for-pid-exit: PID $PID exited after ~${waited}s"
