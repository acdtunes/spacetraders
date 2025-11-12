# Routing Service Quick Start Guide

This guide will help you get the routing service up and running in 5 minutes.

## What is the Routing Service?

The routing service is a standalone Python gRPC microservice that provides advanced route optimization using Google OR-Tools. It handles:

- **Pathfinding** - Find optimal routes with fuel constraints
- **Tour Optimization** - Optimize visit order for multiple waypoints (TSP)
- **Fleet Partitioning** - Distribute waypoints across multiple ships (VRP)

## Prerequisites

- Python 3.8+
- Go 1.21+
- Make (optional but recommended)

## Quick Start (5 Minutes)

### Step 1: Build Everything

```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/gobot
make build
```

This builds:
- CLI binary → `bin/spacetraders`
- Daemon binary → `bin/spacetraders-daemon`
- Routing service manager → `bin/routing-service`

### Step 2: Start Routing Service

**Option A: Using Makefile (Recommended)**
```bash
make run-routing-service
```

**Option B: Using Run Script**
```bash
cd services/routing-service
bash run.sh
```

**Option C: Direct Python**
```bash
cd services/routing-service
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
bash generate_protos.sh
python3 server/main.py
```

You should see:
```
Routing service started on 0.0.0.0:50051
TSP timeout: 5s, VRP timeout: 30s
```

### Step 3: Start Daemon with Routing Service

In a new terminal:

```bash
export ROUTING_SERVICE_ADDR="localhost:50051"
make run-daemon
```

You should see:
```
Connecting to routing service at localhost:50051...
Routing client initialized (gRPC OR-Tools service)
✓ Daemon is ready to accept connections
```

### Step 4: Test Navigation (Optional)

In a third terminal:

```bash
# Register a player
./bin/spacetraders player register MYAGENT YOUR_TOKEN

# Navigate a ship (this will use the routing service)
./bin/spacetraders navigate MYAGENT-1 X1-GZ7-B1
```

## Running Without Routing Service

The daemon works perfectly fine without the routing service - it falls back to a simple mock client:

```bash
# No ROUTING_SERVICE_ADDR set
make run-daemon
```

You'll see:
```
Routing client initialized (mock - set ROUTING_SERVICE_ADDR to use real service)
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ROUTING_SERVICE_ADDR` | _(empty)_ | Address of routing service (e.g., `localhost:50051`) |
| `ROUTING_HOST` | `0.0.0.0` | Host for routing service to bind |
| `ROUTING_PORT` | `50051` | Port for routing service |
| `TSP_TIMEOUT` | `5` | TSP solver timeout (seconds) |
| `VRP_TIMEOUT` | `30` | VRP solver timeout (seconds) |

### Example: Custom Port

```bash
# Terminal 1: Start routing service on custom port
export ROUTING_PORT=8080
make run-routing-service

# Terminal 2: Connect daemon to custom port
export ROUTING_SERVICE_ADDR="localhost:8080"
make run-daemon
```

## Troubleshooting

### Issue: "failed to connect to routing service"

**Solution**: Make sure routing service is running first, then start daemon.

```bash
# Check if routing service is running
lsof -i :50051
```

### Issue: "ModuleNotFoundError: No module named 'ortools'"

**Solution**: Install Python dependencies in virtual environment.

```bash
cd services/routing-service
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
```

### Issue: "No module named 'generated'"

**Solution**: Generate protobuf files.

```bash
cd services/routing-service
bash generate_protos.sh
```

### Issue: Port already in use

**Solution**: Kill existing process or use a different port.

```bash
# Find and kill process on port 50051
lsof -ti:50051 | xargs kill

# Or use a different port
export ROUTING_PORT=50052
```

## Testing the Routing Service

### Run Python Tests

```bash
cd services/routing-service
source venv/bin/activate
python3 test_service.py
```

Expected output:
```
=== Testing PlanRoute ===
✓ Route found!
  Total fuel cost: 120
  Total time: 450s
  Total distance: 120.50
  Steps: 1

=== Testing OptimizeTour ===
✓ Tour optimized!
  Visit order: START -> M2 -> M1 -> M4 -> M3 -> START
  Total time: 2450s
  Total distance: 425.32

=== Testing PartitionFleet ===
✓ Fleet partitioned!
  Ships utilized: 2
  Waypoints assigned: 4

=== Test Results ===
✓ PASS - PlanRoute
✓ PASS - OptimizeTour
✓ PASS - PartitionFleet

Total: 3/3 tests passed
```

### Run Go Integration Tests

```bash
# With routing service running
go test ./internal/adapters/routing/... -v
```

## Architecture Overview

```
┌─────────────────┐         gRPC          ┌──────────────────────┐
│   Go Daemon     │◄────────────────────►│  Routing Service     │
│   (main logic)  │   (routing.proto)     │  (Python + OR-Tools) │
└─────────────────┘                       └──────────────────────┘
```

**Why separate service?**
- OR-Tools is most mature in Python
- Keeps complex routing logic isolated
- Allows independent scaling
- Daemon can run with or without it

## Next Steps

### For Development

1. **Read the architecture docs**: See `docs/ROUTING_SERVICE_ARCHITECTURE.md`
2. **Explore the algorithms**: Check `services/routing-service/utils/routing_engine.py`
3. **Modify timeout settings**: Adjust `TSP_TIMEOUT` and `VRP_TIMEOUT` for your use case

### For Production

1. **Run as systemd service** (Linux):
```bash
sudo systemctl enable routing-service
sudo systemctl start routing-service
```

2. **Use Docker Compose**:
```yaml
version: '3.8'
services:
  routing-service:
    build: ./services/routing-service
    ports:
      - "50051:50051"

  daemon:
    build: .
    environment:
      - ROUTING_SERVICE_ADDR=routing-service:50051
    depends_on:
      - routing-service
```

3. **Monitor with health checks**:
```bash
# Check if service is responding
grpcurl -plaintext localhost:50051 list
```

## Useful Makefile Targets

```bash
# Build everything
make build

# Run routing service only
make run-routing-service

# Run daemon with routing service
make run-with-routing

# Generate protobuf files (Go + Python)
make proto

# Clean everything (including Python venv)
make clean

# Show all available targets
make help
```

## Resources

- **Service README**: `services/routing-service/README.md`
- **Architecture Docs**: `docs/ROUTING_SERVICE_ARCHITECTURE.md`
- **Protocol Definition**: `pkg/proto/routing/routing.proto`
- **Python Implementation**: `services/routing-service/utils/routing_engine.py`
- **Go Client**: `internal/adapters/routing/grpc_routing_client.go`

## Support

For issues or questions:
1. Check the troubleshooting section above
2. Read the full architecture documentation
3. Check Python service logs for detailed error messages
4. Verify protobuf files are generated (`make proto`)

## Summary

The routing service is **optional but recommended** for production use. It provides sophisticated route optimization using OR-Tools algorithms ported from the Python bot.

**Without routing service**: Daemon uses simple mock client (good for development)
**With routing service**: Daemon uses real OR-Tools optimization (good for production)

Both work seamlessly - just set `ROUTING_SERVICE_ADDR` to enable the real service.
