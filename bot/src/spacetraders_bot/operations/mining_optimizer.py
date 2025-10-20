#!/usr/bin/env python3
"""Mining fleet optimization operation using OR-Tools."""

import json
import sys

from spacetraders_bot.core.database import get_database
from spacetraders_bot.core.ortools_mining_optimizer import ORToolsMiningOptimizer
from spacetraders_bot.core.route_planner import GraphBuilder
from spacetraders_bot.operations.common import get_api_client, setup_logging


def mining_optimize_operation(args):
    """
    Optimize mining fleet assignments using OR-Tools Assignment solver.

    Assigns ships to optimal asteroid-market pairs to maximize total fleet
    profit per hour.

    Args:
        args: Argument namespace with:
            - player_id: Player ID
            - system: System symbol (e.g., X1-HU87)
            - ships: Optional comma-separated ship symbols (or all EXCAVATOR ships)
            - algorithm: Algorithm choice ('ortools' or 'greedy')
            - output: Optional JSON output file path
    """
    log_file = setup_logging(
        "mining-optimize",
        args.system,
        getattr(args, 'log_level', 'INFO')
    )

    api = get_api_client(args.player_id)
    db = get_database()

    print("=" * 80)
    print("MINING FLEET OPTIMIZATION")
    print("=" * 80)
    print(f"System: {args.system}")
    print(f"Algorithm: {args.algorithm}")
    print()

    # Get ships to optimize
    if hasattr(args, 'ships') and args.ships:
        # Use specified ships
        ship_symbols = [s.strip() for s in args.ships.split(',')]
        ships = []
        for symbol in ship_symbols:
            ship_data = api.get_ship(symbol)
            if not ship_data:
                print(f"⚠️  Warning: Could not fetch data for ship {symbol}, skipping")
                continue
            ships.append(ship_data)
    else:
        # Get all EXCAVATOR ships
        fleet_result = api.list_ships()
        if not fleet_result or 'data' not in fleet_result:
            print("❌ Failed to fetch fleet")
            return 1

        ships = [
            s for s in fleet_result['data']
            if s.get('registration', {}).get('role') == 'EXCAVATOR'
        ]

    if not ships:
        print("❌ No mining ships found")
        return 1

    print(f"Ships to optimize: {len(ships)}")
    for ship in ships:
        print(f"  - {ship['symbol']} (speed: {ship['engine']['speed']}, "
              f"cargo: {ship['cargo']['capacity']}, fuel: {ship['fuel']['capacity']})")
    print()

    # Build system graph
    print("Building system navigation graph...")
    try:
        builder = GraphBuilder(api)
        graph = builder.build_system_graph(args.system)
        print(f"✅ Graph built: {len(graph.get('waypoints', {}))} waypoints\n")
    except Exception as e:
        print(f"❌ Failed to build graph: {e}")
        return 1

    # Run optimization based on algorithm
    if args.algorithm == 'ortools':
        print("Running OR-Tools optimization...")
        try:
            optimizer = ORToolsMiningOptimizer(args.system, graph, db)
            assignments = optimizer.optimize_fleet_assignment(ships)

            if not assignments:
                print("⚠️  No profitable assignments found")
                return 0

            print(f"✅ Optimized {len(assignments)} ship assignments\n")

            # Display results
            print("=" * 80)
            print("OPTIMAL FLEET ASSIGNMENTS")
            print("=" * 80)

            total_profit = 0
            results = []

            for ship_symbol, assignment in assignments.items():
                total_profit += assignment.profit_per_hour

                print(f"\n{ship_symbol}:")
                print(f"  Asteroid: {assignment.asteroid}")
                print(f"  Market: {assignment.market}")
                print(f"  Material: {assignment.good}")
                print(f"  Profit/Hour: {assignment.profit_per_hour:,.0f} cr/hr")
                print(f"  Cycle Time: {assignment.cycle_time_minutes:.1f} minutes")
                print(f"  Revenue/Cycle: {assignment.revenue_per_cycle:,} cr")
                print(f"  Fuel Cost/Cycle: {assignment.fuel_cost_per_cycle:,} cr")

                # Build result dict for JSON output
                results.append({
                    "ship": ship_symbol,
                    "asteroid": assignment.asteroid,
                    "market": assignment.market,
                    "good": assignment.good,
                    "profit_per_hour": assignment.profit_per_hour,
                    "cycle_time_minutes": assignment.cycle_time_minutes,
                    "fuel_cost_per_cycle": assignment.fuel_cost_per_cycle,
                    "revenue_per_cycle": assignment.revenue_per_cycle,
                })

            print("\n" + "=" * 80)
            print(f"FLEET SUMMARY")
            print(f"Total Fleet Profit: {total_profit:,.0f} cr/hr")
            print(f"Average Profit per Ship: {total_profit / len(assignments):,.0f} cr/hr")
            print("=" * 80)

            # Write JSON output if requested
            if hasattr(args, 'output') and args.output:
                output_data = {
                    "algorithm": "ortools",
                    "system": args.system,
                    "assignments": results,
                    "fleet_summary": {
                        "total_profit_per_hour": total_profit,
                        "average_profit_per_ship": total_profit / len(assignments),
                        "ships_assigned": len(assignments),
                    }
                }

                with open(args.output, 'w') as f:
                    json.dump(output_data, f, indent=2)

                print(f"\n✅ Results saved to {args.output}")

            return 0

        except Exception as e:
            print(f"❌ OR-Tools optimization failed: {e}")
            import traceback
            traceback.print_exc()
            return 1

    elif args.algorithm == 'greedy':
        print("⚠️  Greedy algorithm not yet implemented for fleet optimization")
        print("   Use 'ortools' algorithm or use 'util find-mining' for single-ship greedy")
        return 1

    else:
        print(f"❌ Unknown algorithm: {args.algorithm}")
        return 1
