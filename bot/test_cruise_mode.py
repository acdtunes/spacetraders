#!/usr/bin/env python3
"""Test script to verify CRUISE mode fix"""

import sys
import sqlite3
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent / "lib"))

from api_client import APIClient
from ship_controller import ShipController

# Get token from database
db_path = "data/spacetraders.db"
conn = sqlite3.connect(db_path)
cursor = conn.cursor()
cursor.execute("SELECT token FROM players WHERE player_id = 5")
row = cursor.fetchone()
if not row:
    print("ERROR: Player 5 not found in database")
    sys.exit(1)
token = row[0]
conn.close()

# Initialize API and ship
api = APIClient(token)
ship = ShipController(api, 'VOID_HUNTER-1')

# Get current status
status = ship.get_status()
print(f"Current location: {status['nav']['waypointSymbol']}")
print(f"Current fuel: {status['fuel']['current']}/{status['fuel']['capacity']} ({status['fuel']['current']/status['fuel']['capacity']*100:.1f}%)")
print()

# Navigate to a different location
destination = 'X1-JB26-A1'
print(f"Navigating to {destination}...")
ship.navigate(destination)
print()

# Get final status
final_status = ship.get_status()
print(f"Final location: {final_status['nav']['waypointSymbol']}")
print(f"Final fuel: {final_status['fuel']['current']}/{final_status['fuel']['capacity']} ({final_status['fuel']['current']/final_status['fuel']['capacity']*100:.1f}%)")
