#!/bin/sh
# install-bd-router.sh — activate bd-router machine-locally.
#
# Points the first `bd` on PATH (~/.local/bin/bd) at ./bd-router so every shell
# and gc-launched agent session that runs `bd` gets prefix-aware database
# routing. Idempotent and re-runnable. The real bd binary is left untouched;
# bd-router finds it at runtime by scanning PATH (or $BD_REAL).
#
# The symlink swap is machine-local (not committed). Re-run this after any bd
# reinstall that rewrites ~/.local/bin/bd. Undo with: install-bd-router.sh --uninstall

set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
ROUTER="$DIR/bd-router"
LINK="${BD_LINK:-$HOME/.local/bin/bd}"
BACKUP="$DIR/.bd-router.backup"   # machine-local; records the original target

[ -x "$ROUTER" ] || { printf 'install-bd-router: %s missing or not executable\n' "$ROUTER" >&2; exit 1; }

# Resolve a path through symlinks (portable).
resolve() {
	p=$1; n=0
	while [ -L "$p" ] && [ "$n" -lt 40 ]; do
		t=$(readlink "$p")
		case $t in /*) p=$t ;; *) p=$(dirname -- "$p")/$t ;; esac
		n=$((n + 1))
	done
	printf '%s' "$p"
}

if [ "${1:-}" = "--uninstall" ]; then
	if [ -f "$BACKUP" ]; then
		orig=$(cat "$BACKUP")
		rm -f "$LINK"
		ln -s "$orig" "$LINK"
		rm -f "$BACKUP"
		printf 'install-bd-router: restored %s -> %s\n' "$LINK" "$orig"
	else
		printf 'install-bd-router: no backup found; leaving %s as-is\n' "$LINK" >&2
	fi
	exit 0
fi

# Confirm a real (non-router) bd will still be reachable after the swap, so we
# never leave the machine without a working bd.
real=""
oifs=$IFS; IFS=:
for d in $PATH; do
	c="$d/bd"
	[ -x "$c" ] || continue
	[ "$(resolve "$c")" = "$ROUTER" ] && continue   # skip the router itself
	real=$c; break
done
IFS=$oifs
if [ -z "$real" ]; then
	printf 'install-bd-router: no real bd found on PATH besides the router; aborting\n' >&2
	exit 1
fi

# Idempotent: already active?
if [ -L "$LINK" ] && [ "$(resolve "$LINK")" = "$ROUTER" ]; then
	printf 'install-bd-router: already active (%s -> %s)\n' "$LINK" "$ROUTER"
	printf 'install-bd-router: real bd = %s\n' "$real"
	exit 0
fi

mkdir -p "$(dirname -- "$LINK")"

# Record the current target for --uninstall (only if not already backed up).
if [ -e "$LINK" ] || [ -L "$LINK" ]; then
	if [ ! -f "$BACKUP" ]; then
		if [ -L "$LINK" ]; then resolve "$LINK" > "$BACKUP"; else printf '%s' "$LINK" > "$BACKUP"; fi
	fi
fi

rm -f "$LINK"
ln -s "$ROUTER" "$LINK"

printf 'install-bd-router: %s -> %s\n' "$LINK" "$ROUTER"
printf 'install-bd-router: real bd = %s (backup: %s)\n' "$real" "$BACKUP"
