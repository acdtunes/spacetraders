#!/bin/bash

# Generate Python protobuf files from routing.proto
# This script must be run from the gobot root directory

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"

echo "Generating Python protobuf files..."
echo "Project root: $PROJECT_ROOT"

# Create output directory for generated files
mkdir -p "$SCRIPT_DIR/generated"

# Generate Python code
python3 -m grpc_tools.protoc \
    -I"$PROJECT_ROOT/pkg/proto" \
    --python_out="$SCRIPT_DIR/generated" \
    --grpc_python_out="$SCRIPT_DIR/generated" \
    "$PROJECT_ROOT/pkg/proto/routing/routing.proto"

echo "Python protobuf files generated in $SCRIPT_DIR/generated"

# Update imports in generated files to use relative imports
# This makes the generated files work properly with Python's module system
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    sed -i '' 's/^import routing_pb2/from . import routing_pb2/' "$SCRIPT_DIR/generated/routing_pb2_grpc.py"
else
    # Linux
    sed -i 's/^import routing_pb2/from . import routing_pb2/' "$SCRIPT_DIR/generated/routing_pb2_grpc.py"
fi

echo "Fixed imports in generated files"
