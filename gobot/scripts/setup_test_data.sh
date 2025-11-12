#!/bin/bash
# Setup test data for E2E testing
# This script populates the database with minimal test data for navigation testing

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Get database path from environment or use default
DB_PATH="${DB_PATH:-/tmp/spacetraders_test.db}"

echo -e "${GREEN}Setting up test data in database: ${DB_PATH}${NC}"

# Check if sqlite3 is installed
if ! command -v sqlite3 &> /dev/null; then
    echo -e "${RED}Error: sqlite3 is not installed${NC}"
    echo "Install with: brew install sqlite (macOS) or apt-get install sqlite3 (Linux)"
    exit 1
fi

# Create SQL commands for test data
cat > /tmp/test_data.sql <<'EOF'
-- Insert test player
INSERT OR REPLACE INTO players (player_id, agent_symbol, token, created_at, credits)
VALUES (1, 'TEST_AGENT', 'test_token_12345', datetime('now'), 100000);

-- Insert test system waypoints (X1-TEST system)
-- Headquarters with fuel
INSERT OR REPLACE INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
VALUES ('X1-TEST-HQ', 'X1-TEST', 'PLANET', 0.0, 0.0, '["MARKETPLACE","SHIPYARD"]', 1, '[]', datetime('now'));

-- Destination waypoint
INSERT OR REPLACE INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
VALUES ('X1-TEST-A1', 'X1-TEST', 'ASTEROID', 20.0, 15.0, '["COMMON_METAL_DEPOSITS"]', 0, '[]', datetime('now'));

-- Another destination waypoint
INSERT OR REPLACE INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
VALUES ('X1-TEST-B2', 'X1-TEST', 'ASTEROID', -10.0, 25.0, '["PRECIOUS_METAL_DEPOSITS"]', 0, '[]', datetime('now'));

-- Fuel station waypoint
INSERT OR REPLACE INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
VALUES ('X1-TEST-FUEL', 'X1-TEST', 'ORBITAL_STATION', 10.0, 5.0, '["MARKETPLACE"]', 1, '[]', datetime('now'));

-- Far waypoint (requires refuel)
INSERT OR REPLACE INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
VALUES ('X1-TEST-FAR', 'X1-TEST', 'PLANET', 100.0, 100.0, '["STRIPPED"]', 0, '[]', datetime('now'));

EOF

# Execute SQL
echo -e "${YELLOW}Inserting test data...${NC}"
sqlite3 "$DB_PATH" < /tmp/test_data.sql

# Verify data was inserted
echo -e "${YELLOW}Verifying test data...${NC}"
PLAYER_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM players WHERE agent_symbol='TEST_AGENT';")
WAYPOINT_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM waypoints WHERE system_symbol='X1-TEST';")

if [ "$PLAYER_COUNT" -eq "1" ] && [ "$WAYPOINT_COUNT" -ge "5" ]; then
    echo -e "${GREEN}✓ Test data setup complete${NC}"
    echo -e "  Player: TEST_AGENT (ID: 1)"
    echo -e "  Waypoints in X1-TEST system: $WAYPOINT_COUNT"
    echo ""
    echo "Test waypoints:"
    sqlite3 "$DB_PATH" "SELECT waypoint_symbol, type, x, y, has_fuel FROM waypoints WHERE system_symbol='X1-TEST';" | column -t -s '|'
    exit 0
else
    echo -e "${RED}✗ Test data setup failed${NC}"
    echo -e "  Expected: 1 player, 5 waypoints"
    echo -e "  Got: $PLAYER_COUNT players, $WAYPOINT_COUNT waypoints"
    exit 1
fi
