#!/usr/bin/env bash
# =============================================================================
# deploy/staging/down.sh (sp-widl) — tear the STAGING stack down, cleanly.
#
#   deploy/staging/down.sh              # stop staging daemon + routing, clean sockets/pids
#   deploy/staging/down.sh --drop-db    # ALSO drop the spacetraders_staging database
#
# PROD SAFETY: this script can only ever touch STAGING. It signals a process
# ONLY after confirming (a) its pid came from a staging pidfile and (b) its
# command line carries a staging-unique marker (staging_stop_if_ours, in
# env.sh). It never references a prod pid, socket, port, or launchd label. This
# is the codified fix for the sp-widl near-miss (a hardcoded live pid nearly
# reaped the live daemon).
# =============================================================================
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh" || {
  echo "down.sh: staging env failed its prod-safety checks; refusing to act." >&2; exit 1; }

DROP_DB=0
[[ "${1:-}" == "--drop-db" ]] && DROP_DB=1

staging_env_banner

# --- Stop the staging daemon. Its pid lives in the STAGING pid file (config-
#     derived); the marker is the staging-suffixed binary name, which cannot
#     match the prod daemon ('...-daemon' vs '...-daemon-staging'). ------------
staging_stop_if_ours "$STAGING_PID" "spacetraders-daemon-staging" "daemon"

# --- Stop the staging routing service. Kill the tracked run.sh parent, then
#     sweep any orphaned python child by the STAGING port (:50053, which can
#     never be the prod routing service on :50051). ---------------------------
staging_stop_if_ours "$STAGING_ROUTING_PID" "routing-service/run.sh" "routing"
if pgrep -f "routing-service.*--port $STAGING_ROUTING_PORT" >/dev/null 2>&1; then
  echo "[down] sweeping orphaned staging routing python on :$STAGING_ROUTING_PORT..."
  pkill -f "routing-service.*--port $STAGING_ROUTING_PORT" 2>/dev/null || true
fi

# --- Clean the staging socket (config-derived; asserted to contain 'staging'). -
if [[ -S "$STAGING_SOCKET" || -e "$STAGING_SOCKET" ]]; then
  echo "[down] removing staging socket $STAGING_SOCKET"
  rm -f "$STAGING_SOCKET"
fi

# --- Optional: drop the staging database (explicit opt-in only). --------------
# Guarded by the env.sh assertion that STAGING_DB ends with '_staging', plus a
# re-check here, so this can never drop the live 'spacetraders' database.
if [[ "$DROP_DB" == "1" ]]; then
  [[ "$STAGING_DB" == *_staging ]] || { echo "down.sh: refusing --drop-db: '$STAGING_DB' is not a *_staging database." >&2; exit 1; }
  if command -v psql >/dev/null 2>&1; then
    echo "[down] dropping staging database '$STAGING_DB'..."
    psql "$STAGING_DB_ADMIN_URL" -c "DROP DATABASE IF EXISTS \"$STAGING_DB\"" >/dev/null
  else
    echo "down.sh: psql not found; drop it manually: dropdb -h localhost -p 5432 -U spacetraders $STAGING_DB" >&2
  fi
fi

echo "[down] staging teardown complete."
