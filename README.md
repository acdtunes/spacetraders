# SpaceTraders V2

SpaceTraders autonomous fleet management bot - Version 2 with clean architecture.

## Architecture

This project uses:
- **Hexagonal Architecture** (Ports & Adapters)
- **Domain-Driven Design** (DDD)
- **CQRS** (Command Query Responsibility Segregation)
- **BDD Testing** with pytest-bdd

## Phase 1: Walking Skeleton (COMPLETED)

The walking skeleton proves the architecture works end-to-end with a minimal vertical slice:
- Stub domain models (Waypoint, Route, RouteSegment)
- Stub query handler (PlanRouteHandler)
- CLI adapter
- BDD test

## Setup

### Prerequisites
- Python 3.12
- [uv](https://docs.astral.sh/uv/) - Fast Python package manager

### Installation

1. Install uv (if not already installed):
```bash
curl -LsSf https://astral.sh/uv/install.sh | sh
```

2. Create virtual environment and install dependencies:
```bash
uv sync
```

This will create a `.venv` directory and install all dependencies from `pyproject.toml`.

### Usage

#### CLI Command

Run the navigation command:
```bash
uv run ./spacetraders navigate --from X1-A1 --to X1-B2
```

Expected output:
```
✅ Planned route ROUTE-STUB-1
   From: X1-A1
   To: X1-B2
   Distance: 100.0 units
```

#### Run Tests

Run BDD tests:
```bash
uv run pytest tests/bdd/ -v
```

Or activate the environment first:
```bash
source .venv/bin/activate  # uv creates .venv by default
pytest tests/bdd/ -v
```

## Project Structure

```
bot-v2/
├── src/spacetraders/
│   ├── domain/                     # Pure business logic
│   │   ├── shared/                 # Shared kernel
│   │   │   └── value_objects.py    # Waypoint
│   │   └── navigation/             # Navigation bounded context
│   │       └── route.py            # Route, RouteSegment
│   │
│   ├── application/                # CQRS Handlers
│   │   └── navigation/
│   │       └── queries/
│   │           └── plan_route.py   # PlanRouteQuery, PlanRouteHandler
│   │
│   ├── adapters/
│   │   └── primary/
│   │       └── cli/
│   │           └── main.py         # CLI entry point
│   │
│   └── ports/                      # Interfaces (future)
│
├── tests/
│   ├── bdd/
│   │   ├── features/navigation/
│   │   │   └── route_planning.feature
│   │   └── steps/navigation/
│   │       └── test_route_planning_steps.py
│   └── conftest.py
│
├── config/                         # Configuration files
├── var/                            # Runtime data (databases, logs)
├── docs/                           # Documentation
├── pyproject.toml                  # Project metadata
└── README.md
```

## Next Steps (Phase 2)

See [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) for the full implementation plan.

Phase 2 will implement:
- Player registration with full CQRS
- Real domain models
- SQLite persistence
- pymediatr integration
