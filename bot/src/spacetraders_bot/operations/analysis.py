#!/usr/bin/env python3
"""Utility operations used by the bot CLI."""

import json

from spacetraders_bot.core.database import get_database
from spacetraders_bot.core.utils import calculate_distance, estimate_fuel_cost, parse_waypoint_symbol
from spacetraders_bot.operations.common import get_api_client, setup_logging


def utilities_operation(args):
    """Utility operations - replaces various utility shell scripts"""
    ship_name = args.ship if hasattr(args, 'ship') and args.ship else "system"
    log_file = setup_logging(f"util-{args.util_type}", ship_name, getattr(args, 'log_level', 'INFO'))

    api = get_api_client(args.player_id)

    if args.util_type == 'find-fuel':
        print("=" * 70)
        print("FIND NEAREST FUEL STATION")
        print("=" * 70)

        # Get ship location
        ship = api.get_ship(args.ship)
        if not ship:
            print("❌ Failed to get ship status")
            return 1

        current_location = ship['nav']['waypointSymbol']
        system, _ = parse_waypoint_symbol(current_location)

        # Get current coordinates
        current_wp = api.get_waypoint(system, current_location)
        if not current_wp:
            print("❌ Failed to get waypoint data")
            return 1

        current_coords = {'x': current_wp['x'], 'y': current_wp['y']}

        print(f"Current location: {current_location} ({current_coords['x']}, {current_coords['y']})\n")

        # Get all marketplaces
        result = api.list_waypoints(system, limit=20, traits="MARKETPLACE")
        marketplaces = result.get('data', []) if result else []

        if not marketplaces:
            print("❌ No marketplaces found")
            return 1

        # Calculate distances
        fuel_stations = []
        for wp in marketplaces:
            wp_coords = {'x': wp['x'], 'y': wp['y']}
            distance = calculate_distance(current_coords, wp_coords)
            fuel_stations.append({
                'symbol': wp['symbol'],
                'type': wp['type'],
                'distance': distance
            })

        # Sort by distance
        fuel_stations.sort(key=lambda x: x['distance'])

        print("Nearest fuel stations:")
        print("-" * 70)
        for i, station in enumerate(fuel_stations[:10], 1):
            print(f"{i}. {station['symbol']:20} ({station['type']:20}) - {station['distance']:.0f} units")

        print("\n" + "=" * 70)
        return 0

    elif args.util_type == 'distance':
        print("=" * 70)
        print("CALCULATE DISTANCE")
        print("=" * 70)

        system1, wp1 = parse_waypoint_symbol(args.waypoint1)
        system2, wp2 = parse_waypoint_symbol(args.waypoint2)

        wp1_data = api.get_waypoint(system1, wp1)
        wp2_data = api.get_waypoint(system2, wp2)

        if not wp1_data or not wp2_data:
            print("❌ Failed to get waypoint data")
            return 1

        coords1 = {'x': wp1_data['x'], 'y': wp1_data['y']}
        coords2 = {'x': wp2_data['x'], 'y': wp2_data['y']}

        distance = calculate_distance(coords1, coords2)

        print(f"{args.waypoint1}: ({coords1['x']}, {coords1['y']})")
        print(f"{args.waypoint2}: ({coords2['x']}, {coords2['y']})")
        print(f"\nDistance: {distance:.1f} units")
        print(f"Fuel needed (CRUISE): ~{estimate_fuel_cost(distance, 'CRUISE')} units")
        print(f"Fuel needed (DRIFT): ~{estimate_fuel_cost(distance, 'DRIFT')} units")

        print("\n" + "=" * 70)
        return 0

    elif args.util_type == 'find-mining':
        print("=" * 70)
        print("FIND MINING OPPORTUNITIES")
        print("=" * 70)

        # Get system from token
        system = args.system if hasattr(args, 'system') else None
        if not system:
            print("❌ System required (--system SYSTEM)")
            return 1

        # Get ship specs if provided
        ship_specs = None
        if hasattr(args, 'ship') and args.ship:
            ship_data = api.get_ship(args.ship)
            if ship_data:
                ship_specs = {
                    'symbol': args.ship,
                    'speed': ship_data['engine']['speed'],
                    'fuel_capacity': ship_data['fuel']['capacity'],
                    'cargo_capacity': ship_data['cargo']['capacity']
                }
                print(f"Ship: {args.ship}")
                print(f"  Speed: {ship_specs['speed']}")
                print(f"  Fuel: {ship_specs['fuel_capacity']}")
                print(f"  Cargo: {ship_specs['cargo_capacity']}\n")

        if not ship_specs:
            # Default to mining drone specs (most common mining ship)
            ship_specs = {
                'symbol': 'DEFAULT_MINING_DRONE',
                'speed': 9,
                'fuel_capacity': 80,
                'cargo_capacity': 15
            }
            print(f"Using default mining drone specs (speed: 9, fuel: 80, cargo: 15)\n")

        print(f"System: {system}\n")

        # Get all waypoints in system (paginated)
        waypoints = []
        page = 1
        while True:
            result = api.get(f'/systems/{system}/waypoints?limit=20&page={page}')
            if not result or 'data' not in result:
                break

            page_data = result.get('data', [])
            if not page_data:
                break

            waypoints.extend(page_data)

            # Check if there are more pages
            meta = result.get('meta', {})
            if page >= meta.get('total', 1):
                break

            page += 1
            if page > 50:  # Safety limit
                break

        # Filter for asteroids with good mining traits
        GOOD_TRAITS = {'COMMON_METAL_DEPOSITS', 'PRECIOUS_METAL_DEPOSITS',
                      'RARE_METAL_DEPOSITS', 'MINERAL_DEPOSITS'}
        BAD_TRAITS = {'STRIPPED', 'UNSTABLE_COMPOSITION', 'EXPLOSIVE_GASES',
                     'RADIOACTIVE'}

        # Map traits to materials they produce
        TRAIT_TO_MATERIALS = {
            'COMMON_METAL_DEPOSITS': ['IRON_ORE', 'COPPER_ORE', 'ALUMINUM_ORE'],
            'PRECIOUS_METAL_DEPOSITS': ['SILVER_ORE', 'GOLD_ORE', 'PLATINUM_ORE'],
            'RARE_METAL_DEPOSITS': ['URANITE_ORE', 'MERITIUM_ORE'],
            'MINERAL_DEPOSITS': ['SILICON_CRYSTALS', 'QUARTZ_SAND', 'ICE_WATER']
        }

        mining_asteroids = []
        for wp in waypoints:
            if wp['type'] != 'ASTEROID':
                continue

            traits = {t['symbol'] for t in wp.get('traits', [])}

            # Skip if has bad traits
            if traits & BAD_TRAITS:
                continue

            # Only include if has good mining traits
            if traits & GOOD_TRAITS:
                # Determine possible materials from traits
                possible_materials = []
                for trait in traits & GOOD_TRAITS:
                    possible_materials.extend(TRAIT_TO_MATERIALS.get(trait, []))

                mining_asteroids.append({
                    'symbol': wp['symbol'],
                    'coords': {'x': wp['x'], 'y': wp['y']},
                    'traits': traits,
                    'materials': possible_materials
                })

        print(f"Found {len(mining_asteroids)} suitable mining asteroids\n")

        if not mining_asteroids:
            print("❌ No suitable mining asteroids found")
            return 1

        # Get database connection for market queries
        db = get_database()

        # Initialize SmartNavigator for accurate routing
        from spacetraders_bot.core.smart_navigator import SmartNavigator
        try:
            navigator = SmartNavigator(api, system)
            use_navigator = True
            print("✅ SmartNavigator initialized for accurate fuel routing\n")
        except Exception as e:
            print(f"⚠️  SmartNavigator unavailable, using distance estimates: {e}\n")
            use_navigator = False

        # Find best opportunities
        opportunities = []

        # Mining constants based on ship specs
        cargo_capacity = ship_specs['cargo_capacity']
        fuel_capacity = ship_specs['fuel_capacity']

        # Mining time scales with cargo capacity
        # Assume ~4 units per extraction, 80s cooldown
        extractions_needed = cargo_capacity / 4
        MINING_TIME = extractions_needed * 80  # seconds
        DOCK_SELL_TIME = 60  # 1 minute for docking and selling

        for asteroid in mining_asteroids:
            # Find best market for ANY material this asteroid produces
            best_market = None
            best_material = None
            max_price = 0

            for material in asteroid['materials']:
                # Query database for markets buying this material
                with db.connection() as conn:
                    if system:
                        query = """
                            SELECT waypoint_symbol, sell_price, supply, trade_volume, last_updated
                            FROM market_data
                            WHERE good_symbol = ?
                              AND waypoint_symbol LIKE ?
                              AND sell_price > 0
                            ORDER BY sell_price DESC
                        """
                        rows = conn.execute(query, (material, f"{system}-%")).fetchall()
                    else:
                        query = """
                            SELECT waypoint_symbol, sell_price, supply, trade_volume, last_updated
                            FROM market_data
                            WHERE good_symbol = ?
                              AND sell_price > 0
                            ORDER BY sell_price DESC
                        """
                        rows = conn.execute(query, (material,)).fetchall()

                    markets = [{
                        "waypoint": row["waypoint_symbol"],
                        "price": row["sell_price"],
                        "supply": row["supply"] or "UNKNOWN",
                        "tradeVolume": row["trade_volume"] or 0,
                        "last_updated_at": row["last_updated"]
                    } for row in rows]

                for market in markets:
                    # Get market coordinates
                    market_coords = None
                    for wp in waypoints:
                        if wp['symbol'] == market['waypoint']:
                            market_coords = {'x': wp['x'], 'y': wp['y']}
                            break

                    if not market_coords:
                        continue

                    distance = calculate_distance(asteroid['coords'], market_coords)

                    # Track best price among all materials
                    if market['price'] > max_price:
                        max_price = market['price']
                        best_market = {
                            'waypoint': market['waypoint'],
                            'price': market['price'],
                            'distance': distance
                        }
                        best_material = material

            if best_market:
                distance = best_market['distance']

                if use_navigator:
                    # Use SmartNavigator for accurate routing with refuel stops
                    # Create mock ship data for route planning
                    mock_ship = {
                        'nav': {'waypointSymbol': asteroid['symbol']},
                        'fuel': {'current': fuel_capacity, 'capacity': fuel_capacity},
                        'engine': {'speed': ship_specs['speed']},
                        'cargo': {'capacity': cargo_capacity}
                    }

                    try:
                        # Plan route: asteroid → market (prefer DRIFT)
                        outbound_route = navigator.plan_route(mock_ship, best_market['waypoint'])

                        # Plan return: market → asteroid (after refueling at market)
                        mock_ship['nav']['waypointSymbol'] = best_market['waypoint']
                        mock_ship['fuel']['current'] = fuel_capacity  # Assume refuel at market
                        return_route = navigator.plan_route(mock_ship, asteroid['symbol'])

                        if outbound_route and return_route:
                            # Use actual route times and fuel costs
                            outbound_time = outbound_route.get('total_time', 0)
                            return_time = return_route.get('total_time', 0)
                            round_trip_time = outbound_time + return_time

                            # Calculate fuel cost based on starting fuel vs final fuel
                            outbound_fuel_used = fuel_capacity - outbound_route.get('final_fuel', fuel_capacity)
                            return_fuel_used = fuel_capacity - return_route.get('final_fuel', fuel_capacity)
                            total_fuel_needed = outbound_fuel_used + return_fuel_used

                            # Count refuel stops from steps
                            refuel_stops = sum(1 for step in outbound_route.get('steps', []) if step.get('action') == 'refuel')
                            refuel_stops += sum(1 for step in return_route.get('steps', []) if step.get('action') == 'refuel')

                            cycle_time_seconds = MINING_TIME + round_trip_time + DOCK_SELL_TIME
                            fuel_cost = total_fuel_needed * 1  # 1 credit per fuel (conservative)
                        else:
                            # Fallback to distance estimate if routing fails
                            raise Exception("Route planning failed")
                    except Exception as e:
                        # Fallback to distance-based calculation
                        use_navigator = False

                if not use_navigator:
                    # Fallback: Simple distance-based calculation (DRIFT mode)
                    base_speed = 9
                    base_time_per_unit = 27.8
                    time_per_unit = base_time_per_unit * (base_speed / ship_specs['speed'])

                    one_way_time = distance * time_per_unit
                    round_trip_time = one_way_time * 2

                    cycle_time_seconds = MINING_TIME + round_trip_time + DOCK_SELL_TIME
                    fuel_cost = 2  # Conservative DRIFT estimate

                cycle_time_minutes = cycle_time_seconds / 60

                # Calculate profit
                revenue = best_market['price'] * cargo_capacity
                net_profit = revenue - fuel_cost

                # Profit per hour
                cycles_per_hour = 60 / cycle_time_minutes
                profit_per_hour = net_profit * cycles_per_hour

                opportunities.append({
                    'asteroid': asteroid['symbol'],
                    'material': best_material,
                    'market': best_market['waypoint'],
                    'distance': round(distance, 1),
                    'price': best_market['price'],
                    'revenue': int(revenue),
                    'fuel_cost': int(fuel_cost),
                    'net_profit': int(net_profit),
                    'cycle_time': round(cycle_time_minutes, 1),
                    'profit_per_hour': int(profit_per_hour),
                    'traits': asteroid['traits']
                })

        # Sort by profit per hour
        opportunities.sort(key=lambda x: x['profit_per_hour'], reverse=True)

        # Display top 10
        print("\nTOP 10 MINING OPPORTUNITIES (by profit/hour):")
        print("=" * 80)

        for i, opp in enumerate(opportunities[:10], 1):
            trait_str = ', '.join(sorted(opp['traits']))
            mining_time_min = MINING_TIME / 60
            travel_time_min = round((opp['cycle_time'] - mining_time_min - 1), 1)  # Subtract mining + dock time

            print(f"\n{i}. {opp['asteroid']}")
            print(f"   Traits: {trait_str}")
            print(f"   Best Material: {opp['material']} → {opp['market']} ({opp['distance']} units)")
            print(f"   Sell Price: {opp['price']} cr/unit")
            print(f"   Cycle: {opp['cycle_time']:.1f} min (mine {mining_time_min:.1f}m + travel {travel_time_min:.1f}m)")
            print(f"   Profit/Trip: {opp['net_profit']:,} cr (revenue {opp['revenue']:,} - fuel {opp['fuel_cost']:,})")
            print(f"   💰 Profit/Hour: {opp['profit_per_hour']:,} cr/hr")

        print("\n" + "=" * 70)
        return 0

    return 1
