#!/usr/bin/env python3
"""Waypoint query operations - filter and search waypoints by criteria."""

import json

from spacetraders_bot.core.database import get_database
from spacetraders_bot.operations.common import get_api_client, setup_logging


def waypoint_query_operation(args):
    """
    Query waypoints from the database with filtering options

    Filters by:
    - System (required)
    - Waypoint type (PLANET, ASTEROID, MOON, etc.)
    - Has trait (SHIPYARD, MARKETPLACE, COMMON_METAL_DEPOSITS, etc.)
    - Exclude trait (RADIOACTIVE, EXPLOSIVE_GASES, STRIPPED, etc.)
    - Has fuel
    """
    ship_name = "waypoint_query"
    log_file = setup_logging(ship_name, ship_name, getattr(args, 'log_level', 'INFO'))

    # Get API client to ensure we have player authentication
    api = get_api_client(args.player_id)

    # Get database connection
    db = get_database()

    # Build filter description
    filter_parts = []
    if args.waypoint_type:
        filter_parts.append(f"type={args.waypoint_type}")
    if args.trait:
        filter_parts.append(f"trait={args.trait}")
    if args.exclude:
        filter_parts.append(f"exclude={args.exclude}")
    if args.has_fuel:
        filter_parts.append("has_fuel=true")

    filter_desc = ", ".join(filter_parts) if filter_parts else "none"

    # Print header
    print("=" * 70)
    print(f"WAYPOINT QUERY - {args.system}")
    print(f"Filter: {filter_desc}")
    print("=" * 70)
    print()

    # Build SQL query
    with db.connection() as conn:
        cursor = conn.cursor()

        # Base query
        query = """
            SELECT waypoint_symbol, type, x, y, traits, has_fuel, orbitals
            FROM waypoints
            WHERE system_symbol = ?
        """
        params = [args.system]

        # Add type filter
        if args.waypoint_type:
            query += " AND type = ?"
            params.append(args.waypoint_type)

        # Add has_fuel filter
        if args.has_fuel:
            query += " AND has_fuel = 1"

        # Add trait filter (JSON contains)
        if args.trait:
            # SQLite doesn't have native JSON array contains, so we use LIKE
            # This works because traits is stored as JSON array
            query += " AND traits LIKE ?"
            params.append(f'%"{args.trait}"%')

        # Add exclude filter (JSON does NOT contain)
        if args.exclude:
            exclude_traits = [t.strip() for t in args.exclude.split(',')]
            for exclude_trait in exclude_traits:
                query += " AND traits NOT LIKE ?"
                params.append(f'%"{exclude_trait}"%')

        # Order by waypoint symbol for consistent output
        query += " ORDER BY waypoint_symbol"

        # Execute query
        cursor.execute(query, params)
        rows = cursor.fetchall()

        # Process and display results
        if not rows:
            print("No waypoints found matching criteria")
            print()
            print("=" * 70)
            return 1

        # Display results
        for row in rows:
            waypoint_symbol = row['waypoint_symbol']
            waypoint_type = row['type']
            x = row['x']
            y = row['y']
            traits_json = row['traits']
            has_fuel = row['has_fuel']
            orbitals_json = row['orbitals']

            # Parse JSON fields
            traits = json.loads(traits_json) if traits_json else []
            orbitals = json.loads(orbitals_json) if orbitals_json else []

            # Format trait list
            trait_str = ', '.join(traits) if traits else 'none'

            # Print waypoint info
            coord_str = f"({int(x)}, {int(y)})"
            print(f"{waypoint_symbol:20} {waypoint_type:15} {coord_str:20} [{trait_str}]")

            # Show fuel availability if requested or if has fuel
            if args.has_fuel or has_fuel:
                fuel_status = "⛽ FUEL AVAILABLE" if has_fuel else ""
                if fuel_status:
                    print(f"  {fuel_status}")

            # Show orbitals if present (useful for 0-distance travel)
            if orbitals and len(orbitals) > 0:
                orbital_str = ', '.join(orbitals)
                print(f"  Orbitals: {orbital_str}")

        print()
        print(f"Total: {len(rows)} waypoints found")
        print("=" * 70)

    return 0
