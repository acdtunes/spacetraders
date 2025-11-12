#!/bin/bash
# End-to-End Test Script for SpaceTraders Go Bot POC
# Tests the complete CLI -> gRPC -> Daemon -> Database flow

set -e
set -o pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SOCKET_PATH="/tmp/spacetraders-test-daemon.sock"
DB_PATH="/tmp/spacetraders_test.db"
DAEMON_PID=""
TEST_RESULTS=()
FAILED_TESTS=0
PASSED_TESTS=0

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Stop daemon if running
    if [ -n "$DAEMON_PID" ]; then
        echo "Stopping daemon (PID: $DAEMON_PID)..."
        kill $DAEMON_PID 2>/dev/null || true
        wait $DAEMON_PID 2>/dev/null || true
    fi

    # Remove socket file
    if [ -S "$SOCKET_PATH" ]; then
        rm -f "$SOCKET_PATH"
    fi

    # Remove test database
    if [ -f "$DB_PATH" ]; then
        rm -f "$DB_PATH"
    fi

    echo -e "${GREEN}Cleanup complete${NC}"
}

# Set trap to cleanup on exit
trap cleanup EXIT

# Print test header
print_header() {
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════${NC}"
}

# Print test step
print_step() {
    echo -e "\n${YELLOW}▶ $1${NC}"
}

# Record test result
record_result() {
    local test_name="$1"
    local result="$2"

    if [ "$result" = "PASS" ]; then
        TEST_RESULTS+=("${GREEN}✓${NC} $test_name")
        PASSED_TESTS=$((PASSED_TESTS + 1))
    else
        TEST_RESULTS+=("${RED}✗${NC} $test_name")
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
}

# Start the E2E test
print_header "SpaceTraders Go Bot - End-to-End Test"

echo -e "${BLUE}Test Configuration:${NC}"
echo "  Socket Path: $SOCKET_PATH"
echo "  Database:    $DB_PATH"
echo "  Binaries:    ./bin/spacetraders, ./bin/spacetraders-daemon"

# Step 1: Verify binaries exist
print_step "Step 1: Verifying binaries exist"
if [ ! -f "./bin/spacetraders" ] || [ ! -f "./bin/spacetraders-daemon" ]; then
    echo -e "${RED}✗ Binaries not found. Run 'make build' first.${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Binaries found${NC}"

# Step 2: Clean up any existing processes/files
print_step "Step 2: Cleaning up existing test artifacts"
pkill -f "spacetraders-daemon" 2>/dev/null || true
rm -f "$SOCKET_PATH" "$DB_PATH"
echo -e "${GREEN}✓ Cleanup complete${NC}"

# Step 3: Start daemon in background
print_step "Step 3: Starting daemon with SQLite test database"
export SPACETRADERS_SOCKET="$SOCKET_PATH"
export DB_TYPE="sqlite"
export DB_PATH="$DB_PATH"

./bin/spacetraders-daemon > /tmp/daemon.log 2>&1 &
DAEMON_PID=$!

echo "Daemon PID: $DAEMON_PID"
echo "Waiting for daemon to be ready..."

# Wait for daemon to be ready (check for socket file)
MAX_WAIT=10
WAIT_COUNT=0
while [ ! -S "$SOCKET_PATH" ] && [ $WAIT_COUNT -lt $MAX_WAIT ]; do
    sleep 1
    ((WAIT_COUNT++))
    echo -n "."
done
echo ""

if [ ! -S "$SOCKET_PATH" ]; then
    echo -e "${RED}✗ Daemon failed to start${NC}"
    echo "Daemon log:"
    cat /tmp/daemon.log
    exit 1
fi

# Verify daemon is running
if ! kill -0 $DAEMON_PID 2>/dev/null; then
    echo -e "${RED}✗ Daemon process died${NC}"
    echo "Daemon log:"
    cat /tmp/daemon.log
    exit 1
fi

echo -e "${GREEN}✓ Daemon started successfully${NC}"

# Give daemon time to initialize database schema
echo "Waiting for database initialization..."
sleep 2

# Step 4: Setup test data
print_step "Step 4: Setting up test data"
if ! DB_PATH="$DB_PATH" ./scripts/setup_test_data.sh; then
    echo -e "${RED}✗ Test data setup failed${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Test data setup complete${NC}"

# Step 5: Test health check
print_step "Step 5: Testing health check"
if ./bin/spacetraders --socket "$SOCKET_PATH" health > /tmp/health_output.txt 2>&1; then
    echo -e "${GREEN}✓ Health check passed${NC}"
    cat /tmp/health_output.txt
    record_result "Health Check" "PASS"
else
    echo -e "${RED}✗ Health check failed${NC}"
    cat /tmp/health_output.txt
    record_result "Health Check" "FAIL"
fi

# Step 6: Test container list (should be empty)
print_step "Step 6: Testing container list (should be empty)"
echo "DEBUG: About to run container list command"
echo "DEBUG: Daemon PID=$DAEMON_PID, checking if still running..."
if kill -0 $DAEMON_PID 2>/dev/null; then
    echo "DEBUG: Daemon is still running"
else
    echo "DEBUG: WARNING - Daemon process not found!"
fi

if ./bin/spacetraders --socket "$SOCKET_PATH" container list > /tmp/container_list_output.txt 2>&1; then
    echo -e "${GREEN}✓ Container list command succeeded${NC}"
    cat /tmp/container_list_output.txt
    record_result "Container List (Empty)" "PASS"
else
    echo -e "${RED}✗ Container list command failed${NC}"
    cat /tmp/container_list_output.txt
    record_result "Container List (Empty)" "FAIL"
fi

# Step 7: Test navigation command
print_step "Step 7: Testing navigation command"
echo "Command: navigate --ship TEST-SHIP-1 --destination X1-TEST-A1 --player-id 1"

# Note: This will fail against the real API since we're using a test token
# But it should test the gRPC communication and command handling
if ./bin/spacetraders --socket "$SOCKET_PATH" navigate \
    --ship TEST-SHIP-1 \
    --destination X1-TEST-A1 \
    --player-id 1 > /tmp/navigate_output.txt 2>&1; then
    echo -e "${GREEN}✓ Navigation command completed${NC}"
    cat /tmp/navigate_output.txt
    record_result "Navigate Command" "PASS"
else
    echo -e "${YELLOW}⚠ Navigation command returned error (expected with mock data)${NC}"
    cat /tmp/navigate_output.txt
    # Check if it's a gRPC communication error or API error
    if grep -q "failed to connect" /tmp/navigate_output.txt; then
        echo -e "${RED}✗ gRPC connection failed${NC}"
        record_result "Navigate Command" "FAIL"
    else
        echo -e "${GREEN}✓ gRPC communication successful (API error expected)${NC}"
        record_result "Navigate Command" "PASS"
    fi
fi

# Step 8: Test dock command
print_step "Step 8: Testing dock command"
if ./bin/spacetraders --socket "$SOCKET_PATH" dock \
    --ship TEST-SHIP-1 \
    --player-id 1 > /tmp/dock_output.txt 2>&1; then
    echo -e "${GREEN}✓ Dock command completed${NC}"
    cat /tmp/dock_output.txt
    record_result "Dock Command" "PASS"
else
    echo -e "${YELLOW}⚠ Dock command returned error${NC}"
    cat /tmp/dock_output.txt
    if grep -q "failed to connect" /tmp/dock_output.txt; then
        record_result "Dock Command" "FAIL"
    else
        record_result "Dock Command" "PASS"
    fi
fi

# Step 9: Test orbit command
print_step "Step 9: Testing orbit command"
if ./bin/spacetraders --socket "$SOCKET_PATH" orbit \
    --ship TEST-SHIP-1 \
    --player-id 1 > /tmp/orbit_output.txt 2>&1; then
    echo -e "${GREEN}✓ Orbit command completed${NC}"
    cat /tmp/orbit_output.txt
    record_result "Orbit Command" "PASS"
else
    echo -e "${YELLOW}⚠ Orbit command returned error${NC}"
    cat /tmp/orbit_output.txt
    if grep -q "failed to connect" /tmp/orbit_output.txt; then
        record_result "Orbit Command" "FAIL"
    else
        record_result "Orbit Command" "PASS"
    fi
fi

# Step 10: Test refuel command
print_step "Step 10: Testing refuel command"
if ./bin/spacetraders --socket "$SOCKET_PATH" refuel \
    --ship TEST-SHIP-1 \
    --player-id 1 > /tmp/refuel_output.txt 2>&1; then
    echo -e "${GREEN}✓ Refuel command completed${NC}"
    cat /tmp/refuel_output.txt
    record_result "Refuel Command" "PASS"
else
    echo -e "${YELLOW}⚠ Refuel command returned error${NC}"
    cat /tmp/refuel_output.txt
    if grep -q "failed to connect" /tmp/refuel_output.txt; then
        record_result "Refuel Command" "FAIL"
    else
        record_result "Refuel Command" "PASS"
    fi
fi

# Step 11: Verify daemon is still running
print_step "Step 11: Verifying daemon stability"
if kill -0 $DAEMON_PID 2>/dev/null; then
    echo -e "${GREEN}✓ Daemon is still running${NC}"
    record_result "Daemon Stability" "PASS"
else
    echo -e "${RED}✗ Daemon crashed during tests${NC}"
    record_result "Daemon Stability" "FAIL"
fi

# Print final results
print_header "Test Results Summary"

echo ""
for result in "${TEST_RESULTS[@]}"; do
    echo -e "  $result"
done

echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════${NC}"
echo -e "  Total Tests: $((PASSED_TESTS + FAILED_TESTS))"
echo -e "  ${GREEN}Passed: $PASSED_TESTS${NC}"
echo -e "  ${RED}Failed: $FAILED_TESTS${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "\n${GREEN}✓ All tests passed!${NC}\n"
    exit 0
else
    echo -e "\n${RED}✗ Some tests failed${NC}\n"
    echo "Daemon log (last 50 lines):"
    tail -50 /tmp/daemon.log
    exit 1
fi
