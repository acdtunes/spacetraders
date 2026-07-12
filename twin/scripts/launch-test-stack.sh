#!/usr/bin/env bash
#
# launch-test-stack.sh — boot the digital twin + an ISOLATED test daemon.
# Refuses to launch unless test-config.yaml pins every isolation value; NEVER --force.
# Usage: twin/scripts/launch-test-stack.sh [--dry-run]
# Env overrides (empty ⇒ default): TWIN_TEST_CONFIG, TWIN_BASE_URL, TEST_DATABASE_URL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TWIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$TWIN_DIR/.." && pwd)"
GOBOT_DIR="$REPO_ROOT/gobot"
CLI_BIN="$GOBOT_DIR/bin/spacetraders"
DAEMON_BIN="$GOBOT_DIR/bin/spacetraders-daemon"

TWIN_TEST_CONFIG="${TWIN_TEST_CONFIG:-$TWIN_DIR/test-config.yaml}"
TWIN_BASE_URL="${TWIN_BASE_URL:-http://127.0.0.1:8080/v2}"
TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgresql://spacetraders:dev_password@localhost:5434/spacetraders_test?sslmode=disable}"
# Isolation signal is the DB NAME (spacetraders_test), not the port. Derive the port from the DSN
# so the reachability check tracks it (default :5434; :5433 was taken by another project).
TEST_DB_PORT="$(printf '%s' "$TEST_DATABASE_URL" | sed -E 's#.*@[^:/]+:([0-9]+)/.*#\1#')"; [ -n "$TEST_DB_PORT" ] || TEST_DB_PORT=5434

TEST_PID_FILE="/tmp/spacetraders-daemon-test.pid"
TEST_GRPC_HOST="localhost"; TEST_GRPC_PORT="50062"
TWIN_LOG="/tmp/spacetraders-twin-test.log"; TWIN_PID_FILE="/tmp/spacetraders-twin-test.pid"
DAEMON_LOG="/tmp/spacetraders-daemon-test.log"

DRY_RUN=0; if [ "${1:-}" = "--dry-run" ]; then DRY_RUN=1; fi
fail() { echo "REFUSING TO LAUNCH: $*" >&2; exit 1; }

[ -f "$TWIN_TEST_CONFIG" ] || fail "config not found: $TWIN_TEST_CONFIG"
require_line() {
  grep -Eq "$1" "$TWIN_TEST_CONFIG" || fail "$2 — $TWIN_TEST_CONFIG must contain '$3'.
Without this override a test daemon lands in PRODUCTION's slot, and the next --force boot SIGTERM-kills the production daemon."
}
require_line '^[[:space:]]*pid_file:[[:space:]]*/tmp/spacetraders-daemon-test\.pid[[:space:]]*(#.*)?$' "daemon.pid_file is not the -test pidfile" "pid_file: /tmp/spacetraders-daemon-test.pid"
require_line '^[[:space:]]*socket_path:[[:space:]]*/tmp/spacetraders-daemon-test\.sock[[:space:]]*(#.*)?$' "daemon.socket_path is not the -test socket" "socket_path: /tmp/spacetraders-daemon-test.sock"
require_line '^[[:space:]]*address:[[:space:]]*localhost:50062[[:space:]]*(#.*)?$' "daemon.address is not the test gRPC port" "address: localhost:50062"
require_line '^[[:space:]]*port:[[:space:]]*9092[[:space:]]*(#.*)?$' "metrics.port is not 9092 (prod serves 9090)" "port: 9092"
grep -Fq '/spacetraders_test' "$TWIN_TEST_CONFIG" || fail "database.url does not point at the spacetraders_test DB — prod (spacetraders on 5432) is READ-ONLY."

echo "twin config:   $TWIN_TEST_CONFIG"
echo "daemon bin:    $DAEMON_BIN"
echo "env:           SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG"
echo "env:           ST_API_BASE_URL=$TWIN_BASE_URL"
echo "env:           DATABASE_URL=$TEST_DATABASE_URL"
if [ "$DRY_RUN" = "1" ]; then echo "dry-run: guards passed; nothing launched."; exit 0; fi

if [ ! -x "$CLI_BIN" ] || [ ! -x "$DAEMON_BIN" ]; then echo "building CLI + daemon..."; make -C "$GOBOT_DIR" build-cli build-daemon; fi
if ! (echo > "/dev/tcp/localhost/$TEST_DB_PORT") 2>/dev/null; then
  fail "test Postgres not reachable on localhost:$TEST_DB_PORT. Start it first (docker compose -f twin/docker-compose.test.yml up -d postgres-test). Prod 5432 is READ-ONLY."
fi

if curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1; then echo "twin already serving $TWIN_BASE_URL — reusing it."; else
  [ -d "$TWIN_DIR/node_modules" ] || npm --prefix "$TWIN_DIR" install
  echo "booting twin (npm --prefix $TWIN_DIR run start) -> $TWIN_LOG"
  npm --prefix "$TWIN_DIR" run start >"$TWIN_LOG" 2>&1 &
  echo $! > "$TWIN_PID_FILE"
  i=0; while [ $i -lt 60 ]; do curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1 && break; sleep 0.5; i=$((i + 1)); done
  curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1 || fail "twin did not become ready on $TWIN_BASE_URL (log: $TWIN_LOG)"
fi

if [ -f "$TEST_PID_FILE" ] && kill -0 "$(cat "$TEST_PID_FILE")" 2>/dev/null; then
  echo "stale test daemon pid $(cat "$TEST_PID_FILE") — SIGTERM via the TEST pidfile only..."
  kill "$(cat "$TEST_PID_FILE")" 2>/dev/null || true
  i=0; while [ $i -lt 20 ] && [ -f "$TEST_PID_FILE" ]; do sleep 0.5; i=$((i + 1)); done
fi
rm -f "$TEST_PID_FILE"

echo "booting isolated test daemon -> $DAEMON_LOG"
( cd "$GOBOT_DIR"; exec env "SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG" "ST_API_BASE_URL=$TWIN_BASE_URL" "DATABASE_URL=$TEST_DATABASE_URL" "$DAEMON_BIN" >"$DAEMON_LOG" 2>&1 ) &
i=0; while [ $i -lt 60 ]; do
  if [ -f "$TEST_PID_FILE" ] && (echo > "/dev/tcp/$TEST_GRPC_HOST/$TEST_GRPC_PORT") 2>/dev/null; then break; fi
  sleep 0.5; i=$((i + 1))
done
[ -f "$TEST_PID_FILE" ] || fail "test daemon never wrote $TEST_PID_FILE (log: $DAEMON_LOG)"
(echo > "/dev/tcp/$TEST_GRPC_HOST/$TEST_GRPC_PORT") 2>/dev/null || fail "test daemon gRPC not accepting on $TEST_GRPC_HOST:$TEST_GRPC_PORT (log: $DAEMON_LOG)"

echo ""; echo "test stack is up:"
echo "  twin:         $TWIN_BASE_URL   (log: $TWIN_LOG)"
echo "  daemon pid:   $(cat "$TEST_PID_FILE")   (log: $DAEMON_LOG)"
echo "  daemon gRPC:  $TEST_GRPC_HOST:$TEST_GRPC_PORT"
echo "  next:         $SCRIPT_DIR/seed-player.sh"
