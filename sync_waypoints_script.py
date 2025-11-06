#!/usr/bin/env python3
"""
Quick script to sync system waypoints for X1-HZ85
"""
import asyncio
import sys
import os
from pathlib import Path

# Add bot src to path
sys.path.insert(0, '/Users/andres.camacho/Development/Personal/spacetraders/bot/src')

# CRITICAL: Override database path to use MCP server's database
from configuration.settings import settings
settings.db_path = Path('/Users/andres.camacho/Development/Personal/spacetraders/var/spacetraders.db')

from configuration.container import get_mediator
from application.shipyard.commands.sync_waypoints import SyncSystemWaypointsCommand

async def main():
    """Sync waypoints for X1-HZ85 system"""
    print("Syncing waypoints for system X1-HZ85...")

    mediator = get_mediator()
    command = SyncSystemWaypointsCommand(
        system_symbol="X1-HZ85",
        player_id=1  # ENDURANCE
    )

    await mediator.send_async(command)

    print("✅ Waypoints synced successfully!")
    print("\nQuerying for marketplaces...")

    # Query the database to show what we found
    import sqlite3
    db_path = '/Users/andres.camacho/Development/Personal/spacetraders/bot/var/spacetraders.db'
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()

    # Get all waypoints with MARKETPLACE trait
    cursor.execute("""
        SELECT symbol, type, traits
        FROM waypoints
        WHERE system_symbol = 'X1-HZ85'
        AND traits LIKE '%MARKETPLACE%'
        ORDER BY symbol
    """)

    marketplaces = cursor.fetchall()

    print(f"\n=== MARKETPLACES IN X1-HZ85 ({len(marketplaces)} found) ===")
    market_symbols = []
    for symbol, wtype, traits in marketplaces:
        print(f"  - {symbol} ({wtype})")
        market_symbols.append(symbol)

    print("\n=== COMMA-SEPARATED LIST ===")
    print(",".join(market_symbols))

    conn.close()

if __name__ == "__main__":
    asyncio.run(main())
