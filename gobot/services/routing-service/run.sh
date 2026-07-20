#!/bin/bash

# Run the routing service
# This script must be run from the gobot root directory

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Default values
HOST="${ROUTING_HOST:-0.0.0.0}"
PORT="${ROUTING_PORT:-50051}"
TSP_TIMEOUT="${TSP_TIMEOUT:-5}"
VRP_TIMEOUT="${VRP_TIMEOUT:-30}"
# sp-1wp8: tour selection objective. The LAUNCH default is "rate" ($/hour-primary)
# per the offline replay verdict (+29-32% projected fleet-$/hr over profit-primary
# on 48h of reconstructed real snapshots; replay_objective.py). The solver's own
# in-code default stays "profit" (fail-safe), so an operator reverts by exporting
# TOUR_SOLVER_OBJECTIVE=profit — no code change, next restart applies it.
export TOUR_SOLVER_OBJECTIVE="${TOUR_SOLVER_OBJECTIVE:-rate}"
# ARMED (Admiral 2026-07-17): long-tour rate arm for cap-4. For a long tour (cap>2) this
# flag is the SOLE objective governor (sp-ljh5). RUNTIME OVERRIDE — leave uncommitted;
# disarm with `git checkout -- run.sh` (or export TOUR_SOLVER_RATE_ARMED_LONG=0) + restart.
export TOUR_SOLVER_RATE_ARMED_LONG="${TOUR_SOLVER_RATE_ARMED_LONG:-1}"
# ARMED (Admiral 2026-07-17): OR-Tools prize-collecting sequencer (sp-y05b). The $/hr-critical
# market-sequencing decision (which markets to arb + in what order) had been running on the
# greedy BEAM heuristic — sp-y05b was built+merged but never armed. Replay-measured +12.8%
# rate-$/hr vs beam (429,138 vs 380,464 cr/hr over the same 310 windows). Pure-add (solve_tour
# unions ortools+beam candidates, stage-2 arbiter picks best -> can only match-or-beat beam),
# and ZERO extra API (planning-only on cached market data). RUNTIME OVERRIDE — leave uncommitted;
# disarm with `git checkout -- run.sh` (or export TOUR_SOLVER_SEQUENCER=beam) + restart routing.
export TOUR_SOLVER_SEQUENCER="${TOUR_SOLVER_SEQUENCER:-ortools}"
# ARMED LIVE (Admiral 2026-07-17 "do this live, out of time, risk accepted"): sp-acb8 Tune 1,
# ordered-path #2. Raises MAX_PLANNED_TRANCHES 2->3 — the throughput knob (throughput = 76% of
# the $/hr spread). Lets a hull load one more decayed tranche per market/good (+10-15%/market),
# stacking on the widening's newly-opened sinks toward 15M. NOT replay-gated (Admiral override);
# impact model still decays each tranche (buy x1.1/sell x0.9) so it self-limits on crush. RUNTIME
# OVERRIDE — leave uncommitted; disarm with `export TOUR_SOLVER_MAX_PLANNED_TRANCHES=2` (or git
# checkout run.sh) + deploy-routing. Watch per-good realized sell price for concentration erosion.
export TOUR_SOLVER_MAX_PLANNED_TRANCHES="${TOUR_SOLVER_MAX_PLANNED_TRANCHES:-3}"
# ARMED LIVE (Admiral 2026-07-17 "build everything", toward 15M): sp-7q5t/sp-fguo WIDENING UNLOCK.
# candidate_hop_depth=2 (daemon) widened the candidate set, but the distant rich sinks were being
# CUT at the stage-2 full-scoring shortlist (FULL_SCORE_TOP_N=20) before they could compete —
# systems/tour stuck at ~1.4 vs the >3 bar. Raise the shortlist 20->35 (analyst rec) so the widened
# candidates survive to scoring. FULL_SCORE_TOP_N affects BOTH sequencer paths (lives in solve_tour).
# clamp [10,100]; disarm with `export TOUR_SOLVER_FULL_SCORE_TOP_N=20` (or git checkout run.sh) + deploy-routing.
export TOUR_SOLVER_FULL_SCORE_TOP_N="${TOUR_SOLVER_FULL_SCORE_TOP_N:-35}"
# ARMED LIVE (same): OR-Tools per-model node cap 80->160. Admits ~2x pruned candidate nodes per subset
# model so a distant rich cluster survives pruning to compete in-model. ONLY bites under
# TOUR_SOLVER_SEQUENCER=ortools (armed); inert under beam. 160 stays well under the [40,400] ceiling and
# the 2-5s anytime wall (ORTOOLS_TIME_BUDGET_SECONDS) that bounds per-model solve cost, so p99 tour-solve
# latency is protected — a larger jump waits on a latency sweep. Disarm: `export TOUR_SOLVER_ORTOOLS_MAX_NODES=80`.
export TOUR_SOLVER_ORTOOLS_MAX_NODES="${TOUR_SOLVER_ORTOOLS_MAX_NODES:-160}"
export TOUR_SOLVER_INTER_SYSTEM_TRAVEL_SECONDS="${TOUR_SOLVER_INTER_SYSTEM_TRAVEL_SECONDS:-1200}"  # sp-kab1c

echo "Starting Routing Service..."
echo "Host: $HOST"
echo "Port: $PORT"
echo "TSP Timeout: ${TSP_TIMEOUT}s"
echo "VRP Timeout: ${VRP_TIMEOUT}s"
echo "Tour objective: ${TOUR_SOLVER_OBJECTIVE}"

# Check if virtual environment exists
if [ ! -d "$SCRIPT_DIR/venv" ]; then
    echo "Virtual environment not found. Creating..."
    # Use Python 3.12 for ortools compatibility
    python3.12 -m venv "$SCRIPT_DIR/venv"
    echo "Installing dependencies..."
    "$SCRIPT_DIR/venv/bin/pip" install -r "$SCRIPT_DIR/requirements.txt"
fi

# Activate virtual environment and run server
source "$SCRIPT_DIR/venv/bin/activate"

# Generate protobuf files when MISSING or STALE.
#
# sp-qzej: the old guard only checked ABSENCE (`[ ! -f routing_pb2_grpc.py ]`), so a
# redeploy that reused a pre-existing generated/ from before a proto change kept a STALE
# descriptor. When C1 (sp-64je) added `stock_sources` to routing.proto, a redeploy that
# did not wipe generated/ left the runtime proto without that field; the updated handler
# then hit `AttributeError('stock_sources')` on every OptimizeTradeTour, which the servicer
# returned as `internal_error: stock_sources` and the daemon masked as an empty "tour
# unavailable" — aborting all tours for hours. Regenerating when routing.proto is NEWER
# than the generated files keeps the runtime descriptor in lockstep with the schema.
PROTO_SRC="$SCRIPT_DIR/../../pkg/proto/routing/routing.proto"
GEN_PB2="$SCRIPT_DIR/generated/routing_pb2.py"
GEN_GRPC="$SCRIPT_DIR/generated/routing_pb2_grpc.py"
if [ ! -f "$GEN_GRPC" ] || [ ! -f "$GEN_PB2" ] || [ "$PROTO_SRC" -nt "$GEN_PB2" ] || [ "$PROTO_SRC" -nt "$GEN_GRPC" ]; then
    echo "Generating protobuf files (missing or stale vs routing.proto)..."
    # Use venv's python to generate protos
    mkdir -p "$SCRIPT_DIR/generated"
    "$SCRIPT_DIR/venv/bin/python3" -m grpc_tools.protoc \
        -I"$SCRIPT_DIR/../../pkg/proto" \
        --python_out="$SCRIPT_DIR/generated" \
        --grpc_python_out="$SCRIPT_DIR/generated" \
        "$SCRIPT_DIR/../../pkg/proto/routing/routing.proto"

    # Move files from routing subdirectory to generated directory
    if [ -d "$SCRIPT_DIR/generated/routing" ]; then
        mv "$SCRIPT_DIR/generated/routing"/*.py "$SCRIPT_DIR/generated/"
        rmdir "$SCRIPT_DIR/generated/routing"
    fi

    # Create __init__.py to make generated a Python package
    touch "$SCRIPT_DIR/generated/__init__.py"

    # Fix imports in generated files
    if [ -f "$SCRIPT_DIR/generated/routing_pb2_grpc.py" ]; then
        if [[ "$OSTYPE" == "darwin"* ]]; then
            sed -i '' 's/^from routing import routing_pb2/from . import routing_pb2/' "$SCRIPT_DIR/generated/routing_pb2_grpc.py"
        else
            sed -i 's/^from routing import routing_pb2/from . import routing_pb2/' "$SCRIPT_DIR/generated/routing_pb2_grpc.py"
        fi
        echo "Protobuf files generated successfully"
    else
        echo "Error: Failed to generate protobuf files"
        exit 1
    fi
fi

# Run the server using venv python (cd to routing-service directory so Python can find 'generated' package)
cd "$SCRIPT_DIR"
"$SCRIPT_DIR/venv/bin/python3" server/main.py \
    --host "$HOST" \
    --port "$PORT" \
    --tsp-timeout "$TSP_TIMEOUT" \
    --vrp-timeout "$VRP_TIMEOUT"
