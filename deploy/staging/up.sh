#!/usr/bin/env bash
# =============================================================================
# deploy/staging/up.sh (sp-widl) — bring up the STAGING stack, one command.
#
#   deploy/staging/up.sh
#
# Steps (each fails closed):
#   1. source env.sh   -> prod-safety gate (disjoint-from-prod assertions)
#   2. make build ENV=staging   -> staging-suffixed binaries; prod bin untouched
#   3. ensure the spacetraders_staging database exists
#   4. start the staging routing service on :50053 (bg, own pid file)
#   5. start the staging daemon on the staging socket/pid (bg, --force)
#
# Isolated from prod at every layer. Tear down with deploy/staging/down.sh.
# =============================================================================
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh" || {
  echo "up.sh: staging env failed its prod-safety checks; refusing to start." >&2; exit 1; }

staging_env_banner
mkdir -p "$STAGING_RUN_DIR"

# --- 2. Build staging binaries (NEVER touches the prod binary) ----------------
echo "[up] building staging binaries (make build ENV=staging)..."
make -C "$GOBOT_DIR" build-cli build-daemon ENV=staging
[[ -x "$STAGING_DAEMON_BIN" ]] || { echo "up.sh: staging daemon binary not built: $STAGING_DAEMON_BIN" >&2; exit 1; }

# --- 3. Ensure the staging database exists ------------------------------------
ensure_staging_db() {
  if command -v psql >/dev/null 2>&1; then
    if [[ "$(psql "$STAGING_DB_ADMIN_URL" -tAc "SELECT 1 FROM pg_database WHERE datname='$STAGING_DB'" 2>/dev/null || true)" == "1" ]]; then
      echo "[up] staging db '$STAGING_DB' already exists."
    else
      echo "[up] creating staging db '$STAGING_DB'..."
      psql "$STAGING_DB_ADMIN_URL" -c "CREATE DATABASE \"$STAGING_DB\"" >/dev/null
    fi
    return 0
  fi
  # Fallback: a local postgres docker container publishing 5432.
  local c
  c="$(docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null | awk '/5432/{print $1; exit}')" || true
  if [[ -n "${c:-}" ]]; then
    echo "[up] psql not found; using docker container '$c' to ensure db '$STAGING_DB'..."
    docker exec "$c" psql -U spacetraders -d postgres -tAc \
      "SELECT 1 FROM pg_database WHERE datname='$STAGING_DB'" 2>/dev/null | grep -q 1 \
      || docker exec "$c" psql -U spacetraders -d postgres -c "CREATE DATABASE \"$STAGING_DB\"" >/dev/null
    return 0
  fi
  echo "up.sh: cannot ensure the staging db — install psql or run a postgres container, then:" >&2
  echo "  createdb -h localhost -p 5432 -U spacetraders $STAGING_DB" >&2
  return 1
}
ensure_staging_db

# --- 4. Start the staging routing service on :50053 ---------------------------
# Restart cleanly if a previous staging routing is still up (helper is defined in
# env.sh and refuses to signal anything that is not ours).
staging_stop_if_ours "$STAGING_ROUTING_PID" "routing-service/run.sh" "routing"
echo "[up] starting staging routing service on :$STAGING_ROUTING_PORT (log: $STAGING_ROUTING_LOG)..."
ROUTING_PORT="$STAGING_ROUTING_PORT" nohup bash "$GOBOT_DIR/services/routing-service/run.sh" \
  >>"$STAGING_ROUTING_LOG" 2>&1 &
echo $! > "$STAGING_ROUTING_PID"

# --- 5. Start the staging daemon on the staging socket/pid --------------------
# --force reaps only the STAGING pid (config-derived cfg.Daemon.PIDFile), never
# the live daemon. SPACETRADERS_CONFIG pins the whole isolated config.
echo "[up] starting staging daemon (socket $STAGING_SOCKET, pid $STAGING_PID; log: $STAGING_DAEMON_LOG)..."
SPACETRADERS_CONFIG="$STAGING_CONFIG" nohup "$STAGING_DAEMON_BIN" --force \
  >>"$STAGING_DAEMON_LOG" 2>&1 &

sleep 2
echo ""
echo "[up] staging stack is coming up. Verify + drive it with:"
echo "  deploy/staging/stc.sh player info        # CLI against the staging daemon"
echo "  tail -f $STAGING_DAEMON_LOG"
echo "  deploy/staging/register.sh <CALLSIGN> '<AGENT_JWT>'   # register a staging agent"
echo "  deploy/staging/down.sh                   # tear it all down"
