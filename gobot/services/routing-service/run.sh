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

echo "Starting Routing Service..."
echo "Host: $HOST"
echo "Port: $PORT"
echo "TSP Timeout: ${TSP_TIMEOUT}s"
echo "VRP Timeout: ${VRP_TIMEOUT}s"

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

# Generate protobuf files if they don't exist
if [ ! -f "$SCRIPT_DIR/generated/routing_pb2_grpc.py" ]; then
    echo "Generating protobuf files..."
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
