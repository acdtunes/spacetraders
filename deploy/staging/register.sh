#!/usr/bin/env bash
# =============================================================================
# deploy/staging/register.sh (sp-widl) — register an agent into the STAGING db.
#
#   deploy/staging/register.sh <CALLSIGN> '<AGENT_JWT>'
#   deploy/staging/register.sh '<AGENT_JWT>'          # callsign read from the JWT
#
# Writes ONLY to the staging database (config.staging.yaml's database.url, which
# env.sh asserts targets spacetraders_staging). Tokens are masked in all output.
# Committed replacement for the ad-hoc, gitignored staging/register.sh.
#
# NOTE: `player register` calls the live SpaceTraders API to claim the agent.
# Prefer a staging-only account/token so staging never spends the live account's
# request budget (see config.staging.yaml's conservative api.rate_limit).
# =============================================================================
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh" || {
  echo "register.sh: staging env failed its prod-safety checks; refusing." >&2; exit 1; }

if [[ ! -x "$STAGING_CLI_BIN" ]]; then
  echo "register.sh: staging CLI not built ($STAGING_CLI_BIN). Run deploy/staging/up.sh first." >&2
  exit 1
fi

# Args: <CALLSIGN> <JWT>  OR  <JWT> (callsign decoded from the token payload).
if [[ $# -ge 2 && -n "${1:-}" && -n "${2:-}" ]]; then
  AGENT="$1"; TOKEN="$2"
elif [[ $# -eq 1 && -n "${1:-}" ]]; then
  TOKEN="$1"
  b64="$(printf '%s' "$TOKEN" | cut -d. -f2 | tr '_-' '/+')"
  mod=$(( ${#b64} % 4 )); [[ "$mod" -ne 0 ]] && b64="${b64}$(printf '=%.0s' $(seq 1 $((4-mod))))"
  AGENT="$(printf '%s' "$b64" | base64 -D 2>/dev/null | grep -oE '"identifier":"[^"]*"' | sed -E 's/.*:"([^"]*)".*/\1/')"
  [[ -z "$AGENT" ]] && { echo "register.sh: could not read callsign from the JWT; use: register.sh <CALLSIGN> '<JWT>'" >&2; exit 2; }
  echo "callsign from token: $AGENT"
else
  echo "usage: register.sh <CALLSIGN> '<JWT>'   (or just the JWT)" >&2; exit 2
fi

echo "[register] agent '$AGENT' -> staging db '$STAGING_DB'"
# SPACETRADERS_CONFIG + DATABASE_URL both point at the staging db (belt-and-
# suspenders); no --socket, so the CLI writes the token directly to staging.
SPACETRADERS_CONFIG="$STAGING_CONFIG" DATABASE_URL="$STAGING_DB_URL" \
  "$STAGING_CLI_BIN" player register --agent "$AGENT" --token "$TOKEN" --faction COSMIC \
  2>&1 | sed -E 's#eyJ[A-Za-z0-9_.-]{20,}#<TOKEN-MASKED>#g'
