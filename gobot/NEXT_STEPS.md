# Next Steps: Complete gRPC Integration

## Quick Start (1-2 hours)

### Step 1: Install Protobuf Compiler

```bash
# macOS
brew install protobuf

# Verify installation
protoc --version  # Should show libprotoc 3.x or higher
```

### Step 2: Install Go Protobuf Plugins

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Verify installation
which protoc-gen-go
which protoc-gen-go-grpc
```

### Step 3: Add gRPC Dependencies

```bash
go get google.golang.org/grpc@latest
go get google.golang.org/protobuf@latest
go mod tidy
```

### Step 4: Generate Protobuf Code

```bash
make proto
```

This will generate:
- `pkg/proto/daemon/daemon.pb.go` - Message types
- `pkg/proto/daemon/daemon_grpc.pb.go` - gRPC service interfaces
- `pkg/proto/routing/routing.pb.go` - Message types
- `pkg/proto/routing/routing_grpc.pb.go` - gRPC service interfaces

### Step 5: Wire gRPC Server

Update `internal/adapters/grpc/daemon_server.go`:

```go
import (
    pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
    "google.golang.org/grpc"
)

// Make DaemonServer implement pb.DaemonServiceServer
type DaemonServer struct {
    pb.UnimplementedDaemonServiceServer  // Add this
    // ... rest of fields
}

// Implement gRPC methods
func (s *DaemonServer) NavigateShip(ctx context.Context, req *pb.NavigateShipRequest) (*pb.NavigateShipResponse, error) {
    containerID, err := s.navigateShip(ctx, req.ShipSymbol, req.Destination, int(req.PlayerId))
    if err != nil {
        return nil, err
    }

    return &pb.NavigateShipResponse{
        ContainerId:   containerID,
        ShipSymbol:    req.ShipSymbol,
        Destination:   req.Destination,
        Status:        "PENDING",
        EstimatedTimeSeconds: 120,
    }, nil
}

// Update Start() method
func (s *DaemonServer) Start() error {
    grpcServer := grpc.NewServer()
    pb.RegisterDaemonServiceServer(grpcServer, s)

    return grpcServer.Serve(s.listener)
}
```

### Step 6: Wire gRPC Client

Update `internal/adapters/cli/daemon_client.go`:

```go
import (
    pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

type DaemonClient struct {
    conn   *grpc.ClientConn
    client pb.DaemonServiceClient
}

func NewDaemonClient(socketPath string) (*DaemonClient, error) {
    conn, err := grpc.Dial(
        "unix://"+socketPath,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        return nil, err
    }

    return &DaemonClient{
        conn:   conn,
        client: pb.NewDaemonServiceClient(conn),
    }, nil
}

func (c *DaemonClient) NavigateShip(ctx context.Context, shipSymbol, destination string, playerID int, agentSymbol string) (*NavigateResponse, error) {
    req := &pb.NavigateShipRequest{
        ShipSymbol:  shipSymbol,
        Destination: destination,
        PlayerId:    int32(playerID),
    }

    resp, err := c.client.NavigateShip(ctx, req)
    if err != nil {
        return nil, err
    }

    return &NavigateResponse{
        ContainerID:   resp.ContainerId,
        ShipSymbol:    resp.ShipSymbol,
        Destination:   resp.Destination,
        Status:        resp.Status,
        EstimatedTime: resp.EstimatedTimeSeconds,
    }, nil
}
```

### Step 7: Test End-to-End

```bash
# Terminal 1: Start daemon
./bin/spacetraders-daemon

# Terminal 2: Check health
./bin/spacetraders health
# Expected: ✓ Daemon is healthy

# Terminal 3: Navigate ship
./bin/spacetraders navigate --ship AGENT-1 --destination X1-GZ7-B1 --player-id 1
# Expected: ✓ Navigation started successfully
```

## Medium-Term Tasks (4-8 hours)

### 1. Implement OR-Tools Python Service

Create `ortools-service/routing_server.py`:

```python
import grpc
from concurrent import futures
import routing_pb2
import routing_pb2_grpc
from ortools.constraint_solver import routing_enums_pb2
from ortools.constraint_solver import pywrapcp

class RoutingServicer(routing_pb2_grpc.RoutingServiceServicer):
    def PlanRoute(self, request, context):
        # Implement Dijkstra with fuel constraints
        # Use existing Python bot logic from:
        # bot/src/adapters/secondary/routing/ortools_engine.py
        pass

    def OptimizeTour(self, request, context):
        # Implement TSP optimization
        pass

    def PartitionFleet(self, request, context):
        # Implement VRP optimization
        pass

def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    routing_pb2_grpc.add_RoutingServiceServicer_to_server(
        RoutingServicer(), server)
    server.add_insecure_port('[::]:50051')
    server.start()
    server.wait_for_termination()

if __name__ == '__main__':
    serve()
```

Generate Python protobuf code:
```bash
cd ortools-service
python -m grpc_tools.protoc -I../pkg/proto \
    --python_out=. \
    --grpc_python_out=. \
    ../pkg/proto/routing.proto
```

### 2. Wire Routing Client in Go

Create `internal/adapters/routing/client.go`:

```go
package routing

import (
    "context"
    pb "github.com/andrescamacho/spacetraders-go/pkg/proto/routing"
    "github.com/andrescamacho/spacetraders-go/internal/application/common"
    "google.golang.org/grpc"
)

type ORToolsClient struct {
    client pb.RoutingServiceClient
    conn   *grpc.ClientConn
}

func NewORToolsClient(address string) (*ORToolsClient, error) {
    conn, err := grpc.Dial(address, grpc.WithInsecure())
    if err != nil {
        return nil, err
    }

    return &ORToolsClient{
        client: pb.NewRoutingServiceClient(conn),
        conn:   conn,
    }, nil
}

func (c *ORToolsClient) PlanRoute(ctx context.Context, req *common.RouteRequest) (*common.RouteResponse, error) {
    // Convert common.RouteRequest → pb.PlanRouteRequest
    pbReq := &pb.PlanRouteRequest{
        SystemSymbol:  req.SystemSymbol,
        StartWaypoint: req.StartWaypoint,
        // ... map all fields
    }

    resp, err := c.client.PlanRoute(ctx, pbReq)
    if err != nil {
        return nil, err
    }

    // Convert pb.PlanRouteResponse → common.RouteResponse
    return mapToRouteResponse(resp), nil
}
```

Update daemon main to initialize routing client:
```go
routingClient, err := routing.NewORToolsClient("localhost:50051")
if err != nil {
    return fmt.Errorf("failed to connect to routing service: %w", err)
}
defer routingClient.Close()

navigateHandler := navigation.NewNavigateShipHandler(
    playerRepo,
    waypointRepo,
    apiClient,
    routingClient,  // Now fully wired!
)
```

### 3. Implement Remaining Commands

Add handlers for:
- `DockShipCommand` + `DockShipHandler`
- `OrbitShipCommand` + `OrbitShipHandler`
- `RefuelShipCommand` + `RefuelShipHandler`

Update daemon server to wire these handlers.

### 4. Add Container Log Persistence

Create `ContainerLogRepository`:
```go
type ContainerLogRepository interface {
    SaveLog(ctx context.Context, containerID string, log *LogEntry) error
    GetLogs(ctx context.Context, containerID string, filters *LogFilters) ([]*LogEntry, error)
}
```

Update `ContainerRunner.log()` to persist to database.

## File Locations Reference

### Protobuf Schemas:
- `pkg/proto/daemon.proto` - CLI ↔ Daemon (✅ Done)
- `pkg/proto/routing.proto` - Daemon ↔ OR-Tools (✅ Done)

### Domain Layer:
- `internal/domain/container/container.go` - Container entity (✅ Done)
- `internal/domain/navigation/ship.go` - Ship entity (✅ Done)
- `internal/domain/navigation/route.go` - Route entity (✅ Done)

### Application Layer:
- `internal/application/common/mediator.go` - CQRS mediator (✅ Done)
- `internal/application/navigation/navigate_ship.go` - NavigateShip handler (✅ Done)

### Adapters:
- `internal/adapters/grpc/daemon_server.go` - gRPC server skeleton (✅ Done)
- `internal/adapters/grpc/container_runner.go` - Container execution (✅ Done)
- `internal/adapters/cli/*.go` - CLI commands (✅ Done)

### Entrypoints:
- `cmd/spacetraders-daemon/main.go` - Daemon binary (✅ Done)
- `cmd/spacetraders/main.go` - CLI binary (✅ Done)

## Troubleshooting

### Issue: `protoc: command not found`
**Solution**: Install protobuf compiler (see Step 1)

### Issue: `protoc-gen-go: program not found or is not executable`
**Solution**: Install Go plugins and ensure `$GOPATH/bin` is in `$PATH`
```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Issue: `cannot find package "google.golang.org/grpc"`
**Solution**: Add gRPC dependencies (see Step 3)

### Issue: Daemon fails to start with "address already in use"
**Solution**: Remove stale socket file
```bash
rm /tmp/spacetraders-daemon.sock
./bin/spacetraders-daemon
```

### Issue: CLI can't connect to daemon
**Solution**: Check socket path matches
```bash
# Daemon uses
echo $SPACETRADERS_SOCKET  # or /tmp/spacetraders-daemon.sock

# CLI must use same path
./bin/spacetraders --socket /tmp/spacetraders-daemon.sock health
```

## Testing Checklist

- [ ] Protobuf code generates without errors
- [ ] Daemon compiles and starts
- [ ] CLI compiles and runs
- [ ] Health check works
- [ ] Navigate command creates container
- [ ] Container transitions to RUNNING
- [ ] Container logs are visible
- [ ] Container can be stopped
- [ ] Graceful shutdown works (Ctrl+C)
- [ ] Multiple containers can run concurrently

## Performance Goals

- [ ] CLI response time < 500ms
- [ ] Daemon startup time < 2s
- [ ] 10+ concurrent containers running smoothly
- [ ] Graceful shutdown < 5s with 100 containers
- [ ] Memory usage < 100MB with 50 containers

## Success Criteria

✅ **Functional**:
- Navigate command works end-to-end
- Containers execute in background
- Logs are visible and persisted
- Graceful shutdown works

✅ **Performance**:
- CLI is responsive (< 500ms)
- Daemon handles 10+ containers
- No memory leaks

✅ **Code Quality**:
- All code compiles
- No linter warnings
- Unit tests pass
- Architecture maintained

---

**Ready to proceed?** Start with Step 1 and work through sequentially. Each step builds on the previous one.

**Estimated time to working POC**: 1-2 hours for gRPC wiring, 4-8 hours for OR-Tools service.
