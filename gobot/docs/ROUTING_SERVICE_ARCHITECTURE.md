# Routing Service Architecture

## Overview

The Routing Service is a microservice that provides advanced route optimization using Google OR-Tools. It's implemented as a separate Python gRPC service to leverage the mature OR-Tools Python library while integrating seamlessly with the Go daemon.

## Architecture Diagram

```
┌──────────────────────────────────────────────────────────────┐
│                        Go Daemon                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │          Navigation Command Handler                    │  │
│  │  - NavigateShipCommand                                 │  │
│  │  - Uses RoutingClient interface                        │  │
│  └────────────────────────┬───────────────────────────────┘  │
│                           │                                   │
│  ┌────────────────────────▼───────────────────────────────┐  │
│  │        Routing Client (Adapter)                        │  │
│  │  ┌──────────────────┐      ┌──────────────────┐       │  │
│  │  │ MockRoutingClient│  OR  │ GRPCRoutingClient│       │  │
│  │  │  (Simple POC)    │      │  (Production)    │       │  │
│  │  └──────────────────┘      └─────────┬────────┘       │  │
│  └────────────────────────────────────────┼──────────────┘  │
└─────────────────────────────────────────────┼─────────────────┘
                                             │ gRPC
                                             │ (Unix socket or TCP)
┌─────────────────────────────────────────────▼─────────────────┐
│                  Routing Service (Python)                      │
│  ┌──────────────────────────────────────────────────────────┐ │
│  │              gRPC Server                                  │ │
│  │  - Listens on 0.0.0.0:50051                              │ │
│  │  - Implements RoutingService (routing.proto)             │ │
│  └────────────────────────┬─────────────────────────────────┘ │
│                           │                                    │
│  ┌────────────────────────▼─────────────────────────────────┐ │
│  │         RoutingServiceHandler                            │ │
│  │  - PlanRoute(request) → response                         │ │
│  │  - OptimizeTour(request) → response                      │ │
│  │  - PartitionFleet(request) → response                    │ │
│  └────────────────────────┬─────────────────────────────────┘ │
│                           │                                    │
│  ┌────────────────────────▼─────────────────────────────────┐ │
│  │          ORToolsRoutingEngine                            │ │
│  │  ┌─────────────────┐  ┌─────────────────┐               │ │
│  │  │ Dijkstra        │  │ TSP Solver      │               │ │
│  │  │ Pathfinding     │  │ (OR-Tools CP)   │               │ │
│  │  │ - Fuel aware    │  │ - Distance      │               │ │
│  │  │ - Refuel stops  │  │   matrix        │               │ │
│  │  │ - Orbital hops  │  │ - Guided local  │               │ │
│  │  │                 │  │   search        │               │ │
│  │  └─────────────────┘  └─────────────────┘               │ │
│  │                                                           │ │
│  │  ┌─────────────────────────────────────────────────────┐│ │
│  │  │ VRP Solver (OR-Tools Multi-Vehicle)                 ││ │
│  │  │ - Fleet partitioning                                ││ │
│  │  │ - Load balancing                                    ││ │
│  │  │ - Distance matrix with pathfinding cache           ││ │
│  │  └─────────────────────────────────────────────────────┘│ │
│  └──────────────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────┘
```

## Component Responsibilities

### Go Daemon (Client)

**Location**: `cmd/spacetraders-daemon/main.go`, `internal/adapters/routing/`

**Responsibilities**:
1. Initialize routing client (mock or gRPC based on config)
2. Pass routing client to navigation command handlers
3. Convert domain models to routing request DTOs
4. Handle routing service connection lifecycle

**Key Files**:
- `cmd/spacetraders-daemon/main.go` - Client initialization
- `internal/adapters/routing/grpc_routing_client.go` - gRPC client adapter
- `internal/adapters/routing/mock_routing_client.go` - Mock for testing
- `internal/application/common/ports.go` - RoutingClient interface

### Routing Service (Server)

**Location**: `services/routing-service/`

**Responsibilities**:
1. Expose gRPC API implementing `routing.proto`
2. Execute OR-Tools algorithms for route optimization
3. Manage pathfinding cache for VRP performance
4. Handle errors and timeouts gracefully

**Key Files**:
- `server/main.py` - gRPC server entry point
- `handlers/routing_handler.py` - RPC method implementations
- `utils/routing_engine.py` - OR-Tools algorithm logic (ported from Python bot)

### Protocol Buffer Interface

**Location**: `pkg/proto/routing/routing.proto`

**Responsibilities**:
1. Define contract between Go client and Python server
2. Specify message formats for requests/responses
3. Define service interface (RPC methods)

## Data Flow

### Example: Navigate Ship Command

```
1. User → CLI: spacetraders navigate SHIP-1 X1-GZ7-B1

2. CLI → Daemon (gRPC): NavigateShipCommand{ship: "SHIP-1", destination: "X1-GZ7-B1"}

3. Daemon → NavigateShipHandler:
   - Load ship from repository (current location, fuel, speed)
   - Load waypoint graph from repository
   - Call routing client

4. NavigateShipHandler → RoutingClient (gRPC):
   PlanRouteRequest{
     start: "X1-GZ7-A1",
     goal: "X1-GZ7-B1",
     current_fuel: 150,
     fuel_capacity: 400,
     engine_speed: 30,
     waypoints: [{symbol: "X1-GZ7-A1", x: 0, y: 0, has_fuel: true}, ...]
   }

5. GRPCRoutingClient → Routing Service (Python):
   - Serialize request to protobuf
   - Send via gRPC channel

6. Routing Service → ORToolsRoutingEngine:
   - Build waypoint graph from request
   - Execute Dijkstra pathfinding with fuel constraints
   - Return route steps

7. Routing Service → GRPCRoutingClient:
   PlanRouteResponse{
     steps: [
       {action: TRAVEL, waypoint: "X1-GZ7-B1", fuel_cost: 120, time: 450, distance: 120.5}
     ],
     total_fuel_cost: 120,
     total_time_seconds: 450,
     total_distance: 120.5,
     success: true
   }

8. NavigateShipHandler → Ship:
   - Execute each route step (travel, refuel)
   - Update ship state via API
   - Persist to repository

9. Handler → Daemon → CLI: NavigateShipResponse{success: true, arrival_time: ...}
```

## Algorithm Details

### 1. Dijkstra Pathfinding (PlanRoute)

**Input**: Start waypoint, goal waypoint, fuel constraints, waypoint graph

**Algorithm**:
```python
priority_queue = [(0, start, current_fuel, [])]  # (time, waypoint, fuel, path)
visited = {}

while priority_queue:
    time, current, fuel, path = heappop(priority_queue)

    if current == goal:
        return path

    if (current, fuel) in visited:
        continue

    visited[(current, fuel)] = time

    # Option 1: Refuel (if at fuel station and below capacity)
    if has_fuel(current) and fuel < fuel_capacity:
        heappush(priority_queue, (time, current, fuel_capacity, path + [REFUEL]))

    # Option 2: Travel to neighbors
    for neighbor in neighbors(current):
        distance = euclidean_distance(current, neighbor)
        fuel_cost = calculate_fuel_cost(distance, select_flight_mode(fuel))

        if fuel >= fuel_cost:
            new_time = time + calculate_travel_time(distance, mode, engine_speed)
            heappush(priority_queue, (
                new_time,
                neighbor,
                fuel - fuel_cost,
                path + [TRAVEL(neighbor, fuel_cost, time, distance)]
            ))

return None  # No path found
```

**Key Features**:
- **Fuel discretization**: Groups fuel levels into buckets (÷10) to reduce state space
- **90% rule**: Forces refuel at start when below 90% capacity
- **Safety margin**: Maintains 4-unit fuel buffer (except final hop to goal)
- **Mode selection**: BURN (fastest) > CRUISE (standard) > DRIFT (never used unless emergency)
- **Orbital hops**: Zero distance, 1 second, zero fuel for orbital transfers

### 2. TSP Optimization (OptimizeTour)

**Input**: Start waypoint, target waypoints, fuel capacity, engine speed

**Algorithm** (OR-Tools Constraint Programming):
```python
# Build distance matrix
n = len(waypoints)
distance_matrix = [[0] * n for _ in range(n)]

for i in range(n):
    for j in range(n):
        distance_matrix[i][j] = int(euclidean_distance(waypoints[i], waypoints[j]) * 100)

# Create routing model
manager = RoutingIndexManager(n, 1, 0)  # n nodes, 1 vehicle, depot at 0
routing = RoutingModel(manager)

# Define distance callback
def distance_callback(from_index, to_index):
    return distance_matrix[from_index][to_index]

routing.RegisterTransitCallback(distance_callback)
routing.SetArcCostEvaluatorOfAllVehicles(...)

# Add constraints (must visit all waypoints)
for node in range(1, n):
    routing.AddDisjunction([node], 999999)  # High penalty for dropping

# Solve with guided local search
search_parameters = DefaultRoutingSearchParameters()
search_parameters.first_solution_strategy = PATH_CHEAPEST_ARC
search_parameters.local_search_metaheuristic = GUIDED_LOCAL_SEARCH
search_parameters.time_limit.seconds = 5

solution = routing.SolveWithParameters(search_parameters)

# Extract tour
tour = []
index = routing.Start(0)
while not routing.IsEnd(index):
    node = manager.IndexToNode(index)
    tour.append(waypoints[node])
    index = solution.Value(routing.NextVar(index))

return tour
```

**Key Features**:
- **Distance scaling**: Multiply by 100 for integer precision (OR-Tools requirement)
- **Guided local search**: Metaheuristic for escaping local optima
- **Timeout**: 5-second default (configurable)
- **Return to start**: Optional (tours vs. paths)

### 3. VRP Fleet Partitioning (PartitionFleet)

**Input**: Ships with locations, markets to visit, fuel/speed constraints

**Algorithm** (OR-Tools Multi-Vehicle VRP):
```python
# Build node list (markets + ship starting locations)
nodes = list(markets)
starts = []
ends = []

for ship in ships:
    if ship.location not in nodes:
        nodes.append(ship.location)
    starts.append(nodes.index(ship.location))
    ends.append(nodes.index(ship.location))

# Build distance matrix with pathfinding cache
distance_matrix = [[0] * len(nodes) for _ in range(len(nodes))]

for i, origin in enumerate(nodes):
    for j, target in enumerate(nodes):
        # Use cached pathfinding result if available
        route = find_optimal_path(origin, target, fuel_capacity, engine_speed)
        distance_matrix[i][j] = route['total_time'] if route else 1_000_000

# Calculate disjunction penalty (prevent market dropping)
max_cost = max(max(row) for row in distance_matrix)
disjunction_penalty = max(max_cost * 10, 10_000_000)

# Create VRP model
manager = RoutingIndexManager(len(nodes), len(ships), starts, ends)
routing = RoutingModel(manager)

# Distance callback
def distance_callback(from_index, to_index):
    return distance_matrix[from_index][to_index]

routing.RegisterTransitCallback(distance_callback)

# Add time dimension with load balancing
routing.AddDimension(transit_callback_index, 0, disjunction_penalty, True, "TravelTime")
time_dimension = routing.GetDimensionOrDie("TravelTime")
time_dimension.SetGlobalSpanCostCoefficient(100)  # Penalty for imbalance

# Add disjunction for markets (make them mandatory with high penalty)
for market in markets:
    routing.AddDisjunction([market_index], disjunction_penalty)

# Solve
solution = routing.SolveWithParameters(search_parameters)

# Extract assignments
assignments = {ship: [] for ship in ships}

for vehicle, ship in enumerate(ships):
    index = routing.Start(vehicle)

    # Handle ship-at-market edge case
    start_waypoint = nodes[manager.IndexToNode(index)]
    if start_waypoint in markets:
        assignments[ship].append(start_waypoint)

    # Extract route
    while not routing.IsEnd(index):
        node = manager.IndexToNode(index)
        waypoint = nodes[node]

        if waypoint in markets and waypoint not in assignments[ship]:
            assignments[ship].append(waypoint)

        index = solution.Value(routing.NextVar(index))

return assignments
```

**Key Features**:
- **Pathfinding cache**: O(N²) distance matrix build uses cached results
- **Disjunction penalty**: 10x max cost prevents market dropping
- **Load balancing**: GlobalSpanCostCoefficient=100 (moderate balance pressure)
- **Depot handling**: Ships starting at markets are assigned immediately
- **Timeout**: 30-second default (configurable)

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ROUTING_HOST` | `0.0.0.0` | Host for routing service to bind |
| `ROUTING_PORT` | `50051` | Port for routing service |
| `TSP_TIMEOUT` | `5` | TSP solver timeout (seconds) |
| `VRP_TIMEOUT` | `30` | VRP solver timeout (seconds) |
| `ROUTING_SERVICE_ADDR` | _(empty)_ | Daemon: routing service address (e.g., `localhost:50051`) |

### Daemon Configuration

The daemon automatically chooses routing client based on `ROUTING_SERVICE_ADDR`:

```go
var routingClient common.RoutingClient
routingAddr := os.Getenv("ROUTING_SERVICE_ADDR")

if routingAddr != "" {
    // Production: use real gRPC client
    grpcClient, err := routing.NewGRPCRoutingClient(routingAddr)
    routingClient = grpcClient
} else {
    // Development: use mock client
    routingClient = routing.NewMockRoutingClient()
}
```

## Deployment

### Development

```bash
# Terminal 1: Start routing service
cd services/routing-service
bash run.sh

# Terminal 2: Start daemon with routing service
export ROUTING_SERVICE_ADDR="localhost:50051"
go run cmd/spacetraders-daemon/main.go
```

### Production

```bash
# Option 1: Systemd service
sudo systemctl start routing-service
sudo systemctl start spacetraders-daemon

# Option 2: Docker Compose
docker-compose up -d
```

## Performance Considerations

### Pathfinding Cache

VRP distance matrix construction requires O(N²) pathfinding calls. The routing engine uses an internal cache to avoid redundant computations:

```python
cache_key = (origin, target, fuel_capacity, engine_speed)
if cache_key in self._pathfinding_cache:
    return self._pathfinding_cache[cache_key]  # Cache hit
else:
    route = find_optimal_path(...)
    self._pathfinding_cache[cache_key] = route  # Cache miss
    return route
```

**Impact**: For 50-node VRP, cache reduces 2,500 calls to ~1,250 calls (50% reduction).

### Timeout Tuning

| Use Case | Recommended TSP Timeout | Recommended VRP Timeout |
|----------|-------------------------|-------------------------|
| Interactive CLI | 2-3s | 10-15s |
| Batch processing | 10-15s | 60-120s |
| Real-time scouting | 1-2s | 5-10s |

### Horizontal Scaling

The routing service is stateless and can be scaled horizontally:

```yaml
# docker-compose.yml
services:
  routing-service-1:
    build: ./services/routing-service
    ports:
      - "50051:50051"

  routing-service-2:
    build: ./services/routing-service
    ports:
      - "50052:50051"
```

Use a load balancer (e.g., nginx, envoy) to distribute requests.

## Error Handling

### Routing Failures

```protobuf
message PlanRouteResponse {
  bool success = 5;
  optional string error_message = 6;
}
```

**Common Errors**:
- `"No path found"` - Graph disconnected or insufficient fuel
- `"Failed to optimize tour"` - TSP timeout or infeasible constraints
- `"Failed to partition fleet"` - VRP timeout or disconnected markets

**Client Handling**:
```go
resp, err := routingClient.PlanRoute(ctx, req)
if err != nil {
    return fmt.Errorf("routing service error: %w", err)
}

if !resp.Success {
    return fmt.Errorf("routing failed: %s", resp.ErrorMessage)
}
```

### Connection Failures

The gRPC client uses a 5-second connection timeout:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

conn, err := grpc.DialContext(ctx, address, grpc.WithBlock())
```

**Fallback Strategy**: If routing service is unavailable, daemon can fall back to mock client or retry with exponential backoff.

## Testing Strategy

### Unit Tests (Python)

Test individual algorithms in isolation:

```python
def test_dijkstra_pathfinding():
    engine = ORToolsRoutingEngine()
    graph = build_test_graph()

    result = engine.find_optimal_path(
        graph=graph,
        start="A",
        goal="C",
        current_fuel=100,
        fuel_capacity=200,
        engine_speed=30
    )

    assert result is not None
    assert result['total_fuel_cost'] <= 200
    assert result['steps'][-1]['waypoint'] == "C"
```

### Integration Tests (Go)

Test gRPC communication:

```go
func TestGRPCRoutingClient(t *testing.T) {
    client, err := routing.NewGRPCRoutingClient("localhost:50051")
    require.NoError(t, err)
    defer client.Close()

    req := &common.RouteRequest{
        StartWaypoint: "A",
        GoalWaypoint:  "C",
        // ...
    }

    resp, err := client.PlanRoute(context.Background(), req)
    require.NoError(t, err)
    require.NotNil(t, resp)
    require.Greater(t, len(resp.Steps), 0)
}
```

### End-to-End Tests

Test full navigation flow with real routing service:

```bash
# Start routing service
bash services/routing-service/run.sh &

# Start daemon with routing service
export ROUTING_SERVICE_ADDR="localhost:50051"
go run cmd/spacetraders-daemon/main.go &

# Execute navigation command
go run cmd/spacetraders/main.go navigate SHIP-1 X1-GZ7-B1
```

## Future Enhancements

### 1. WebSocket Streaming

For long-running VRP operations, stream progress updates:

```protobuf
service RoutingService {
  rpc PartitionFleetStream(PartitionFleetRequest) returns (stream PartitionFleetProgress);
}
```

### 2. Multi-System Routing

Support cross-system routes (with jump gates):

```protobuf
message PlanRouteRequest {
  repeated string systems = 8;  // Allow multi-system paths
  repeated JumpGate jump_gates = 9;
}
```

### 3. Dynamic Re-routing

Re-optimize tours when new markets appear:

```protobuf
service RoutingService {
  rpc UpdateTour(UpdateTourRequest) returns (OptimizeTourResponse);
}
```

### 4. Persistent Cache

Persist pathfinding cache to Redis for cross-request optimization:

```python
cache_key = f"{origin}:{target}:{fuel_capacity}:{engine_speed}"
cached = redis.get(cache_key)
if cached:
    return json.loads(cached)
```

## References

- [Python Bot OR-Tools Implementation](../../../bot/src/adapters/secondary/routing/ortools_engine.py)
- [routing.proto Interface](../../pkg/proto/routing/routing.proto)
- [Go Daemon Integration](../../cmd/spacetraders-daemon/main.go)
- [OR-Tools Routing Guide](https://developers.google.com/optimization/routing)
