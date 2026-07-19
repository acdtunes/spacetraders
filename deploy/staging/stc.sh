#!/usr/bin/env bash
# =============================================================================
# deploy/staging/stc.sh (sp-widl) — run the CLI against the STAGING daemon.
#
#   deploy/staging/stc.sh player info
#   deploy/staging/stc.sh ship list
#
# Every call targets the staging daemon socket + the committed staging config,
# so it can never reach the live daemon. Committed replacement for the ad-hoc,
# gitignored staging/stc.sh (which hardcoded an absolute repo path and grep-
# swapped the live DSN).
# =============================================================================
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh" || {
  echo "stc.sh: staging env failed its prod-safety checks; refusing." >&2; exit 1; }

if [[ ! -x "$STAGING_CLI_BIN" ]]; then
  echo "stc.sh: staging CLI not built ($STAGING_CLI_BIN)." >&2
  echo "  build it: make -C '$GOBOT_DIR' build-cli ENV=staging   (or run deploy/staging/up.sh)" >&2
  exit 1
fi

# Inject --player-id (default 1, the fresh-DB staging agent) only if the caller
# did not pass one, so `stc.sh --player-id 2 ...` still works.
inject_pid=1
for a in "$@"; do
  case "$a" in --player-id|--player-id=*) inject_pid=0 ;; esac
done
pid_args=()
[[ "$inject_pid" == 1 ]] && pid_args=(--player-id "${STAGING_PLAYER_ID:-1}")

exec env SPACETRADERS_CONFIG="$STAGING_CONFIG" \
  "$STAGING_CLI_BIN" --socket "$STAGING_SOCKET" ${pid_args[@]+"${pid_args[@]}"} "$@"
