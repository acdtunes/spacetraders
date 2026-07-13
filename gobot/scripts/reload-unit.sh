#!/usr/bin/env bash
# Reload ONE launchd unit safely (sp-516j).
#
# THE INCIDENT this fixes: a routine `make deploy-daemon` took the PROD daemon
# DOWN for ~3 minutes. The old reload did cp-plist then a 3x { bootout; sleep 1;
# bootstrap } loop; all three bootstraps hit launchd's transient
# "Bootstrap failed: 5: Input/output error" race, and the failure path was a
# bare `exit 1` that LEFT THE SERVICE BOOTED-OUT — the fleet ran unmanaged until
# a human noticed. The race is TRANSIENT (a single bootstrap seconds later
# recovered it), so the real bugs were (a) far too small a retry budget and
# (b) failing INTO a down state instead of recovering.
#
# TWO PRONGS (see reload_unit below):
#   1. DIFF-GATE: if the freshly-rendered plist is byte-identical to the
#      installed one (the common binary-only deploy), DO NOT bootout/bootstrap.
#      Just `launchctl kickstart -k` to re-exec the new binary in place — no
#      unload, so the I/O race cannot happen at all.
#   2. FAIL-LOUD-BUT-RECOVER: only when the plist genuinely differs do we take
#      the bootout+bootstrap path. There we poll until fully unloaded (not a
#      fixed sleep), retry bootstrap with escalating backoff, and — crucially —
#      run a MANDATORY health assert that keeps (re)bootstrapping until the
#      service is loaded AND running. We NEVER return leaving it booted-out;
#      only after the whole recovery deadline is exhausted do we `exit 1`
#      LOUDLY, printing the exact manual recovery command.
#
# This lives in a script (not inline in the Makefile) so the decision logic is
# reviewable and verifiable in isolation: every launchctl/sleep call is routed
# through an injectable indirection ($LAUNCHCTL / RELOAD_SLEEP) so a mock can
# drive both branches without a real launchd. See scripts/render-launchd.sh for
# the sibling extract-the-shell-out-of-the-Makefile pattern (sp-898q).
set -euo pipefail

LABEL="${1:?usage: reload-unit.sh <launchd-label>   (e.g. com.spacetraders.daemon)}"

# Directories come from the Makefile via env (mirrors render-launchd.sh). Fail
# loudly if unset rather than operate on an empty path.
: "${LAUNCHD_OUT:?LAUNCHD_OUT must be set (the rendered-plist build dir)}"
: "${AGENTS_DIR:?AGENTS_DIR must be set (~/Library/LaunchAgents)}"

# --- Injectable dependencies (production defaults; overridden by tests) -------
# LAUNCHCTL     : the launchctl binary. Tests point this at a mock recorder.
# RELOAD_SLEEP  : the sleep command. Tests set `true` to make backoff instant.
# RELOAD_UID    : the gui-domain uid. Defaults to the real uid.
# RELOAD_RECOVER_SECS : hard recovery deadline (s) before we give up + exit 1.
# RELOAD_UNLOAD_POLLS : max polls waiting for a bootout to fully take effect.
LAUNCHCTL="${LAUNCHCTL:-launchctl}"
uid="${RELOAD_UID:-$(id -u)}"
RECOVER_SECS="${RELOAD_RECOVER_SECS:-60}"
UNLOAD_POLLS="${RELOAD_UNLOAD_POLLS:-15}"

rendered="$LAUNCHD_OUT/$LABEL.plist"
installed="$AGENTS_DIR/$LABEL.plist"
target="gui/$uid/$LABEL"

# Refuse to run without a rendered plist to install/compare against (mirrors
# render-launchd.sh's "no templates" guard).
if [ ! -f "$rendered" ]; then
  echo "reload-unit: rendered plist not found: $rendered (run 'make launchd' first)" >&2
  exit 1
fi

warn() { echo "$@" >&2; }
run_launchctl() { "$LAUNCHCTL" "$@"; }
do_sleep() { "${RELOAD_SLEEP:-sleep}" "$@" >/dev/null 2>&1 || true; }
now() { date +%s; }

# is_loaded: launchctl print exits 0 iff the service is bootstrapped in the
#            domain (exits 113 "Could not find service" otherwise).
is_loaded() { run_launchctl print "$target" >/dev/null 2>&1; }

# is_running: the service is loaded AND has a live process. `launchctl print`
#             emits `state = running` plus a `pid = N` line for a live service
#             (confirmed on macOS 15). Either signal counts as healthy.
is_running() { run_launchctl print "$target" 2>/dev/null | grep -Eq 'state = running|pid = [0-9]+'; }

# wait_unloaded: PRONG 2, item 1 — poll until the service is genuinely gone
# after a bootout, rather than a fixed `sleep 1`. Bounded; returns regardless so
# assert_running_or_recover remains the backstop.
wait_unloaded() {
  local left="$UNLOAD_POLLS"
  while [ "$left" -gt 0 ]; do
    is_loaded || return 0
    do_sleep 1
    left=$(( left - 1 ))
  done
  return 0
}

# assert_running_or_recover: PRONG 2, items 2+3 — the universal safety net.
# Drives the service to loaded+running FROM ANY STATE, within a hard deadline:
#   - already running        -> success.
#   - loaded but not running -> poll (it may still be starting).
#   - not loaded             -> (re)bootstrap; on the transient I/O race, re-
#                               bootout to a clean slate and back off (escalating,
#                               capped), then retry.
# The loop only ever exits via `is_running` (success) or deadline exhaustion, so
# we can never fall through leaving the service booted-out. On exhaustion we make
# a final best-effort bootstrap (leave it loaded, not down) and fail LOUD with
# the exact manual recovery command.
assert_running_or_recover() {
  local deadline delay attempt
  deadline=$(( $(now) + RECOVER_SECS ))
  delay=1
  attempt=0
  while [ "$(now)" -lt "$deadline" ]; do
    if is_running; then
      echo "reload-unit $LABEL: healthy (loaded + running)"
      return 0
    fi
    if is_loaded; then
      # Loaded but not yet reporting a live process — give it a beat instead of
      # thrashing bootstrap.
      do_sleep 1
      continue
    fi
    # Fully unloaded here (is_loaded just returned false — the poll-until-
    # unloaded guarantee), so it is safe to bootstrap.
    attempt=$(( attempt + 1 ))
    if run_launchctl bootstrap "gui/$uid" "$installed" 2>/dev/null; then
      do_sleep 1
      continue
    fi
    warn "reload-unit $LABEL: bootstrap attempt $attempt hit the launchd I/O race — re-bootout + backoff ${delay}s"
    run_launchctl bootout "$target" 2>/dev/null || true
    wait_unloaded
    do_sleep "$delay"
    delay=$(( delay * 2 ))
    if [ "$delay" -gt 8 ]; then delay=8; fi
  done

  # Recovery deadline exhausted. Leave it bootstrapped if we possibly can (never
  # walk away with the fleet down), then fail LOUDLY with the manual fix.
  run_launchctl bootstrap "gui/$uid" "$installed" 2>/dev/null || true
  warn "reload-unit $LABEL FAILED: service is not running after ${RECOVER_SECS}s of recovery attempts."
  warn "MANUAL RECOVERY — run this now, the fleet may be unmanaged:"
  warn "    launchctl bootstrap gui/$uid \"$installed\""
  warn "    launchctl print gui/$uid/$LABEL | grep -E 'state|pid'"
  exit 1
}

reload_unit() {
  if [ -f "$installed" ] && cmp -s "$rendered" "$installed"; then
    # PRONG 1 — plist unchanged: re-exec the new binary IN PLACE. No unload, so
    # the "Input/output error" race is structurally impossible on this path.
    echo "reload-unit $LABEL: plist unchanged — kickstart in place (no bootout/bootstrap)"
    if ! run_launchctl kickstart -k "$target" 2>/dev/null; then
      # kickstart requires the service to be loaded; if it wasn't, fall through
      # to the recovery net, which will bootstrap it from the installed plist.
      warn "reload-unit $LABEL: kickstart failed (service not loaded?) — recovering via bootstrap"
    fi
  else
    # PRONG 2 — plist genuinely differs (or first install): install the new
    # plist, bootout, poll until unloaded, then let the recovery net bootstrap
    # it back with retry/backoff + a mandatory running assert.
    echo "reload-unit $LABEL: plist changed — installing new plist + full reload"
    cp "$rendered" "$installed"
    run_launchctl bootout "$target" 2>/dev/null || true
    wait_unloaded
  fi

  # Both paths converge here: guarantee the service ends up loaded AND running.
  assert_running_or_recover
  echo "reload-unit $LABEL: deployed + running"
}

reload_unit
