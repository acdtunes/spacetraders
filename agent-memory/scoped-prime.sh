#!/usr/bin/env bash
#
# scoped-prime.sh — per-agent memory injection for bd prime.
#
# Runs in place of `bd prime` in the SessionStart / PreCompact hooks. It keeps
# bd's workflow context verbatim but REPLACES the flat "Persistent Memories"
# dump (every memory to every agent) with a salient, agent-scoped section:
# the caller sees only its own memories plus shared fleet directives.
#
# Agent identity comes from $GC_AGENT (fallback $GC_ALIAS) — the bare agent
# name a live gc session carries (e.g. "captain", "shipwright"). Scope is
# resolved from memory-key namespaces ("<owner>:<slug>") and scope-map.tsv;
# see that file for the policy.
#
# SAFETY: on ANY problem (no identity, no jq, no/blank memories, unreadable
# map) it emits plain `bd prime` unchanged — the pre-existing behavior — so a
# session is never worse off than before this shim existed.
#
# Portable to macOS system bash 3.2: no associative arrays; awk does map
# lookups, jq does all JSON handling (safe for values with quotes/newlines).

set -uo pipefail

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
MAP="$SELF_DIR/scope-map.tsv"

# Full bd prime output — always our fallback.
prime="$(bd prime 2>/dev/null)" || { bd prime; exit 0; }

# Everything before the flat memory dump (bd always emits it as the last
# section). We keep this verbatim and rebuild the memory section ourselves.
head_part="$(printf '%s\n' "$prime" | sed '/^## Persistent Memories/,$d')"

agent="${GC_AGENT:-${GC_ALIAS:-}}"

# Bail to plain output if we cannot scope safely.
if [ -z "$agent" ] || ! command -v jq >/dev/null 2>&1; then
  printf '%s\n' "$prime"; exit 0
fi

mem_json="$(bd memories --json 2>/dev/null || true)"
if ! printf '%s' "$mem_json" | jq -e 'type=="object" and length>0' >/dev/null 2>&1; then
  printf '%s\n' "$prime"; exit 0
fi

prefix="$(bd config get issue_prefix 2>/dev/null | tr -d '[:space:]')"

default_owner=""
if [ -f "$MAP" ]; then
  default_owner="$(awk -v p="$prefix" '$1=="default" && $2==p {print $3; exit}' "$MAP" 2>/dev/null)"
fi
[ -n "$default_owner" ] || default_owner="shared"

# owner_of <key> -> prints the owning scope for a memory key.
owner_of() {
  case "$1" in
    *:*) printf '%s' "${1%%:*}"; return ;;  # namespaced key wins
  esac
  local o=""
  [ -f "$MAP" ] && o="$(awk -v k="$1" '$1=="key" && $2==k {print $3; exit}' "$MAP" 2>/dev/null)"
  [ -n "$o" ] && { printf '%s' "$o"; return; }
  printf '%s' "$default_owner"
}

keys="$(printf '%s' "$mem_json" | jq -r 'keys_unsorted[]')"

# emit_bucket <owner>: print every memory owned by <owner>, as markdown.
emit_bucket() {
  printf '%s\n' "$keys" | while IFS= read -r key; do
    [ -n "$key" ] || continue
    [ "$(owner_of "$key")" = "$1" ] || continue
    val="$(printf '%s' "$mem_json" | jq -r --arg k "$key" '.[$k] // ""')"
    printf '### %s\n%s\n\n' "$key" "$val"
  done
}

# Count what this agent will actually see (own + shared).
sel_count="$(printf '%s\n' "$keys" | while IFS= read -r key; do
  [ -n "$key" ] || continue
  o="$(owner_of "$key")"
  if [ "$o" = "$agent" ] || [ "$o" = "shared" ]; then echo x; fi
done | grep -c x || true)"

printf '%s\n' "$head_part"
printf '\n## Your memories — honor these (agent: %s)\n' "$agent"
printf 'Your own scoped memories plus shared fleet directives. READ them now and APPLY them this turn, before you act — they are here because they bind your role, not as background.\n\n'

if [ "${sel_count:-0}" -eq 0 ] 2>/dev/null; then
  printf '_No memories are scoped to you yet. Record durable, role-specific lessons with_ `bd remember --key %s:<slug> "..."`.\n' "$agent"
else
  [ "$agent" != "shared" ] && emit_bucket "shared"   # shared directives first
  emit_bucket "$agent"                               # then your own
fi
