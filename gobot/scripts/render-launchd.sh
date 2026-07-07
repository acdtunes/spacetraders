#!/usr/bin/env bash
# Renders committed launchd templates (deploy/launchd/*.plist.template) into
# concrete plists by substituting machine-specific paths. The committed template
# is the source of truth: load-bearing keys (BD_REAL, ExitTimeOut, the gobot
# path) can no longer be silently dropped by a hand-edit that the next regen
# clobbers — the historical whole-fleet outage when BD_REAL went missing
# (sp-sk68). See deploy/launchd/README.md. (sp-898q)
set -euo pipefail

TEMPLATE_DIR="${1:?usage: render-launchd.sh <template-dir> <output-dir>}"
OUT_DIR="${2:?usage: render-launchd.sh <template-dir> <output-dir>}"

# Substitution inputs — the Makefile passes these; fail loudly if any is unset
# rather than render a plist with an empty path.
: "${LAUNCHD_HOME:?LAUNCHD_HOME must be set}"
: "${GOBOT_DIR:?GOBOT_DIR must be set}"
: "${REPO_ROOT:?REPO_ROOT must be set}"
: "${BD_REAL:?BD_REAL must be set}"

mkdir -p "$OUT_DIR"

shopt -s nullglob
templates=("$TEMPLATE_DIR"/*.plist.template)
if [ ${#templates[@]} -eq 0 ]; then
  echo "error: no *.plist.template files in $TEMPLATE_DIR" >&2
  exit 1
fi

for tpl in "${templates[@]}"; do
  base="$(basename "$tpl" .template)"   # e.g. com.spacetraders.captain.plist
  out="$OUT_DIR/$base"
  # '#' delimiter: the substituted values are filesystem paths full of '/'.
  sed \
    -e "s#@@HOME@@#${LAUNCHD_HOME}#g" \
    -e "s#@@GOBOT_DIR@@#${GOBOT_DIR}#g" \
    -e "s#@@REPO_ROOT@@#${REPO_ROOT}#g" \
    -e "s#@@BD_REAL@@#${BD_REAL}#g" \
    "$tpl" > "$out"
  # A surviving @@PLACEHOLDER@@ means a typo'd or unset variable slipped a blank
  # into a load-bearing path — refuse to emit a broken plist.
  if grep -q '@@[A-Z_]*@@' "$out"; then
    echo "error: unrendered placeholder(s) in $out:" >&2
    grep -n '@@[A-Z_]*@@' "$out" >&2
    exit 1
  fi
  echo "rendered $out"
done
