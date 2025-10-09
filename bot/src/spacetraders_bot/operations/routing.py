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
from spacetraders_bot.core.routing import GraphBuilder, RouteOptimizer, TimeCalculator, TourOptimizer
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
    log_file = setup_logging("graph-build", ship_name, getattr(args, 'log_level', 'INFO'))

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
    log_file = setup_logging("route-plan", args.ship, getattr(args, 'log_level', 'INFO'))

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

    # Plan route
    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        args.start,
        args.goal,
        ship_data['fuel']['current'],
        prefer_cruise=not args.drift_only
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


    print("=" * 70)
    print("MULTI-STOP TOUR PLANNING")
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

    # Parse stops
    stops = [s.strip() for s in args.stops.split(',')]

    print(f"Ship: {args.ship} (speed: {ship_data['engine']['speed']})")
    print(f"Start: {args.start}")
    print(f"Stops: {', '.join(stops)}")
    print(f"Return to start: {args.return_to_start}\n")

    # Plan tour (using greedy algorithm by default, with caching)
    optimizer = TourOptimizer(graph, ship_data)
    algorithm = getattr(args, 'algorithm', 'greedy')
    tour = optimizer.plan_tour(
        args.start,
        stops,
        ship_data['fuel']['current'],
        return_to_start=args.return_to_start,
        algorithm=algorithm,
        use_cache=True
    )

    if not tour:
        print("❌ No tour found")
        return 1

    # Display tour
    print(f"{'='*70}")
    print("OPTIMAL TOUR")
    print('='*70)
    print(f"Total legs: {tour['total_legs']}")
    print(f"Total time: {TimeCalculator.format_time(tour['total_time'])}")
    print(f"Final fuel: {tour['final_fuel']}/{ship_data['fuel']['capacity']}\n")

    for i, leg in enumerate(tour['legs'], 1):
        print(f"LEG {i}: {leg['start']} → {leg['goal']}")
        print(f"  Time: {TimeCalculator.format_time(leg['total_time'])}")
        print(f"  Steps: {len(leg['steps'])}")
        print()

    print('='*70)

    # Save tour if requested
    if args.output:
        output_file = Path(args.output)
        output_file.parent.mkdir(parents=True, exist_ok=True)
        with open(output_file, 'w') as f:
            json.dump(tour, f, indent=2)
        print(f"Tour saved to: {output_file}")

    return 0


def scout_markets_operation(args):
    """
    Scout all markets in a system using optimized tour planning

    This uses greedy nearest-neighbor or 2-Opt optimization to find
    near-optimal routes for visiting all markets as quickly as possible.

    With --continuous flag, restarts immediately after completing each tour
    for continuous market intelligence gathering.
    """
    import signal
    import time

    setup_logging('SCOUT-MARKETS', args.ship, getattr(args, 'log_level', 'INFO'))
    logger = logging.getLogger(__name__ + '.scout_markets')
    logger.info("=" * 70)
    logger.info("SpaceTraders Bot - SCOUT-MARKETS Operation")
    logger.info("=" * 70)

    # Initialize API
    api = get_api_client(args.player_id)

    operation_start = datetime.now(timezone.utc)
    captain_logger = get_captain_logger(args.player_id)
    operator_name = get_operator_name(args)

    def log_error(error: str, cause: str, *, impact: Optional[Dict] = None,
                  resolution: str = "Manual follow-up", lesson: str = "Review scouting configuration",
                  escalate: bool = False, tags: Optional[List[str]] = None) -> None:
        log_captain_event(
            captain_logger,
            'CRITICAL_ERROR',
            operator=operator_name,
            ship=args.ship,
            error=error,
            cause=cause,
            impact=impact or {},
            resolution=resolution,
            lesson=lesson,
            escalate=escalate,
            tags=tags or ['scouting']
        )

    def log_tour_completion(markets_scouted: int, goods_updated: int, total_time: str, tour_index: int) -> None:
        duration = humanize_duration(datetime.now(timezone.utc) - tour_start)
        results = {
            'Markets Visited': markets_scouted,
            'Goods Updated': goods_updated,
            'Planned Duration': total_time,
            'Tour': tour_index,
        }
        notes = f"Scouted markets in {args.system}."
        log_captain_event(
            captain_logger,
            'OPERATION_COMPLETED',
            operator=operator_name,
            ship=args.ship,
            duration=duration,
            results=results,
            notes=notes,
            tags=['scouting', args.system.lower()]
        )

    def log_tour_summary(markets_scouted: int, goods_updated: int, tour_index: int) -> None:
        log_captain_event(
            captain_logger,
            'PERFORMANCE_SUMMARY',
            summary_type='Market Scouting',
            financials={'revenue': 0, 'cumulative': 0, 'rate': 0},
            operations={'completed': tour_index, 'active': 0, 'success_rate': 100},
            fleet={'active': 1, 'total': 1},
            top_performers=[{
                'ship': args.ship,
                'profit': goods_updated,
                'operation': 'scouting'
            }],
            tags=['scouting', 'performance']
        )

    # Continuous mode flag
    continuous = getattr(args, 'continuous', False)
    running = True
    tour_count = 0

    # Signal handler for graceful shutdown
    def handle_shutdown(signum, frame):
        nonlocal running
        logger.info(f"Received signal {signum}, shutting down after current tour...")
        running = False

    if continuous:
        signal.signal(signal.SIGTERM, handle_shutdown)
        signal.signal(signal.SIGINT, handle_shutdown)
        logger.info("🔄 CONTINUOUS MODE ENABLED - Tours will loop indefinitely")

    # Main loop (runs once if not continuous)
    while running:
        tour_count += 1
        tour_start = datetime.now(timezone.utc)

        if continuous:
            logger.info(f"\n{'='*70}")
            logger.info(f"STARTING TOUR #{tour_count}")
            logger.info(f"{'='*70}")

        # Load or build graph from database
        builder = GraphBuilder(api)
        graph = builder.load_system_graph(args.system)

        if not graph:
            logger.info(f"Graph not found in database, building graph for {args.system}...")
            graph = builder.build_system_graph(args.system)
            if not graph:
                logger.error(f"Failed to build graph for system {args.system}")
                log_error(
                    "Graph build failed",
                    f"Unable to build graph for system {args.system}",
                    resolution="Run graph-build operation manually",
                    escalate=True
                )
                return 1

        # Get ship data first (needed for current location)
        ship_data = api.get_ship(args.ship)
        if not ship_data:
            logger.error(f"Failed to get ship data for {args.ship}")
            log_error(
                "Ship data unavailable",
                f"API returned no data for {args.ship}",
                resolution="Ensure ship exists and token has access",
                escalate=True
            )
            return 1

        current_location = ship_data['nav']['waypointSymbol']
        logger.info(f"Ship {args.ship} currently at {current_location}")

        # Determine which markets to visit and tour start point
        if hasattr(args, 'markets_list') and args.markets_list:
            # BUG FIX: When coordinator provides specific markets list (partitioned assignment),
            # start tour from FIRST assigned market (partition centroid), NOT current location.
            # This prevents overlap when all ships start at same waypoint but have disjoint assignments.
            markets = [m.strip() for m in args.markets_list.split(',')]
            logger.info(f"Using specific markets list (coordinator-assigned partition): {', '.join(markets)}")

            # Tour starts from first assigned market (partition centroid)
            # This ensures tours are independent of where ships happen to be stationed
            tour_start_location = markets[0]
            logger.info(f"Tour will start from first assigned market (partition centroid): {tour_start_location}")

            # All markets in list will be visited (including the start point)
            # The tour planning will handle this by removing start from stops internally
            market_stops = markets
        else:
            # Auto-discover markets from graph (non-partitioned mode)
            markets = TourOptimizer.get_markets_from_graph(graph)
            logger.info(f"Found {len(markets)} markets in {args.system}: {', '.join(markets)}")

            if not markets:
                logger.error("No markets found in system")
                log_error(
                    "No markets discovered",
                    f"Graph for {args.system} contains no markets",
                    resolution="Verify system symbol or rebuild graph",
                    escalate=True
                )
                return 1

            # Remove current location from markets list (will visit all others from here)
            tour_start_location = current_location
            market_stops = [m for m in markets if m != current_location]

            # Limit to requested number of markets (if specified)
            if hasattr(args, 'markets') and args.markets and args.markets < len(market_stops):
                market_stops = market_stops[:args.markets]
                logger.info(f"Limiting to {args.markets} markets (excluding current location)")

        if not market_stops:
            logger.info("Ship is at the only market in the system!")
            if not continuous:
                return 0
            time.sleep(60)  # Wait a minute before next tour
            continue

        # Initialize optimizer
        optimizer = TourOptimizer(graph, ship_data)

        # Run optimization based on algorithm choice
        algorithm = args.algorithm.lower()

        if algorithm in ['greedy', '2opt']:
            logger.info(f"Using {algorithm} algorithm with caching...")
            tour = optimizer.plan_tour(
                tour_start_location, market_stops,
                ship_data['fuel']['current'],
                return_to_start=args.return_to_start,
                algorithm=algorithm,
                use_cache=True
            )
        else:
            logger.error(f"Unknown algorithm: {algorithm}")
            log_error(
                "Unknown routing algorithm",
                f"Algorithm '{algorithm}' not supported",
                resolution="Use 'greedy' or '2opt'",
                escalate=False
            )
            return 1

        if not tour:
            logger.error("Failed to find tour")
            if not continuous:
                log_error(
                    "Tour planning failed",
                    "Optimizer returned no tour",
                    resolution="Rebuild graph or adjust algorithm",
                    escalate=True
                )
                return 1
            logger.info("Waiting 60s before retry...")
            time.sleep(60)
            continue

        # Print results
        print(f"\n{'='*70}")
        print(f"MARKET SCOUT TOUR - {args.system}")
        if continuous:
            print(f"Tour #{tour_count}")
        print(f"{'='*70}")
        print(f"Algorithm: {algorithm.upper()}")
        print(f"Markets to visit: {len(market_stops)}")
        planned_time = TimeCalculator.format_time(tour['total_time'])
        print(f"Total time: {planned_time}")
        print(f"Total legs: {tour['total_legs']}")
        print(f"Final fuel: {tour['final_fuel']}/{ship_data['fuel']['capacity']}")
        print(f"\nRoute order:")
        for i, leg in enumerate(tour['legs'], 1):
            dest = leg['goal'] if 'goal' in leg else leg['legs'][0]['goal']
            print(f"  {i}. {dest}")

        # Save to file if requested
        if args.output:
            output_file = Path(args.output)
            output_file.parent.mkdir(parents=True, exist_ok=True)
            with open(output_file, 'w') as f:
                json.dump(tour, f, indent=2)
            print(f"\nTour saved to: {output_file}")

        # Execute the tour: navigate to each market and collect data
        logger.info(f"\n{'='*70}")
        logger.info("EXECUTING TOUR - NAVIGATING AND COLLECTING MARKET DATA")
        logger.info(f"{'='*70}")

        from spacetraders_bot.core.ship_controller import ShipController
        from spacetraders_bot.core.smart_navigator import SmartNavigator
        from spacetraders_bot.core.utils import timestamp_iso
        from .common import get_database

        ship = ShipController(api, args.ship)
        navigator = SmartNavigator(api, args.system)
        db = get_database()

        markets_scouted = 0
        goods_updated = 0

        # Visit each market in the optimized tour order
        for i, leg in enumerate(tour['legs'], 1):
            destination = leg['goal'] if 'goal' in leg else leg['legs'][0]['goal']
            logger.info(f"\n[{i}/{len(tour['legs'])}] Navigating to {destination}...")

            # Navigate to market
            if not navigator.execute_route(ship, destination):
                logger.warning(f"Failed to navigate to {destination}, skipping")
                continue

            # Dock to access market
            ship.dock()

            # Get market data
            system_symbol = args.system
            market = api.get_market(system_symbol, destination)

            if market:
                trade_goods = market.get('tradeGoods', [])
                timestamp = timestamp_iso()

                # Update database immediately for each good
                with db.transaction() as db_conn:
                    for good in trade_goods:
                        db.update_market_data(
                            db_conn,
                            waypoint_symbol=destination,
                            good_symbol=good['symbol'],
                            supply=good.get('supply'),
                            activity=good.get('activity'),
                            purchase_price=good.get('purchasePrice', 0),
                            sell_price=good.get('sellPrice', 0),
                            trade_volume=good.get('tradeVolume', 0),
                            last_updated=timestamp,
                            player_id=args.player_id
                        )
                        goods_updated += 1

                markets_scouted += 1
                logger.info(f"✅ Updated database: {len(trade_goods)} goods")
            else:
                logger.warning(f"Failed to get market data for {destination}")

        logger.info(f"\n{'='*70}")
        logger.info("TOUR EXECUTION COMPLETE")
        logger.info(f"{'='*70}")
        logger.info(f"Markets scouted: {markets_scouted}/{len(tour['legs'])}")
        logger.info(f"Goods updated in database: {goods_updated}")
        logger.info(f"{'='*70}")

        log_tour_completion(markets_scouted, goods_updated, planned_time, tour_count)
        log_tour_summary(markets_scouted, goods_updated, tour_count)

        if continuous:
            logger.info(f"Tour #{tour_count} complete, restarting immediately...")
        else:
            # Exit loop if not continuous
            break

    if continuous:
        logger.info(f"\n🛑 Continuous scouting stopped after {tour_count} tours")

    return 0
