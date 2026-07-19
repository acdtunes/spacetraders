#!/usr/bin/env bash
# =============================================================================
# deploy/staging/env.sh (sp-widl) — the single source of truth + prod-safety
# gate for the STAGING environment. SOURCE this (do not execute it); every
# other deploy/staging/*.sh sources it first.
#
# It resolves the staging identifiers from gobot/config.staging.yaml — the SAME
# file the staging daemon loads via SPACETRADERS_CONFIG — so the scripts and the
# daemon can never drift. It then HARD-ASSERTS, fail-closed, that every mutable
# resource is disjoint from prod and self-identifies as staging. If any check
# fails the source returns non-zero and the caller aborts before touching
# anything. This is the barrier that makes the sp-widl near-miss (a hardcoded
# LIVE pid nearly reaped the live daemon) structurally impossible: nothing here
# is hardcoded to a live path, and the guards refuse anything that looks live.
# =============================================================================

# Refuse execution — this file only makes sense sourced.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  echo "deploy/staging/env.sh must be SOURCED, not executed: 'source deploy/staging/env.sh'" >&2
  exit 2
fi

# --- Checkout-relative roots (NEVER hardcode an absolute /Users path) --------
# Works identically from the prod checkout or any git worktree.
STAGING_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$STAGING_DIR/../.." && pwd)"
GOBOT_DIR="$REPO_ROOT/gobot"
export STAGING_ENV="staging"

# --- The committed staging config (loaded by the daemon AND parsed here) -----
# STAGING_CONFIG_OVERRIDE exists so the fail-closed guards below can be exercised
# against a crafted config in tests; it defaults to the committed staging config.
# It changes only WHICH file is parsed — the guards run against it either way, so
# it can never be used to slip a live-pointing config past the safety checks.
STAGING_CONFIG="${STAGING_CONFIG_OVERRIDE:-$GOBOT_DIR/config.staging.yaml}"
if [[ ! -f "$STAGING_CONFIG" ]]; then
  echo "staging/env.sh: FATAL: missing $STAGING_CONFIG" >&2; return 1
fi

# --- Staging binaries (produced by 'make build ENV=staging') -----------------
STAGING_DAEMON_BIN="$GOBOT_DIR/bin/spacetraders-daemon-staging"
STAGING_CLI_BIN="$GOBOT_DIR/bin/spacetraders-staging"

# --- Non-safety-critical ports (functional, not isolation-critical). A
#     mismatch here fails loudly at connect time, never silently hits prod. ---
STAGING_ROUTING_PORT="50053"   # must match routing.address in config.staging.yaml
STAGING_METRICS_PORT="9095"    # must match metrics.port in config.staging.yaml

# --- Runtime artifacts (pids/logs) live in a gitignored run dir, never mixed
#     with the prod gobot/daemon.log / gobot/routing.log. --------------------
STAGING_RUN_DIR="$STAGING_DIR/run"
STAGING_DAEMON_LOG="$STAGING_RUN_DIR/daemon.log"
STAGING_ROUTING_LOG="$STAGING_RUN_DIR/routing.log"
# Routing pid file this script family manages (run.sh writes none). The daemon
# writes its OWN pid to STAGING_PID (config-derived) on start.
STAGING_ROUTING_PID="$STAGING_RUN_DIR/routing.pid"

# --- Prod literals we must NEVER collide with (documented in
#     internal/infrastructure/config/staging_isolation_test.go). --------------
readonly PROD_SOCKET="/tmp/spacetraders-daemon.sock"
readonly PROD_PID="/tmp/spacetraders-daemon.pid"
readonly PROD_DB="spacetraders"

# --- Extract the SAFETY-CRITICAL identifiers from the config (single source of
#     truth: whatever the daemon will actually bind). --------------------------
_yaml_scalar() {
  # _yaml_scalar <key-regex> <file>: first "key: value" scalar, comment/quotes stripped.
  grep -E "^[[:space:]]*$1:" "$2" | head -1 \
    | sed -E "s/^[[:space:]]*$1:[[:space:]]*//; s/[[:space:]]+#.*//; s/[[:space:]]+$//; s/^[\"']//; s/[\"']$//"
}

STAGING_SOCKET="$(_yaml_scalar 'socket_path' "$STAGING_CONFIG")"
STAGING_PID="$(_yaml_scalar 'pid_file' "$STAGING_CONFIG")"
STAGING_DB_URL="$(_yaml_scalar 'url' "$STAGING_CONFIG")"
# DB name = last path segment of the URL, query string stripped.
STAGING_DB="$(printf '%s' "$STAGING_DB_URL" | sed -E 's#.*/##; s/\?.*//')"
# Maintenance URL (connect to the 'postgres' db to CREATE the staging db).
STAGING_DB_ADMIN_URL="$(printf '%s' "$STAGING_DB_URL" | sed -E 's#/[^/?]+(\?|$)#/postgres\1#')"

# --- FAIL-CLOSED prod-safety assertions --------------------------------------
_staging_die() { echo "staging/env.sh: FATAL: $*" >&2; return 1; }

[[ -n "$STAGING_SOCKET" ]] || { _staging_die "could not resolve socket_path from $STAGING_CONFIG"; return 1; }
[[ -n "$STAGING_PID"    ]] || { _staging_die "could not resolve pid_file from $STAGING_CONFIG"; return 1; }
[[ -n "$STAGING_DB_URL" ]] || { _staging_die "could not resolve database.url from $STAGING_CONFIG"; return 1; }
[[ -n "$STAGING_DB"     ]] || { _staging_die "could not resolve staging DB name from database.url"; return 1; }

# 1) Nothing may equal a prod resource.
[[ "$STAGING_SOCKET" != "$PROD_SOCKET" ]] || { _staging_die "staging socket equals PROD socket ($PROD_SOCKET)"; return 1; }
[[ "$STAGING_PID"    != "$PROD_PID"    ]] || { _staging_die "staging pid equals PROD pid ($PROD_PID)"; return 1; }
[[ "$STAGING_DB"     != "$PROD_DB"     ]] || { _staging_die "staging DB equals PROD DB ($PROD_DB)"; return 1; }

# 2) Everything must self-identify as staging (a grep can never confuse them).
[[ "$STAGING_SOCKET" == *staging* ]] || { _staging_die "staging socket '$STAGING_SOCKET' does not contain 'staging'"; return 1; }
[[ "$STAGING_PID"    == *staging* ]] || { _staging_die "staging pid '$STAGING_PID' does not contain 'staging'"; return 1; }
[[ "$STAGING_DB"     == *_staging ]] || { _staging_die "staging DB '$STAGING_DB' does not end with '_staging'"; return 1; }
[[ "$STAGING_DB_URL" == *staging* ]] || { _staging_die "staging DB URL does not target a *staging* database"; return 1; }
[[ "$STAGING_DAEMON_BIN" == *-staging ]] || { _staging_die "staging daemon binary name is not '-staging' suffixed"; return 1; }

export STAGING_DIR REPO_ROOT GOBOT_DIR STAGING_CONFIG \
       STAGING_DAEMON_BIN STAGING_CLI_BIN \
       STAGING_ROUTING_PORT STAGING_METRICS_PORT \
       STAGING_RUN_DIR STAGING_DAEMON_LOG STAGING_ROUTING_LOG STAGING_ROUTING_PID \
       STAGING_SOCKET STAGING_PID STAGING_DB STAGING_DB_URL STAGING_DB_ADMIN_URL

# staging_stop_if_ours <pidfile> <marker> <label>: stop a process we started,
# but ONLY after confirming it is ours. The pid is read from OUR pidfile, and
# before any signal is sent the process command line MUST contain <marker>
# (a staging-unique token). This is the teardown-side guard against the sp-widl
# near-miss: a stale/reused pid that happens to be the live daemon is refused,
# never signalled. Always removes the (now-stale) pidfile.
staging_stop_if_ours() {
  local pidfile="$1" marker="$2" label="$3" pid=""
  [[ -f "$pidfile" ]] || { echo "[stop] no $label pidfile ($pidfile) — nothing to stop."; return 0; }
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  if [[ -z "$pid" ]]; then rm -f "$pidfile"; return 0; fi         # empty → stale, clean up
  if kill -0 "$pid" 2>/dev/null; then
    if ps -p "$pid" -o command= 2>/dev/null | grep -q -- "$marker"; then
      echo "[stop] stopping staging $label (pid $pid, matched '$marker')..."
      kill "$pid" 2>/dev/null || true
      sleep 1
      kill -9 "$pid" 2>/dev/null || true
      rm -f "$pidfile"                                            # ours + stopped → clean up
    else
      # A LIVE process that is not ours: never signal it, and never delete its
      # pidfile — leave everything for the operator to resolve.
      echo "[stop] REFUSING: pid $pid ($pidfile) is NOT the staging $label (no '$marker' in its command) — left untouched." >&2
    fi
  else
    echo "[stop] staging $label (pid $pid) not running."
    rm -f "$pidfile"                                             # dead pid → stale, clean up
  fi
}

# A concise banner so every script prints exactly what it will touch.
staging_env_banner() {
  cat >&2 <<EOF
[staging env] repo    : $REPO_ROOT
[staging env] config  : $STAGING_CONFIG
[staging env] daemon  : $STAGING_DAEMON_BIN
[staging env] socket  : $STAGING_SOCKET   (prod: $PROD_SOCKET)
[staging env] pid     : $STAGING_PID   (prod: $PROD_PID)
[staging env] db      : $STAGING_DB   (prod: $PROD_DB)
[staging env] routing : localhost:$STAGING_ROUTING_PORT
[staging env] metrics : 127.0.0.1:$STAGING_METRICS_PORT
EOF
}
