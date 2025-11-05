#!/bin/bash

# Test runner for SpaceTraders bot
# Supports running unit tests, integration tests, or all tests with various options

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Default values
TEST_TYPE="all"
VERBOSE=""
COVERAGE=""
FAILFAST=""
MAX_FAIL=""
MARKERS=""

# Help message
show_help() {
    cat << EOF
Usage: ./run_tests.sh [OPTIONS]

Run tests for the SpaceTraders bot project.

OPTIONS:
    --unit              Run only unit tests (BDD tests: domain, application, shared)
    --integration       Run only integration tests (CLI, daemon, persistence, routing)
    --all               Run all tests (default)
    -v, --verbose       Run tests in verbose mode
    -c, --coverage      Run tests with coverage report
    -x, --failfast      Stop on first failure
    --maxfail N         Stop after N failures
    -m, --markers EXPR  Run tests matching marker expression (e.g. "not slow")
    -h, --help          Show this help message

EXAMPLES:
    ./run_tests.sh --unit -v
    ./run_tests.sh --integration --coverage
    ./run_tests.sh --all -x
    ./run_tests.sh --maxfail 5
    ./run_tests.sh -m "not slow"

TEST CATEGORIES:
    Unit Tests: Domain logic, application handlers, shared utilities
    Integration Tests: CLI, daemon, database persistence, routing engine
EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --unit)
            TEST_TYPE="unit"
            shift
            ;;
        --integration)
            TEST_TYPE="integration"
            shift
            ;;
        --all)
            TEST_TYPE="all"
            shift
            ;;
        -v|--verbose)
            VERBOSE="-v"
            shift
            ;;
        -c|--coverage)
            COVERAGE="--cov=src/spacetraders --cov-report=html --cov-report=term"
            shift
            ;;
        -x|--failfast)
            FAILFAST="-x"
            shift
            ;;
        --maxfail)
            MAX_FAIL="--maxfail=$2"
            shift 2
            ;;
        -m|--markers)
            MARKERS="-m $2"
            shift 2
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            show_help
            exit 1
            ;;
    esac
done

# Get script directory and navigate to project root
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR" || exit 1

# Build pytest command
PYTEST_CMD="uv run pytest"

# Determine test paths based on test type
case $TEST_TYPE in
    unit)
        echo -e "${YELLOW}Running unit tests...${NC}"
        TEST_PATH="tests/bdd/steps/domain/ tests/bdd/steps/application/ tests/bdd/steps/shared/"
        ;;
    integration)
        echo -e "${YELLOW}Running integration tests...${NC}"
        TEST_PATH="tests/integration/ tests/bdd/steps/infrastructure/ tests/bdd/steps/integration/ tests/bdd/steps/daemon/ tests/bdd/steps/navigation/"
        ;;
    all)
        echo -e "${YELLOW}Running all tests...${NC}"
        TEST_PATH="tests/"
        ;;
esac

# Build full command
FULL_CMD="$PYTEST_CMD $TEST_PATH $VERBOSE $COVERAGE $FAILFAST $MAX_FAIL $MARKERS"

# Print command being run
echo -e "${GREEN}Command:${NC} $FULL_CMD"
echo ""

# Run tests
eval $FULL_CMD

# Capture exit code
EXIT_CODE=$?

# Print summary
echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ Tests passed${NC}"
else
    echo -e "${RED}✗ Tests failed${NC}"
fi

exit $EXIT_CODE
