#!/usr/bin/env bash
#
# migrate-game.sh — move the SpaceTraders gobot game between the Mac (launchd)
# and the GCP VM (systemd), in either direction, in one command.
#
#   ./scripts/migrate-game.sh to-vm       # Mac  -> VM
#   ./scripts/migrate-game.sh to-local    # VM   -> Mac
#
# Flags:
#   --no-metrics   skip the Prometheus/Grafana volume transfer (game only)
#   --keep-vm      (to-local) leave the instance RUNNING instead of stopping it
#   --yes          skip the confirmation prompt
#
# The game DB always rides with the daemon (co-located — a remote DB is too slow).
# Metrics are pruned to the current era before transfer so the volume stays small
# and the transfer survives a flaky connection. See the runbook bead in the
# spacetraders tracker for the why behind each step.
#
set -uo pipefail

### ─────────────────────────── CONFIG ───────────────────────────
PROJECT="pj-fisitalview-hml"
ZONE="us-east4-c"
INSTANCE="spacetraders-gobot"
VM_USER="andres.dandrea"
SSH_KEY="$HOME/.ssh/google_compute_engine"
GOBOT="$HOME/IdeaProjects/cities/spacetraders/gobot"
SCRATCH="${TMPDIR:-/tmp}/st-migrate"; mkdir -p "$SCRATCH"

FW_SSH="fmd36-allow-ssh"
FW_METRICS="fmd36-allow-metrics-ui"
PG_CONTAINER="spacetraders-postgres"
PROM_VOL="gobot_prometheus_data"
GRAF_VOL="gobot_grafana_data"
DB="spacetraders"; DBUSER="spacetraders"

# Unattended auth: point ST_MIGRATE_SA_KEY at a service-account key JSON to skip
# the interactive `gcloud auth login` on every flip (see setup notes in the bead).
SA_KEY="${ST_MIGRATE_SA_KEY:-}"

# Weekly universe reset lands ~13:00 UTC on the era resetDate; prune metrics blocks
# older than the current era so only live-era data is carried.
KEEP_METRICS=1; STOP_VM=1; ASSUME_YES=0

### ─────────────────────────── PLUMBING ─────────────────────────
c(){ printf '\033[1;36m'; printf '%s' "$*"; printf '\033[0m\n'; }
ok(){ printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
warn(){ printf '\033[1;33m! %s\033[0m\n' "$*" >&2; }
die(){ printf '\033[1;31mFATAL: %s\033[0m\n' "$*" >&2; exit 1; }

GC(){ gcloud "$@" --project="$PROJECT" --quiet; }

ensure_auth(){
  if [[ -n "$SA_KEY" && -f "$SA_KEY" ]]; then
    gcloud auth activate-service-account --key-file="$SA_KEY" --quiet >/dev/null 2>&1 \
      && { ok "authenticated via service account"; return; }
    warn "SA key present but activation failed; falling back to user auth"
  fi
  gcloud auth print-access-token >/dev/null 2>&1 \
    || die "gcloud auth expired — run: gcloud auth login  (or set ST_MIGRATE_SA_KEY)"
}

my_ip(){ curl -4 -s --max-time 8 ifconfig.me || die "cannot determine public IP"; }

# CGNAT rotates the IP constantly — re-point the firewall to the CURRENT IP right
# before every network phase.
repoint_fw(){
  local ip; ip=$(my_ip)
  GC compute firewall-rules update "$FW_SSH"     --source-ranges="$ip/32" >/dev/null
  GC compute firewall-rules update "$FW_METRICS" --source-ranges="$ip/32" >/dev/null
  ok "firewall → $ip"
}

VMIP=""
vm_ip(){ GC compute instances describe "$INSTANCE" --zone="$ZONE" \
  --format="value(networkInterfaces[0].accessConfigs[0].natIP)"; }
vm_status(){ GC compute instances describe "$INSTANCE" --zone="$ZONE" --format="value(status)"; }

SSH(){ ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=8 -i "$SSH_KEY" "$VM_USER@$VMIP" "$@"; }

wait_ssh(){ local i; for i in $(seq 1 30); do SSH true 2>/dev/null && return 0; sleep 3; done; return 1; }

confirm(){ [[ $ASSUME_YES -eq 1 ]] && return 0
  read -r -p "$1 [y/N] " a; [[ "$a" =~ ^[Yy]$ ]]; }

# du of a docker volume, in bytes, via a throwaway container.
vol_bytes(){ docker run --rm -v "$1":/v alpine sh -c 'du -sb /v | cut -f1'; }
vol_bytes_ssh(){ SSH "docker run --rm -v $1:/v alpine sh -c 'du -sb /v | cut -f1'"; }

### ───────────────── metrics: prune-to-era + transfer ────────────
# Prune local Prometheus TSDB to the current era, then stream volumes local→VM
# (or VM→local). Retries once on truncation.
era_cutoff_ms(){
  # resetDate (YYYY-MM-DD) of the open era, at 13:00 UTC, in ms — minus a 12h margin
  local rd; rd=$("$GOBOT/bin/spacetraders" universe status 2>/dev/null \
    | awk '/Era resetDate/{print $3}')
  [[ -n "$rd" ]] || { echo 0; return; }
  local s; s=$(date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "${rd}T01:00:00Z" +%s 2>/dev/null) || { echo 0; return; }
  echo $(( s * 1000 ))
}

prune_local_metrics(){
  local cut; cut=$(era_cutoff_ms)
  [[ "$cut" -gt 0 ]] || { warn "no era cutoff; skipping prune"; return; }
  docker stop "$PROM_VOL" >/dev/null 2>&1 || docker stop spacetraders-prometheus >/dev/null 2>&1 || true
  docker run --rm -e CM="$cut" -v "$PROM_VOL":/p alpine sh -c '
    apk add --no-cache jq >/dev/null 2>&1
    r=0
    for b in /p/[0-9A-Z]*/; do
      [ -f "$b/meta.json" ] || continue
      mx=$(jq -r ".maxTime" "$b/meta.json" 2>/dev/null); case "$mx" in ""|null) continue;; esac
      [ "$mx" -lt "$CM" ] && { rm -rf "$b"; r=$((r+1)); }
    done
    echo "pruned $r pre-era blocks"'
  docker start spacetraders-prometheus >/dev/null 2>&1 || true
}

# stream_volume SRC_IS_LOCAL VOL  — pushes local→VM if $1=1, pulls VM→local if $1=0
stream_volume(){
  local from_local="$1" vol="$2" comp="$3" size_src size_dst
  if [[ "$from_local" == 1 ]]; then
    SSH "docker stop spacetraders-prometheus spacetraders-grafana >/dev/null 2>&1; \
         docker run --rm -v $vol:/v alpine sh -c 'rm -rf /v/* /v/.[!.]* 2>/dev/null || true'"
    docker run --rm -v "$vol":/v alpine tar c${comp}f - -C /v . \
      | SSH "docker run --rm -i -v $vol:/v alpine tar x${comp}f - -C /v && sync"
    size_src=$(vol_bytes "$vol"); size_dst=$(vol_bytes_ssh "$vol")
  else
    docker run --rm -v "$vol":/v alpine sh -c 'rm -rf /v/* /v/.[!.]* 2>/dev/null || true'
    SSH "docker run --rm -v $vol:/v alpine tar c${comp}f - -C /v ." \
      | docker run --rm -i -v "$vol":/v alpine tar x${comp}f - -C /v && sync
    size_src=$(vol_bytes_ssh "$vol"); size_dst=$(vol_bytes "$vol")
  fi
  # truncation guard: dst must be within 5% of src
  if [[ "$size_src" -gt 0 ]] && (( size_dst * 100 < size_src * 95 )); then
    warn "$vol truncated ($size_dst/$size_src) — retrying once"
    stream_volume "$from_local" "$vol" "$comp"
  fi
}

### ─────────────────────────── to-vm ────────────────────────────
migrate_to_vm(){
  c "── Mac → VM ──"
  [[ "$(launchctl list | grep -c com.spacetraders.daemon)" -gt 0 ]] || warn "local daemon not running?"

  c "1/8 rebuild linux binaries (from current source)"
  rm -rf "$SCRATCH/linux-bin"; mkdir -p "$SCRATCH/linux-bin"
  ( cd "$GOBOT" && for d in cmd/*/; do
      GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$SCRATCH/linux-bin/$(basename "$d")" "./$d" \
        || die "build failed: $d"; done )
  ok "binaries built"

  c "2/8 start VM + firewall"
  [[ "$(vm_status)" == RUNNING ]] || GC compute instances start "$INSTANCE" --zone="$ZONE" >/dev/null
  VMIP=$(vm_ip); ok "VM $VMIP"; repoint_fw
  wait_ssh || die "VM SSH unreachable"

  c "3/8 ship source + binaries"
  rsync -az -e "ssh -i $SSH_KEY" \
    --exclude=.git --exclude=.captain-worktrees --exclude=services/routing-service/venv \
    --exclude=bin --exclude='*.log' --exclude=captain --exclude=spacetraders-daemon \
    --exclude=outputs --exclude=analysis "$GOBOT/" "$VM_USER@$VMIP:~/gobot/"
  rsync -az -e "ssh -i $SSH_KEY" "$SCRATCH/linux-bin/" "$VM_USER@$VMIP:~/gobot/bin/"
  SSH 'chmod +x ~/gobot/bin/*; sed -i "/^metrics:/,/^[a-z_]*:/ s/host: localhost/host: 0.0.0.0/" ~/gobot/config.yaml; \
       cd ~/gobot/services/routing-service && ./venv/bin/pip install -q -r requirements.txt >/dev/null 2>&1; \
       ./venv/bin/python3 -c "import ortools,grpc" && echo venv-ok'
  ok "shipped"

  confirm "Stop the LOCAL game and cut over to the VM now?" || die "aborted"

  c "4/8 stop local + dump"
  local U; U=$(id -u)
  launchctl bootout "gui/$U/com.spacetraders.captain" 2>/dev/null || true
  launchctl bootout "gui/$U/com.spacetraders.daemon"  2>/dev/null || true
  local i; for i in $(seq 1 25); do pgrep -f bin/spacetraders-daemon >/dev/null || break; sleep 3; done
  launchctl bootout "gui/$U/com.spacetraders.routing" 2>/dev/null || true
  pkill -f "routing-service/run.sh" 2>/dev/null || true; pkill -f "server/main.py" 2>/dev/null || true
  mkdir -p ~/Library/LaunchAgents/disabled-spacetraders
  mv ~/Library/LaunchAgents/com.spacetraders.{daemon,routing,captain}.plist \
     ~/Library/LaunchAgents/disabled-spacetraders/ 2>/dev/null || true
  docker exec "$PG_CONTAINER" pg_dump -U "$DBUSER" -Fc -Z6 "$DB" > "$SCRATCH/dump.pgdump"
  [[ "$(stat -f %z "$SCRATCH/dump.pgdump")" -gt 10000000 ]] || die "dump suspiciously small — aborting before wipe"
  ok "dumped $(du -h "$SCRATCH/dump.pgdump" | cut -f1)"

  c "5/8 transfer + restore on VM"
  repoint_fw
  rsync -z -e "ssh -i $SSH_KEY" "$SCRATCH/dump.pgdump" "$VM_USER@$VMIP:~/dump.pgdump"
  SSH 'set -e; docker start '"$PG_CONTAINER"' >/dev/null
    for i in $(seq 1 20); do docker exec '"$PG_CONTAINER"' pg_isready -U '"$DBUSER"' >/dev/null 2>&1 && break; sleep 1; done
    docker exec '"$PG_CONTAINER"' psql -U '"$DBUSER"' -d postgres -q \
      -c "DROP DATABASE IF EXISTS '"$DB"' WITH (FORCE);" -c "CREATE DATABASE '"$DB"' OWNER '"$DBUSER"';"
    docker cp ~/dump.pgdump '"$PG_CONTAINER"':/tmp/d.pgdump >/dev/null
    docker exec '"$PG_CONTAINER"' pg_restore -U '"$DBUSER"' -d '"$DB"' -j2 --no-owner --exit-on-error /tmp/d.pgdump
    docker exec '"$PG_CONTAINER"' rm /tmp/d.pgdump; rm ~/dump.pgdump'
  ok "restored"

  c "6/8 start game on VM"
  SSH 'sudo systemctl enable --now spacetraders-routing >/dev/null 2>&1
    for i in $(seq 1 40); do ss -tln 2>/dev/null | grep -q ":50051" && break; sleep 1; done
    sudo systemctl enable --now spacetraders-daemon >/dev/null 2>&1; sleep 12
    systemctl is-active spacetraders-routing spacetraders-daemon'
  verify_trading_ssh

  if [[ $KEEP_METRICS == 1 ]]; then
    c "7/8 metrics (prune-to-era → stream)"
    prune_local_metrics
    docker stop spacetraders-prometheus spacetraders-grafana >/dev/null 2>&1 || true
    repoint_fw
    stream_volume 1 "$PROM_VOL" ""
    stream_volume 1 "$GRAF_VOL" "z"
    SSH "docker start spacetraders-prometheus spacetraders-grafana >/dev/null
         docker update --restart unless-stopped $PG_CONTAINER spacetraders-prometheus spacetraders-grafana >/dev/null"
    ok "metrics on VM"
  else c "7/8 metrics skipped (--no-metrics)"; fi

  c "8/8 done — game LIVE on VM $VMIP"
  ok "Grafana http://$VMIP:3000 · Prometheus http://$VMIP:9091"
}

### ─────────────────────────── to-local ─────────────────────────
migrate_to_local(){
  c "── VM → Mac ──"
  [[ "$(vm_status)" == RUNNING ]] || die "VM not running"
  VMIP=$(vm_ip); repoint_fw; wait_ssh || die "VM SSH unreachable"

  c "1/8 rebuild local (darwin) binaries"
  ( cd "$GOBOT" && make build-daemon build-cli build-watchkeeper >/dev/null ) || die "make build failed"
  "$GOBOT/bin/spacetraders-daemon" --help >/dev/null 2>&1 || die "local daemon binary broken"
  ok "binaries rebuilt"

  confirm "Stop the VM game and cut over to LOCAL now?" || die "aborted"

  c "2/8 stop VM game + dump"
  SSH 'sudo systemctl stop spacetraders-daemon spacetraders-routing; sleep 2
    docker exec '"$PG_CONTAINER"' pg_dump -U '"$DBUSER"' -Fc -Z6 '"$DB"' > ~/dump.pgdump; ls -l ~/dump.pgdump | awk "{print \$5}"'

  c "3/8 transfer home"
  repoint_fw
  rsync -z -e "ssh -i $SSH_KEY" "$VM_USER@$VMIP:~/dump.pgdump" "$SCRATCH/dump.pgdump"
  [[ "$(stat -f %z "$SCRATCH/dump.pgdump")" -gt 10000000 ]] || die "dump suspiciously small — aborting before wipe"
  ok "dump home $(du -h "$SCRATCH/dump.pgdump" | cut -f1)"

  c "4/8 restore local"
  docker exec "$PG_CONTAINER" psql -U "$DBUSER" -d postgres -q \
    -c "DROP DATABASE IF EXISTS $DB WITH (FORCE);" -c "CREATE DATABASE $DB OWNER $DBUSER;"
  docker cp "$SCRATCH/dump.pgdump" "$PG_CONTAINER":/tmp/d.pgdump >/dev/null
  docker exec "$PG_CONTAINER" pg_restore -U "$DBUSER" -d "$DB" -j2 --no-owner --exit-on-error /tmp/d.pgdump
  docker exec "$PG_CONTAINER" rm /tmp/d.pgdump
  ok "restored"

  c "5/8 start local (routing + daemon)"
  local U; U=$(id -u)
  launchctl bootout "gui/$U/com.spacetraders.daemon" 2>/dev/null || true
  launchctl bootout "gui/$U/com.spacetraders.routing" 2>/dev/null || true
  mv ~/Library/LaunchAgents/disabled-spacetraders/com.spacetraders.{routing,daemon}.plist \
     ~/Library/LaunchAgents/ 2>/dev/null || true
  launchctl bootstrap "gui/$U" ~/Library/LaunchAgents/com.spacetraders.routing.plist
  bind_routing "$U"
  launchctl bootstrap "gui/$U" ~/Library/LaunchAgents/com.spacetraders.daemon.plist
  sleep 12; verify_trading_local

  if [[ $KEEP_METRICS == 1 ]]; then
    c "6/8 metrics (VM → Mac)"
    docker stop spacetraders-prometheus spacetraders-grafana >/dev/null 2>&1 || true
    repoint_fw
    stream_volume 0 "$PROM_VOL" ""
    stream_volume 0 "$GRAF_VOL" "z"
    docker start spacetraders-prometheus spacetraders-grafana >/dev/null
    ok "metrics local"
  else c "6/8 metrics skipped (--no-metrics)"; fi

  c "7/8 defuse VM"
  repoint_fw
  SSH 'sudo systemctl disable spacetraders-daemon spacetraders-routing spacetraders-captain >/dev/null 2>&1
       docker update --restart no '"$PG_CONTAINER"' spacetraders-prometheus spacetraders-grafana >/dev/null' \
    || warn "defuse over SSH failed (IP churn) — stopping instance anyway"

  if [[ $STOP_VM == 1 ]]; then
    c "8/8 stop VM instance"
    GC compute instances stop "$INSTANCE" --zone="$ZONE" >/dev/null; ok "VM $(vm_status)"
  else c "8/8 VM left RUNNING (--keep-vm)"; fi
  ok "game LIVE on Mac"
}

# routing port 50051 is inside macOS' ephemeral range; Cloudflare WARP sometimes
# grabs it. Bounce WARP once if the bind doesn't take.
bind_routing(){
  local U="$1" i
  for i in $(seq 1 6); do sleep 3; lsof -nP -iTCP:50051 -sTCP:LISTEN >/dev/null 2>&1 && { ok "routing bound"; return; }; done
  warn "routing not bound — bouncing WARP"
  command -v warp-cli >/dev/null && { warp-cli disconnect; sleep 2; \
    launchctl kickstart -k "gui/$U/com.spacetraders.routing"; sleep 2; warp-cli connect; }
  for i in $(seq 1 10); do sleep 3; lsof -nP -iTCP:50051 -sTCP:LISTEN >/dev/null 2>&1 && { ok "routing bound"; return; }; done
  warn "routing STILL not bound — check routing.log"
}

active_player_sql="SELECT player_id FROM containers WHERE status='RUNNING' GROUP BY player_id ORDER BY count(*) DESC LIMIT 1"
verify_trading_local(){
  local p base n i
  p=$(docker exec "$PG_CONTAINER" psql -U "$DBUSER" -d "$DB" -tAc "$active_player_sql")
  base=$(docker exec "$PG_CONTAINER" psql -U "$DBUSER" -d "$DB" -tAc "SELECT count(*) FROM transactions WHERE player_id=$p")
  for i in $(seq 1 24); do
    n=$(docker exec "$PG_CONTAINER" psql -U "$DBUSER" -d "$DB" -tAc "SELECT count(*) FROM transactions WHERE player_id=$p")
    [[ "$n" -gt "$base" ]] && { ok "player $p trading locally ($n tx)"; return; }; sleep 5
  done; warn "player $p not yet trading — check daemon.log"
}
verify_trading_ssh(){
  SSH 'p=$(docker exec '"$PG_CONTAINER"' psql -U '"$DBUSER"' -d '"$DB"' -tAc "'"$active_player_sql"'")
    base=$(docker exec '"$PG_CONTAINER"' psql -U '"$DBUSER"' -d '"$DB"' -tAc "SELECT count(*) FROM transactions WHERE player_id=$p")
    for i in $(seq 1 24); do
      n=$(docker exec '"$PG_CONTAINER"' psql -U '"$DBUSER"' -d '"$DB"' -tAc "SELECT count(*) FROM transactions WHERE player_id=$p")
      [ "$n" -gt "$base" ] && { echo "✓ player $p trading on VM ($n tx)"; exit 0; }; sleep 5
    done; echo "! player $p not yet trading"'
}

### ─────────────────────────── main ─────────────────────────────
DIR="${1:-}"; shift || true
while [[ $# -gt 0 ]]; do case "$1" in
  --no-metrics) KEEP_METRICS=0;; --keep-vm) STOP_VM=0;; --yes|-y) ASSUME_YES=1;;
  *) die "unknown flag: $1";; esac; shift; done

case "$DIR" in to-vm|to-local) ;; *)
  die "usage: $0 <to-vm|to-local> [--no-metrics] [--keep-vm] [--yes]";; esac
command -v gcloud >/dev/null || die "gcloud not found"
[[ -d "$GOBOT" ]] || die "gobot dir not found: $GOBOT"
ensure_auth

case "$DIR" in
  to-vm)    migrate_to_vm;;
  to-local) migrate_to_local;;
esac
