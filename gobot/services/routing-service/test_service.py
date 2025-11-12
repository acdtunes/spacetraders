"""
Simple test script for routing service.

Tests the gRPC service locally without needing the full Go daemon.
"""
import sys
import os

# Add generated protos to path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), 'generated'))

import grpc
from generated import routing_pb2
from generated import routing_pb2_grpc


def test_plan_route():
    """Test PlanRoute RPC"""
    print("\n=== Testing PlanRoute ===")

    # Connect to service
    channel = grpc.insecure_channel('localhost:50051')
    stub = routing_pb2_grpc.RoutingServiceStub(channel)

    # Create test waypoints (simple 3-waypoint system)
    waypoints = [
        routing_pb2.Waypoint(symbol="A", x=0.0, y=0.0, has_fuel=True),
        routing_pb2.Waypoint(symbol="B", x=100.0, y=0.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="C", x=200.0, y=0.0, has_fuel=True),
    ]

    # Request route from A to C
    request = routing_pb2.PlanRouteRequest(
        system_symbol="TEST-SYSTEM",
        start_waypoint="A",
        goal_waypoint="C",
        current_fuel=100,
        fuel_capacity=200,
        engine_speed=30,
        waypoints=waypoints
    )

    try:
        response = stub.PlanRoute(request)

        if response.success:
            print(f"✓ Route found!")
            print(f"  Total fuel cost: {response.total_fuel_cost}")
            print(f"  Total time: {response.total_time_seconds}s")
            print(f"  Total distance: {response.total_distance:.2f}")
            print(f"  Steps: {len(response.steps)}")

            for i, step in enumerate(response.steps):
                action = "TRAVEL" if step.action == routing_pb2.ROUTE_ACTION_TRAVEL else "REFUEL"
                print(f"    {i+1}. {action} to {step.waypoint} (fuel={step.fuel_cost}, time={step.time_seconds}s)")
        else:
            print(f"✗ Route failed: {response.error_message}")

    except grpc.RpcError as e:
        print(f"✗ RPC error: {e}")
        return False

    return True


def test_optimize_tour():
    """Test OptimizeTour RPC"""
    print("\n=== Testing OptimizeTour ===")

    channel = grpc.insecure_channel('localhost:50051')
    stub = routing_pb2_grpc.RoutingServiceStub(channel)

    # Create test waypoints (5-waypoint tour)
    waypoints = [
        routing_pb2.Waypoint(symbol="START", x=0.0, y=0.0, has_fuel=True),
        routing_pb2.Waypoint(symbol="M1", x=100.0, y=50.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="M2", x=150.0, y=100.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="M3", x=50.0, y=150.0, has_fuel=True),
        routing_pb2.Waypoint(symbol="M4", x=25.0, y=75.0, has_fuel=False),
    ]

    # Request tour optimization
    request = routing_pb2.OptimizeTourRequest(
        system_symbol="TEST-SYSTEM",
        start_waypoint="START",
        target_waypoints=["M1", "M2", "M3", "M4"],
        fuel_capacity=400,
        engine_speed=30,
        all_waypoints=waypoints,
        return_to_start=True
    )

    try:
        response = stub.OptimizeTour(request)

        if response.success:
            print(f"✓ Tour optimized!")
            print(f"  Visit order: {' -> '.join(response.visit_order)}")
            print(f"  Total time: {response.total_time_seconds}s")
            print(f"  Total distance: {response.total_distance:.2f}")
        else:
            print(f"✗ Tour optimization failed: {response.error_message}")

    except grpc.RpcError as e:
        print(f"✗ RPC error: {e}")
        return False

    return True


def test_partition_fleet():
    """Test PartitionFleet RPC"""
    print("\n=== Testing PartitionFleet ===")

    channel = grpc.insecure_channel('localhost:50051')
    stub = routing_pb2_grpc.RoutingServiceStub(channel)

    # Create test waypoints
    waypoints = [
        routing_pb2.Waypoint(symbol="BASE", x=0.0, y=0.0, has_fuel=True),
        routing_pb2.Waypoint(symbol="M1", x=100.0, y=0.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="M2", x=200.0, y=0.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="M3", x=0.0, y=100.0, has_fuel=False),
        routing_pb2.Waypoint(symbol="M4", x=0.0, y=200.0, has_fuel=False),
    ]

    # Ship configurations
    ship_configs = {
        "SHIP-1": routing_pb2.ShipConfig(
            current_location="BASE",
            fuel_capacity=400,
            engine_speed=30,
            current_fuel=400
        ),
        "SHIP-2": routing_pb2.ShipConfig(
            current_location="BASE",
            fuel_capacity=400,
            engine_speed=30,
            current_fuel=400
        ),
    }

    # Request fleet partitioning
    request = routing_pb2.PartitionFleetRequest(
        system_symbol="TEST-SYSTEM",
        ship_symbols=["SHIP-1", "SHIP-2"],
        market_waypoints=["M1", "M2", "M3", "M4"],
        ship_configs=ship_configs,
        all_waypoints=waypoints,
        iterations=1
    )

    try:
        response = stub.PartitionFleet(request)

        if response.success:
            print(f"✓ Fleet partitioned!")
            print(f"  Ships utilized: {response.ships_utilized}")
            print(f"  Waypoints assigned: {response.total_waypoints_assigned}")

            for ship, tour in response.assignments.items():
                print(f"  {ship}: {', '.join(tour.waypoints)}")
        else:
            print(f"✗ Fleet partitioning failed: {response.error_message}")

    except grpc.RpcError as e:
        print(f"✗ RPC error: {e}")
        return False

    return True


def main():
    """Run all tests"""
    print("Routing Service Test Suite")
    print("===========================")
    print("Make sure the routing service is running on localhost:50051")

    results = []

    results.append(("PlanRoute", test_plan_route()))
    results.append(("OptimizeTour", test_optimize_tour()))
    results.append(("PartitionFleet", test_partition_fleet()))

    print("\n=== Test Results ===")
    passed = sum(1 for _, result in results if result)
    total = len(results)

    for name, result in results:
        status = "✓ PASS" if result else "✗ FAIL"
        print(f"{status} - {name}")

    print(f"\nTotal: {passed}/{total} tests passed")

    return 0 if passed == total else 1


if __name__ == '__main__':
    sys.exit(main())
