# Tour Cache Quick Start Guide

## What is Tour Cache?

Database caching for tour optimization results. Avoids recalculating the same routes, providing ~10x speedup for repeated operations.

## Quick Examples

### Python API

```python
from lib.routing import GraphBuilder, TourOptimizer
from lib.api_client import APIClient

# Setup
api = APIClient(token="YOUR_TOKEN")
builder = GraphBuilder(api)
graph = builder.load_system_graph("X1-HU87")
ship_data = api.get_ship("SHIP-1")

# Plan tour with caching (recommended)
optimizer = TourOptimizer(graph, ship_data)
tour = optimizer.plan_tour(
    start="X1-HU87-A1",
    stops=["X1-HU87-B7", "X1-HU87-C3", "X1-HU87-D5"],
    current_fuel=ship_data['fuel']['current'],
    return_to_start=True,
    algorithm="2opt",      # or "greedy"
    use_cache=True         # Enable caching
)

# First run: Cache MISS → Calculates and saves
# Second run: Cache HIT → Instant result
```

### CLI Usage

```bash
# First scout: Calculates tour (slow)
python3 spacetraders_bot.py scout-markets \
  --player-id 1 \
  --ship SHIP-2 \
  --system X1-HU87 \
  --algorithm 2opt \
  --return-to-start

# Second scout: Uses cache (fast!)
python3 spacetraders_bot.py scout-markets \
  --player-id 1 \
  --ship SHIP-2 \
  --system X1-HU87 \
  --algorithm 2opt \
  --return-to-start
```

## Cache Management

### Clear Cache (All Systems)

```python
from lib.database import get_database

db = get_database()
with db.transaction() as conn:
    count = db.clear_tour_cache(conn)
    print(f"Cleared {count} cached tours")
```

### Clear Cache (Specific System)

```python
with db.transaction() as conn:
    count = db.clear_tour_cache(conn, system="X1-HU87")
    print(f"Cleared {count} tours for X1-HU87")
```

### View Cached Tours

```python
with db.connection() as conn:
    cursor = conn.cursor()
    cursor.execute("SELECT system, algorithm, calculated_at FROM tour_cache")
    for row in cursor.fetchall():
        print(f"{row['system']} - {row['algorithm']} - {row['calculated_at']}")
```

## Key Features

✅ **Order-independent:** Markets can be in any order, cache still hits
✅ **Algorithm-aware:** Greedy and 2-opt cached separately
✅ **Automatic:** No code changes needed, works out of the box
✅ **Persistent:** Cache survives restarts
✅ **Fast:** ~10x speedup on cache hits

## When Cache Helps Most

- **Repeated scout-markets operations** (same system, same markets)
- **2-opt optimization** (slow to calculate, instant on cache hit)
- **Continuous scouting** (minimal startup overhead after first run)
- **Multi-ship operations** (all ships benefit from cached tours)

## Troubleshooting

**Problem:** Expected cache hit, but got cache miss

**Check:**
1. Same system? Cache is per-system
2. Same algorithm? Greedy and 2-opt are separate
3. Same markets? Order doesn't matter, but set must match
4. Same return-to-start setting? Affects cache key

**Debug:**
```python
# Enable logging
import logging
logging.getLogger('lib.routing.TourOptimizer').setLevel(logging.INFO)

# Run operation, look for:
# "Cache HIT: ..." or "Cache MISS: ..."
```

## Performance

**Without Cache:**
- 2-opt (30 markets): ~45 seconds
- Route planning: ~5 seconds
- **Total: 50 seconds**

**With Cache (Hit):**
- Cache lookup: ~0.1 seconds
- Route planning: ~5 seconds
- **Total: 5.1 seconds**

**Speedup: ~10x**

## Documentation

- **Full Feature Docs:** `TOUR_CACHE_FEATURE.md`
- **Implementation Summary:** `TOUR_CACHE_SUMMARY.md`
- **This Guide:** `TOUR_CACHE_QUICKSTART.md`

## Testing

```bash
# Run test suite
python3 test_tour_cache.py

# Expected: 5/5 tests passed
```

## That's It!

The cache works automatically. Use `plan_tour()` instead of `solve_nearest_neighbor()` and enjoy the speedup!
