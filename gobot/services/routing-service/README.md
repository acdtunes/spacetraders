# Routing Service - OR-Tools gRPC Microservice

A standalone Python gRPC service that provides advanced route optimization using Google OR-Tools. This service implements sophisticated algorithms for pathfinding, tour optimization, and fleet partitioning.

## Overview

The Routing Service is a microservice architecture component that separates complex OR-Tools routing logic from the main Go daemon. It provides three core capabilities:

1. **PlanRoute** - Dijkstra-based pathfinding with fuel constraints
2. **OptimizeTour** - TSP (Traveling Salesman Problem) optimization for multi-waypoint tours
3. **PartitionFleet** - VRP (Vehicle Routing Problem) for distributing waypoints across multiple ships

## Architecture

```
┌─────────────────┐         gRPC          ┌──────────────────────┐
│   Go Daemon     │◄────────────────────►│  Routing Service     │
│                 │     (routing.proto)   │  (Python + OR-Tools) │
└─────────────────┘                       └──────────────────────┘
         │                                          │
         │                                          │
    Uses routing                              Uses OR-Tools
    for navigation                            - Dijkstra pathfinding
                                             - TSP solver
                                             - VRP solver
```

### Key Design Decisions

1. **Separate Process**: Python service runs as independent process, communicating via gRPC
2. **OR-Tools Expertise**: Keeps complex OR-Tools logic in Python where it's mature and well-tested
3. **Stateless**: Service is stateless - all context passed in requests
4. **Caching**: Internal pathfinding cache for VRP optimization (reduces redundant computations)

## Algorithms

### 1. Dijkstra Pathfinding (PlanRoute)

**Algorithm**: Modified Dijkstra's algorithm with fuel constraints

**Features**:
- Fuel-aware pathfinding with automatic refuel stop insertion
- Supports orbital hops (instant, zero-fuel transfers)
- 90% fuel rule: forces refuel at start when below 90% capacity
- Flight mode selection: BURN (fastest) > CRUISE (standard) > DRIFT (emergency)
- Safety margin: maintains 4-unit fuel buffer (except final hop)

**Complexity**: O(E log V) where E = edges, V = vertices

**Example**:
```
Input:  Start=A, Goal=C, Fuel=50/200, Graph={A→B→C}
Output: [REFUEL at A, TRAVEL to B, TRAVEL to C]
        Total fuel: 120, Time: 450s
```

### 2. TSP Optimization (OptimizeTour)

**Algorithm**: OR-Tools Constraint Programming with Guided Local Search

**Features**:
- Finds optimal visit order for multiple waypoints
- Configurable return-to-start (tours vs. paths)
- Distance matrix optimization (100x precision scaling)
- Timeout-based solver (default: 5 seconds)

**Complexity**: NP-hard (TSP), approximated by heuristics

**Example**:
```
Input:  Start=BASE, Waypoints=[M1, M2, M3], ReturnToStart=true
Output: Visit order: BASE → M2 → M1 → M3 → BASE
        Total distance: 450.2 units, Time: 1200s
```

### 3. VRP Fleet Partitioning (PartitionFleet)

**Algorithm**: Multi-vehicle VRP with disjunction penalties

**Features**:
- Distributes markets across multiple ships
- Balanced load distribution (GlobalSpanCostCoefficient=100)
- Handles ship-at-market edge case (depot node assignment)
- Disjunction penalty: 10x max distance (prevents market dropping)
- Pathfinding cache for distance matrix efficiency

**Complexity**: NP-hard (VRP), approximated by OR-Tools heuristics

**Example**:
```
Input:  Ships=[S1, S2], Markets=[M1, M2, M3, M4]
Output: S1=[M1, M3], S2=[M2, M4]
        Ships utilized: 2, Total assigned: 4
```

## Installation

### Prerequisites

- Python 3.8+
- pip
- protoc (for regenerating protobuf files)

### Setup

```bash
# Navigate to routing service directory
cd services/routing-service

# Create virtual environment
python3 -m venv venv
source venv/bin/activate

# Install dependencies
pip install -r requirements.txt

# Generate protobuf files
bash generate_protos.sh
```

## Running the Service

### Option 1: Direct Python Execution

```bash
# Activate virtual environment
source venv/bin/activate

# Run server
python3 server/main.py --host 0.0.0.0 --port 50051 --tsp-timeout 5 --vrp-timeout 30
```

### Option 2: Using Run Script

```bash
bash run.sh
```

### Option 3: Via Go Binary (Recommended)

```bash
# Build Go binary
go build -o bin/routing-service ./cmd/routing-service

# Run (automatically manages Python service)
./bin/routing-service
```

### Environment Variables

- `ROUTING_HOST` - Host to bind to (default: `0.0.0.0`)
- `ROUTING_PORT` - Port to bind to (default: `50051`)
- `TSP_TIMEOUT` - TSP solver timeout in seconds (default: `5`)
- `VRP_TIMEOUT` - VRP solver timeout in seconds (default: `30`)

## Integration with Go Daemon

The daemon automatically connects to the routing service if `ROUTING_SERVICE_ADDR` is set:

```bash
# Start routing service
export ROUTING_PORT=50051
./bin/routing-service

# In another terminal, start daemon with routing service
export ROUTING_SERVICE_ADDR="localhost:50051"
./bin/spacetraders-daemon
```

Without `ROUTING_SERVICE_ADDR`, the daemon falls back to a simple mock routing client.

## Testing

### Unit Tests

```bash
# Activate venv
source venv/bin/activate

# Run Python tests
python3 test_service.py
```

This tests all three RPC methods with synthetic data.

### Integration Tests

```bash
# Start routing service
bash run.sh

# In another terminal, run Go daemon tests
cd ../..
go test ./internal/adapters/routing/... -v
```

## gRPC API

### PlanRoute

Finds optimal fuel-constrained path between two waypoints.

**Request**:
```protobuf
message PlanRouteRequest {
  string system_symbol = 1;
  string start_waypoint = 2;
  string goal_waypoint = 3;
  int32 current_fuel = 4;
  int32 fuel_capacity = 5;
  int32 engine_speed = 6;
  repeated Waypoint waypoints = 7;
}
```

**Response**:
```protobuf
message PlanRouteResponse {
  repeated RouteStep steps = 1;
  int32 total_fuel_cost = 2;
  int32 total_time_seconds = 3;
  double total_distance = 4;
  bool success = 5;
  optional string error_message = 6;
}
```

### OptimizeTour

Optimizes visit order for multiple waypoints using TSP.

**Request**:
```protobuf
message OptimizeTourRequest {
  string system_symbol = 1;
  string start_waypoint = 2;
  repeated string target_waypoints = 3;
  int32 fuel_capacity = 4;
  int32 engine_speed = 5;
  repeated Waypoint all_waypoints = 6;
  bool return_to_start = 7;
}
```

**Response**:
```protobuf
message OptimizeTourResponse {
  repeated string visit_order = 1;
  repeated RouteStep route_steps = 2;
  int32 total_time_seconds = 3;
  double total_distance = 4;
  bool success = 5;
  optional string error_message = 6;
}
```

### PartitionFleet

Distributes markets across multiple ships using VRP.

**Request**:
```protobuf
message PartitionFleetRequest {
  string system_symbol = 1;
  repeated string ship_symbols = 2;
  repeated string market_waypoints = 3;
  map<string, ShipConfig> ship_configs = 4;
  repeated Waypoint all_waypoints = 5;
  int32 iterations = 6;
}
```

**Response**:
```protobuf
message PartitionFleetResponse {
  map<string, ShipTour> assignments = 1;
  bool success = 2;
  optional string error_message = 3;
  int32 total_waypoints_assigned = 4;
  int32 ships_utilized = 5;
}
```

## Performance Characteristics

### PlanRoute
- **Typical**: <10ms for 50-node graphs
- **Large**: ~100ms for 200-node graphs
- **Worst Case**: 1s for complex fuel constraints

### OptimizeTour
- **Small** (5 waypoints): <100ms
- **Medium** (10 waypoints): ~500ms
- **Large** (20 waypoints): ~5s (timeout)

### PartitionFleet
- **Small** (2 ships, 5 markets): ~200ms
- **Medium** (5 ships, 20 markets): ~2s
- **Large** (10 ships, 50 markets): ~30s (timeout)

**Note**: VRP performance depends heavily on distance matrix size (O(N²) pathfinding calls with caching).

## Troubleshooting

### Service Won't Start

**Issue**: `failed to bind to port`
- **Solution**: Port already in use. Change `ROUTING_PORT` or kill existing process.

**Issue**: `ModuleNotFoundError: No module named 'ortools'`
- **Solution**: Virtual environment not activated or dependencies not installed.
```bash
source venv/bin/activate
pip install -r requirements.txt
```

### gRPC Connection Errors

**Issue**: Daemon can't connect to routing service
- **Solution**: Check service is running and `ROUTING_SERVICE_ADDR` is correct.
```bash
# Test connection
grpcurl -plaintext localhost:50051 list
```

### Routing Failures

**Issue**: `No path found`
- **Cause**: Graph disconnected or insufficient fuel
- **Solution**: Verify waypoint graph has fuel stations and is fully connected.

**Issue**: `VRP dropped markets`
- **Cause**: Disjunction penalty too low (markets unreachable)
- **Solution**: Increase VRP timeout or check fuel/distance constraints.

## Development

### Project Structure

```
routing-service/
├── server/
│   └── main.py              # gRPC server entry point
├── handlers/
│   └── routing_handler.py   # RPC method implementations
├── utils/
│   └── routing_engine.py    # OR-Tools algorithms (ported from Python bot)
├── generated/               # Generated protobuf files
│   ├── routing_pb2.py
│   └── routing_pb2_grpc.py
├── requirements.txt         # Python dependencies
├── generate_protos.sh       # Protobuf generation script
├── run.sh                   # Service startup script
├── test_service.py          # Integration tests
└── README.md               # This file
```

### Adding New RPC Methods

1. Update `pkg/proto/routing/routing.proto`
2. Regenerate Go protobuf: `make proto`
3. Regenerate Python protobuf: `bash generate_protos.sh`
4. Implement handler in `handlers/routing_handler.py`
5. Add algorithm logic to `utils/routing_engine.py` if needed

### Debugging

Enable debug logging:

```python
# In server/main.py
logging.basicConfig(level=logging.DEBUG)
```

Add verbose OR-Tools output:

```python
# In utils/routing_engine.py
search_parameters.log_search = True
```

## References

- [Google OR-Tools Documentation](https://developers.google.com/optimization)
- [OR-Tools Routing](https://developers.google.com/optimization/routing)
- [gRPC Python](https://grpc.io/docs/languages/python/)
- [Protocol Buffers](https://developers.google.com/protocol-buffers)

## License

Same as parent project (MIT).
