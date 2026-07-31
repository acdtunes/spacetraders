#!/usr/bin/env bash
#
# seed-player.sh — mint TWINAGENT against the RUNNING twin via the bot's own CLI
# (`player register --new`). Refuses live-API + non-test-DB targets; verifies player id 1.
# Usage: twin/scripts/seed-player.sh [--dry-run]
# Env (empty ⇒ default): TWIN_TEST_CONFIG, TWIN_BASE_URL, TEST_DATABASE_URL,
#   TEST_AGENT (TWINAGENT), TEST_FACTION (COSMIC), ST_ACCOUNT_TOKEN (twin-test-account-token)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TWIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"; REPO_ROOT="$(cd "$TWIN_DIR/.." && pwd)"
GOBOT_DIR="$REPO_ROOT/gobot"; CLI_BIN="$GOBOT_DIR/bin/spacetraders"

TWIN_TEST_CONFIG="${TWIN_TEST_CONFIG:-$TWIN_DIR/test-config.yaml}"
TWIN_BASE_URL="${TWIN_BASE_URL:-http://127.0.0.1:8080/v2}"
TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable}"
TEST_AGENT="${TEST_AGENT:-TWINAGENT}"; TEST_FACTION="${TEST_FACTION:-COSMIC}"
ACCOUNT_TOKEN="${ST_ACCOUNT_TOKEN:-twin-test-account-token}"; EXPECTED_PLAYER_ID=1

DRY_RUN=0; if [ "${1:-}" = "--dry-run" ]; then DRY_RUN=1; fi
fail() { echo "REFUSING TO SEED: $*" >&2; exit 1; }

case "$TWIN_BASE_URL" in *spacetraders.io*) fail "base URL '$TWIN_BASE_URL' points at the LIVE SpaceTraders API. Seeding must only hit the local twin (http://127.0.0.1:8080/v2)." ;; esac
case "$TEST_DATABASE_URL" in *spacetraders_test*) : ;; *) fail "DATABASE_URL '$TEST_DATABASE_URL' is not the spacetraders_test DB. Prod (5432/spacetraders) is READ-ONLY." ;; esac
[ -f "$TWIN_TEST_CONFIG" ] || fail "config not found: $TWIN_TEST_CONFIG (run launch-test-stack.sh first)"

echo "register:      $CLI_BIN player register --new --agent $TEST_AGENT --faction $TEST_FACTION"
echo "env:           SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG"
echo "env:           ST_API_BASE_URL=$TWIN_BASE_URL"
echo "env:           DATABASE_URL=$TEST_DATABASE_URL"
echo "env:           ST_ACCOUNT_TOKEN=$ACCOUNT_TOKEN"
if [ "$DRY_RUN" = "1" ]; then echo "dry-run: guards passed; nothing registered."; exit 0; fi

[ -x "$CLI_BIN" ] || make -C "$GOBOT_DIR" build-cli
set +e
OUT="$(cd "$GOBOT_DIR" && env "SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG" "ST_API_BASE_URL=$TWIN_BASE_URL" "DATABASE_URL=$TEST_DATABASE_URL" "ST_ACCOUNT_TOKEN=$ACCOUNT_TOKEN" "$CLI_BIN" player register --new --agent "$TEST_AGENT" --faction "$TEST_FACTION" 2>&1)"
STATUS=$?; set -e
echo "$OUT"
if [ $STATUS -ne 0 ]; then
  if echo "$OUT" | grep -q "an OPEN era"; then echo "test DB already seeded (an OPEN era exists) — nothing to do."; exit 0; fi
  fail "player register --new failed (exit $STATUS). Is the test stack up? Run launch-test-stack.sh first."
fi
PLAYER_ID="$(echo "$OUT" | sed -n 's/.*Player ID:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)"
if [ "$PLAYER_ID" != "$EXPECTED_PLAYER_ID" ]; then
  fail "register minted player id '${PLAYER_ID:-<none>}' but test-config.yaml pins captain.player_id: $EXPECTED_PLAYER_ID. Drop+recreate spacetraders_test, restart the test daemon, reseed."
fi
echo ""; echo "seeded $TEST_AGENT (player id $PLAYER_ID, faction $TEST_FACTION) against $TWIN_BASE_URL."
