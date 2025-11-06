#!/usr/bin/env python3
"""
Emergency database query script to extract marketplace waypoints from X1-HZ85.
"""
import sqlite3
import sys

db_path = "/Users/andres.camacho/Development/Personal/spacetraders/bot/var/spacetraders.db"

try:
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()

    # First, check what players exist
    print("=== REGISTERED PLAYERS ===")
    cursor.execute("SELECT player_id, agent_symbol, credits FROM players")
    players = cursor.fetchall()
    if players:
        for player_id, agent, credits in players:
            print(f"  Player {player_id}: {agent} ({credits:,} credits)")
    else:
        print("  No players found in database")

    print("\n=== X1-HZ85 WAYPOINTS ===")
    # Query waypoints in X1-HZ85 with MARKETPLACE trait
    cursor.execute("""
        SELECT waypoint_symbol, type, traits, has_fuel
        FROM waypoints
        WHERE system_symbol = 'X1-HZ85'
        ORDER BY waypoint_symbol
    """)

    waypoints = cursor.fetchall()

    if waypoints:
        print(f"Found {len(waypoints)} waypoints in X1-HZ85:")
        marketplaces = []
        for symbol, wtype, traits, fuel in waypoints:
            fuel_marker = "⛽" if fuel else "  "
            print(f"  {fuel_marker} {symbol} ({wtype})")
            if traits and 'MARKETPLACE' in traits:
                marketplaces.append(symbol)
                print(f"     -> HAS MARKETPLACE")

        if marketplaces:
            print(f"\n=== MARKETPLACE WAYPOINTS ({len(marketplaces)}) ===")
            print(",".join(marketplaces))
        else:
            print("\nNo marketplaces found in waypoint data.")
    else:
        print("No waypoints found for X1-HZ85 in database.")
        print("\nSearching for ANY waypoints in database...")
        cursor.execute("SELECT DISTINCT system_symbol FROM waypoints LIMIT 10")
        systems = cursor.fetchall()
        if systems:
            print("Found waypoints in these systems:")
            for (system,) in systems:
                print(f"  - {system}")
        else:
            print("Waypoints table is completely empty.")

    # Check for market data
    print("\n=== MARKET DATA IN X1-HZ85 ===")
    cursor.execute("""
        SELECT DISTINCT waypoint_symbol
        FROM market_data
        WHERE waypoint_symbol LIKE 'X1-HZ85-%'
    """)
    markets = cursor.fetchall()
    if markets:
        print(f"Found {len(markets)} waypoints with market data:")
        market_list = [m[0] for m in markets]
        for m in market_list:
            print(f"  - {m}")
        print(f"\nComma-separated: {','.join(market_list)}")
    else:
        print("No market data found for X1-HZ85.")

    conn.close()

except Exception as e:
    print(f"Error querying database: {e}")
    import traceback
    traceback.print_exc()
    sys.exit(1)
