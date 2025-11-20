#!/bin/bash

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Parse command line arguments
ENABLE_RACE=false
ENABLE_COVER=false
for arg in "$@"; do
    case $arg in
        --race)
            ENABLE_RACE=true
            shift
            ;;
        --cover)
            ENABLE_COVER=true
            shift
            ;;
        --parallel=*)
            FORCE_PARALLEL="${arg#*=}"
            shift
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --race          Enable race detection (slower, more thorough)"
            echo "  --cover         Enable coverage reporting"
            echo "  --parallel=N    Force N parallel workers (default: auto-detect)"
            echo "  --help          Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                    # Fast mode (no race/cover, ~18s)"
            echo "  $0 --race             # With race detection (~22s)"
            echo "  $0 --race --cover     # Full checks (~31s)"
            echo ""
            exit 0
            ;;
    esac
done

# Display header
echo -e "${CYAN}${BOLD}"
echo "════════════════════════════════════════════════════════════"
echo "    SpaceTraders Go Bot - Test Runner"
echo "════════════════════════════════════════════════════════════"
echo -e "${NC}"

# Temporary file for test output
TEMP_OUTPUT=$(mktemp)

# Detect CPU cores for optimal parallelization
if [ -n "$FORCE_PARALLEL" ]; then
    PARALLEL_COUNT=$FORCE_PARALLEL
    CPU_CORES="(forced)"
else
    CPU_CORES=$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo "4")
    # Use 80% of cores for parallel tests (leave headroom for system)
    PARALLEL_COUNT=$(( CPU_CORES * 4 / 5 ))
    # Ensure at least 2, max 8
    PARALLEL_COUNT=$(( PARALLEL_COUNT < 2 ? 2 : PARALLEL_COUNT ))
    PARALLEL_COUNT=$(( PARALLEL_COUNT > 8 ? 8 : PARALLEL_COUNT ))
fi

# Build test flags
TEST_FLAGS="-v -parallel=${PARALLEL_COUNT}"
if [ "$ENABLE_RACE" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -race"
fi
if [ "$ENABLE_COVER" = true ]; then
    TEST_FLAGS="$TEST_FLAGS -cover"
fi

# Run tests and capture output (filter LC_DYSYMTAB warning)
echo -e "${BLUE}Running tests with ${PARALLEL_COUNT} parallel workers (${CPU_CORES} cores detected)...${NC}"
if [ "$ENABLE_RACE" = true ]; then
    echo -e "${BLUE}  • Race detection: enabled${NC}"
else
    echo -e "${GREEN}  • Race detection: DISABLED (faster)${NC}"
fi
if [ "$ENABLE_COVER" = true ]; then
    echo -e "${BLUE}  • Coverage reporting: enabled${NC}"
else
    echo -e "${GREEN}  • Coverage reporting: DISABLED (faster)${NC}"
fi
echo ""

START_TIME=$(date +%s)
go test $TEST_FLAGS ./... 2>&1 | grep -v "LC_DYSYMTAB" > "$TEMP_OUTPUT"
END_TIME=$(date +%s)
ELAPSED_TIME=$((END_TIME - START_TIME))

echo ""
echo -e "${CYAN}${BOLD}════════════════════════════════════════════════════════════${NC}"
echo -e "${WHITE}${BOLD}                    TEST SUMMARY${NC}"
echo -e "${CYAN}${BOLD}════════════════════════════════════════════════════════════${NC}\n"

# Count Go test functions (traditional tests)
GO_PASS=$(grep -c "\-\-\- PASS:" "$TEMP_OUTPUT" 2>/dev/null || echo "0")
GO_FAIL=$(grep -c "\-\-\- FAIL:" "$TEMP_OUTPUT" 2>/dev/null || echo "0")
GO_SKIP=$(grep -c "\-\-\- SKIP:" "$TEMP_OUTPUT" 2>/dev/null || echo "0")
WARNINGS=$(grep -c "warning:" "$TEMP_OUTPUT" 2>/dev/null || echo "0")

# Remove any newlines and ensure numeric
GO_PASS=$(echo "$GO_PASS" | tr -d '\n' | grep -o '[0-9]*' | head -1)
GO_FAIL=$(echo "$GO_FAIL" | tr -d '\n' | grep -o '[0-9]*' | head -1)
GO_SKIP=$(echo "$GO_SKIP" | tr -d '\n' | grep -o '[0-9]*' | head -1)
WARNINGS=$(echo "$WARNINGS" | tr -d '\n' | grep -o '[0-9]*' | head -1)

# Default to 0 if empty
GO_PASS=${GO_PASS:-0}
GO_FAIL=${GO_FAIL:-0}
GO_SKIP=${GO_SKIP:-0}
WARNINGS=${WARNINGS:-0}

# Count BDD scenarios (from godog output)
# Format: "534 scenarios (309 passed, 225 undefined)"
BDD_LINE=$(grep "scenarios (" "$TEMP_OUTPUT" | head -1)
if [ -n "$BDD_LINE" ]; then
    BDD_TOTAL=$(echo "$BDD_LINE" | grep -o '^[0-9]*' | head -1)
    BDD_PASSED=$(echo "$BDD_LINE" | grep -o '[0-9]* passed' | grep -o '[0-9]*' | head -1)
    BDD_FAILED=$(echo "$BDD_LINE" | grep -o '[0-9]* failed' | grep -o '[0-9]*' | head -1)
    BDD_UNDEFINED=$(echo "$BDD_LINE" | grep -o '[0-9]* undefined' | grep -o '[0-9]*' | head -1)
    BDD_PENDING=$(echo "$BDD_LINE" | grep -o '[0-9]* pending' | grep -o '[0-9]*' | head -1)

    # Default to 0 if not found
    BDD_TOTAL=${BDD_TOTAL:-0}
    BDD_PASSED=${BDD_PASSED:-0}
    BDD_FAILED=${BDD_FAILED:-0}
    BDD_UNDEFINED=${BDD_UNDEFINED:-0}
    BDD_PENDING=${BDD_PENDING:-0}
else
    BDD_TOTAL=0
    BDD_PASSED=0
    BDD_FAILED=0
    BDD_UNDEFINED=0
    BDD_PENDING=0
fi

# Calculate total tests (use BDD if available, otherwise Go)
if [ "$BDD_TOTAL" -gt 0 ]; then
    TOTAL_TESTS=$BDD_TOTAL
    TOTAL_PASS=$BDD_PASSED
    TOTAL_FAIL=$BDD_FAILED
else
    TOTAL_TESTS=$((GO_PASS + GO_FAIL + GO_SKIP))
    TOTAL_PASS=$GO_PASS
    TOTAL_FAIL=$GO_FAIL
fi

# Calculate pass rate
if [ "$TOTAL_TESTS" -gt 0 ]; then
    PASS_RATE=$(awk "BEGIN {printf \"%.1f\", ($TOTAL_PASS / $TOTAL_TESTS) * 100}")
else
    PASS_RATE="0.0"
fi

# Display counts with colors
if [ "$BDD_TOTAL" -gt 0 ]; then
    # BDD Scenarios only (cleaner, less confusing)
    echo -e "${GREEN}${BOLD}  ✓ Passed:${NC}      ${GREEN}${BDD_PASSED}${NC}"
    if [ "$BDD_FAILED" -gt 0 ]; then
        echo -e "${RED}${BOLD}  ✗ Failed:${NC}      ${RED}${BDD_FAILED}${NC}"
    fi
    if [ "$BDD_UNDEFINED" -gt 0 ]; then
        echo -e "${YELLOW}${BOLD}  ? Undefined:${NC}   ${YELLOW}${BDD_UNDEFINED}${NC}"
    fi
    if [ "$BDD_PENDING" -gt 0 ]; then
        echo -e "${CYAN}${BOLD}  ⊝ Pending:${NC}     ${CYAN}${BDD_PENDING}${NC}"
    fi
else
    # Traditional Go tests
    echo -e "${GREEN}${BOLD}✓ Passed:${NC}    ${GREEN}${TOTAL_PASS}${NC}"
    echo -e "${RED}${BOLD}✗ Failed:${NC}    ${RED}${TOTAL_FAIL}${NC}"
    if [ "$GO_SKIP" -gt 0 ]; then
        echo -e "${YELLOW}${BOLD}⊝ Skipped:${NC}   ${YELLOW}${GO_SKIP}${NC}"
    fi
fi

if [ "$WARNINGS" -gt 0 ]; then
    echo -e "${YELLOW}${BOLD}⚠ Warnings:${NC}  ${YELLOW}${WARNINGS}${NC}"
fi

echo -e "${WHITE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
if [ "$BDD_TOTAL" -gt 0 ]; then
    echo -e "${BOLD}Total Scenarios: ${TOTAL_TESTS}${NC}"
else
    echo -e "${BOLD}Total Tests: ${TOTAL_TESTS}${NC}"
fi
echo -e "${BOLD}Elapsed Time: ${ELAPSED_TIME}s${NC} (${PARALLEL_COUNT} parallel workers)"

# Display pass rate with color based on percentage
if (( $(echo "$PASS_RATE >= 90" | bc -l) )); then
    RATE_COLOR=$GREEN
elif (( $(echo "$PASS_RATE >= 70" | bc -l) )); then
    RATE_COLOR=$YELLOW
else
    RATE_COLOR=$RED
fi

echo -e "${BOLD}Pass Rate:   ${RATE_COLOR}${PASS_RATE}%${NC}\n"

echo -e "${CYAN}${BOLD}════════════════════════════════════════════════════════════${NC}\n"

# Final status
if [ "$TOTAL_FAIL" -eq 0 ]; then
    if [ "$BDD_TOTAL" -gt 0 ] && [ "$BDD_UNDEFINED" -gt 0 ]; then
        echo -e "${YELLOW}${BOLD}⚠️  TESTS PASSED (${BDD_UNDEFINED} undefined scenarios)${NC}"
        echo -e "${CYAN}Undefined scenarios have no step implementations yet${NC}\n"
        EXIT_CODE=0
    else
        echo -e "${GREEN}${BOLD}🎉 ALL TESTS PASSED! 🎉${NC}\n"
        EXIT_CODE=0
    fi
else
    echo -e "${RED}${BOLD}❌ SOME TESTS FAILED${NC}"
    echo -e "${YELLOW}Run with -v flag for detailed failure information${NC}\n"
    EXIT_CODE=1
fi

# Show failed test details if any
if [ "$TOTAL_FAIL" -gt 0 ]; then
    echo -e "${RED}${BOLD}Failed Tests:${NC}"
    grep "\-\-\- FAIL:" "$TEMP_OUTPUT" | sed 's/    \-\-\- FAIL:/  ✗/' | head -20

    if [ "$TOTAL_FAIL" -gt 20 ]; then
        echo -e "  ${YELLOW}... and $((TOTAL_FAIL - 20)) more${NC}"
    fi
    echo ""
fi

# Cleanup
rm "$TEMP_OUTPUT"

exit $EXIT_CODE
