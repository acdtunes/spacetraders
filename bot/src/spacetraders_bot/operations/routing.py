#!/usr/bin/env python3
"""
Routing operations: graph building, route planning, tour optimization
"""

import json
import logging
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, List, Optional

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.route_planner import GraphBuilder, RouteOptimizer, TimeCalculator, TourOptimizer
from spacetraders_bot.core.routing_pause import is_paused as routing_paused, get_pause_details
from spacetraders_bot.operations.common import (
    setup_logging,
    get_api_client,
    get_captain_logger,
    log_captain_event,
    humanize_duration,
    get_operator_name,
)


def graph_build_operation(args):
    """Build navigation graph for a system"""
    ship_name = "system"
    log_file = setup_logging("graph-build", ship_name, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    print("=" * 70)
    print("BUILDING SYSTEM NAVIGATION GRAPH")
    print("=" * 70)
    print(f"System: {args.system}")
    print(f"Output: graphs/{args.system}_graph.json\n")

    api = get_api_client(args.player_id)
    builder = GraphBuilder(api)

    # Build graph
    graph = builder.build_system_graph(args.system)

    if graph:
        print(f"\n{'='*70}")
        print("GRAPH BUILD COMPLETE")
        print('='*70)
        print(f"Waypoints: {len(graph['waypoints'])}")
        print(f"Edges: {len(graph['edges'])}")
        print(f"Fuel stations: {sum(1 for wp in graph['waypoints'].values() if wp['has_fuel'])}")
        print('='*70)
        return 0
    else:
        print("❌ Failed to build graph")
        return 1


def route_plan_operation(args):
    """Plan optimal route with fuel awareness"""
    log_file = setup_logging("route-plan", args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    print("=" * 70)
    print("ROUTE PLANNING")
    print("=" * 70)

    api = get_api_client(args.player_id)
    db = Database()

    # Load graph from database
    with db.connection() as conn:
        graph = db.get_system_graph(conn, args.system)

    if not graph:
        print(f"❌ Graph not found in database for system {args.system}")
        print(f"Building graph now...")
        builder = GraphBuilder(api)
        graph = builder.build_system_graph(args.system)
        if not graph:
            print(f"❌ Failed to build graph")
            return 1

    # Get ship data
    ship_data = api.get_ship(args.ship)
    if not ship_data:
        print(f"❌ Failed to get ship data for {args.ship}")
        return 1

    print(f"Ship: {args.ship}")
    print(f"  Frame: {ship_data['frame']['symbol']}")
    print(f"  Engine: {ship_data['engine']['symbol']} (speed: {ship_data['engine']['speed']})")
    print(f"  Fuel: {ship_data['fuel']['current']}/{ship_data['fuel']['capacity']}")
    print(f"\nRoute: {args.start} → {args.goal}\n")

    if routing_paused():
        details = get_pause_details() or {}
        print(f"❌ Routing is paused: {details.get('reason', 'Validation failure')}\n")
        return 1

    # Plan route
    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        args.start,
        args.goal,
        ship_data['fuel']['current'],
    )

    if not route:
        print("❌ No route found")
        return 1

    # Display route
    print(f"{'='*70}")
    print("OPTIMAL ROUTE")
    print('='*70)
    print(f"Total time: {TimeCalculator.format_time(route['total_time'])}")
    print(f"Final fuel: {route['final_fuel']}/{ship_data['fuel']['capacity']}\n")

    for i, step in enumerate(route['steps'], 1):
        if step['action'] == 'navigate':
            print(f"{i}. NAVIGATE {step['from']} → {step['to']}")
            print(f"   Mode: {step['mode']}")
            print(f"   Distance: {step['distance']:.1f} units")
            print(f"   Time: {TimeCalculator.format_time(step['time'])}")
            print(f"   Fuel: -{step['fuel_cost']}")
        elif step['action'] == 'refuel':
            print(f"{i}. REFUEL at {step['waypoint']}")
            print(f"   Fuel added: +{step['fuel_added']}")
        print()

    print('='*70)

    # Save route if requested
    if args.output:
        output_file = Path(args.output)
        output_file.parent.mkdir(parents=True, exist_ok=True)
        with open(output_file, 'w') as f:
            json.dump(route, f, indent=2)
        print(f"Route saved to: {output_file}")

    return 0


def scout_markets_operation(args):
    """
    Scout all markets in a system using optimized tour planning

    This uses greedy nearest-neighbor or 2-Opt optimization to find
    near-optimal routes for visiting all markets as quickly as possible.

    With --continuous flag, restarts immediately after completing each tour
    for continuous market intelligence gathering.

    REFACTORED: Now delegates to ScoutMarketsExecutor for better testability.
    """
    setup_logging('SCOUT-MARKETS', args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)
    logger = logging.getLogger(__name__ + '.scout_markets')
    logger.info("=" * 70)
    logger.info("SpaceTraders Bot - SCOUT-MARKETS Operation")
    logger.info("=" * 70)

    if routing_paused():
        details = get_pause_details() or {}
        logger.error("Routing is paused: %s", details.get('reason', 'Validation failure'))
        return 1

    # Initialize API and captain logger
    api = get_api_client(args.player_id)
    captain_logger = get_captain_logger(args.player_id)

    # Delegate to executor (new SOLID architecture)
    from .scouting import ScoutMarketsExecutor
    executor = ScoutMarketsExecutor(args, api, logger, captain_logger)
    return executor.run()
