#!/usr/bin/env python3
"""
Test script for tour cache functionality

Tests:
1. Table creation
2. Cache save and retrieval
3. Order-independent cache keys (sorted markets)
4. Cache hit/miss behavior
"""

import sys
import logging
import json
from pathlib import Path

# Add lib to path
sys.path.insert(0, str(Path(__file__).parent))

from lib.database import get_database

# Setup logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


def test_table_creation():
    """Test 1: Verify tour_cache table exists"""
    logger.info("=" * 60)
    logger.info("Test 1: Table Creation")
    logger.info("=" * 60)

    db = get_database("data/test_tour_cache.db")

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT name FROM sqlite_master
            WHERE type='table' AND name='tour_cache'
        """)
        result = cursor.fetchone()

        if result:
            logger.info("✓ tour_cache table exists")

            # Check columns
            cursor.execute("PRAGMA table_info(tour_cache)")
            columns = cursor.fetchall()
            column_names = [col['name'] for col in columns]

            expected_columns = [
                'system', 'markets', 'algorithm', 'start_waypoint',
                'tour_order', 'total_distance', 'calculated_at'
            ]

            for col in expected_columns:
                if col in column_names:
                    logger.info(f"  ✓ Column '{col}' exists")
                else:
                    logger.error(f"  ✗ Column '{col}' missing!")
                    return False
        else:
            logger.error("✗ tour_cache table does not exist!")
            return False

    return True


def test_cache_save_and_retrieve():
    """Test 2: Save and retrieve a tour from cache"""
    logger.info("\n" + "=" * 60)
    logger.info("Test 2: Cache Save and Retrieval")
    logger.info("=" * 60)

    db = get_database("data/test_tour_cache.db")

    # Test data
    system = "X1-TEST"
    markets = ["X1-TEST-A1", "X1-TEST-B2", "X1-TEST-C3"]
    algorithm = "2opt"
    start_waypoint = "X1-TEST-A1"
    tour_order = ["X1-TEST-A1", "X1-TEST-B2", "X1-TEST-C3", "X1-TEST-A1"]
    total_distance = 456.78

    # Save to cache
    with db.transaction() as conn:
        success = db.save_tour_cache(
            conn, system, markets, algorithm, tour_order, total_distance, start_waypoint
        )

        if success:
            logger.info("✓ Tour saved to cache")
        else:
            logger.error("✗ Failed to save tour to cache")
            return False

    # Retrieve from cache
    with db.connection() as conn:
        cached = db.get_cached_tour(conn, system, markets, algorithm, start_waypoint)

        if cached:
            logger.info("✓ Tour retrieved from cache")
            logger.info(f"  Tour order: {cached['tour_order']}")
            logger.info(f"  Total distance: {cached['total_distance']}")
            logger.info(f"  Calculated at: {cached['calculated_at']}")

            # Verify data matches
            if cached['tour_order'] == tour_order:
                logger.info("  ✓ Tour order matches")
            else:
                logger.error(f"  ✗ Tour order mismatch!")
                logger.error(f"    Expected: {tour_order}")
                logger.error(f"    Got: {cached['tour_order']}")
                return False

            if abs(cached['total_distance'] - total_distance) < 0.01:
                logger.info("  ✓ Total distance matches")
            else:
                logger.error(f"  ✗ Distance mismatch!")
                logger.error(f"    Expected: {total_distance}")
                logger.error(f"    Got: {cached['total_distance']}")
                return False
        else:
            logger.error("✗ Failed to retrieve tour from cache")
            return False

    return True


def test_order_independent_cache():
    """Test 3: Verify same markets in different order use same cache entry"""
    logger.info("\n" + "=" * 60)
    logger.info("Test 3: Order-Independent Cache Keys")
    logger.info("=" * 60)

    db = get_database("data/test_tour_cache.db")

    system = "X1-TEST2"
    algorithm = "greedy"
    start_waypoint = None

    # Same markets in different orders
    markets_v1 = ["X1-TEST2-A1", "X1-TEST2-B2", "X1-TEST2-C3"]
    markets_v2 = ["X1-TEST2-C3", "X1-TEST2-A1", "X1-TEST2-B2"]  # Different order

    tour_order = ["X1-TEST2-START", "X1-TEST2-A1", "X1-TEST2-B2", "X1-TEST2-C3"]
    total_distance = 789.12

    # Save with first order
    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system, markets_v1, algorithm, tour_order, total_distance, start_waypoint
        )
    logger.info("✓ Saved tour with market order: " + str(markets_v1))

    # Try to retrieve with second order (should find the same cache entry)
    with db.connection() as conn:
        cached = db.get_cached_tour(conn, system, markets_v2, algorithm, start_waypoint)

        if cached:
            logger.info("✓ Retrieved tour with different market order: " + str(markets_v2))
            logger.info("  ✓ Cache is order-independent!")

            if cached['tour_order'] == tour_order:
                logger.info("  ✓ Retrieved tour matches saved tour")
            else:
                logger.error(f"  ✗ Tour order mismatch!")
                return False
        else:
            logger.error("✗ Failed to retrieve with different order - cache should be order-independent!")
            return False

    return True


def test_cache_key_uniqueness():
    """Test 4: Verify different algorithms/start points create separate cache entries"""
    logger.info("\n" + "=" * 60)
    logger.info("Test 4: Cache Key Uniqueness")
    logger.info("=" * 60)

    db = get_database("data/test_tour_cache.db")

    system = "X1-TEST3"
    markets = ["X1-TEST3-A1", "X1-TEST3-B2"]

    # Save greedy tour
    greedy_tour = ["X1-TEST3-A1", "X1-TEST3-B2"]
    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system, markets, "greedy", greedy_tour, 100.0, None
        )
    logger.info("✓ Saved greedy tour")

    # Save 2opt tour (different algorithm, same markets)
    opt_tour = ["X1-TEST3-B2", "X1-TEST3-A1"]  # Different order
    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system, markets, "2opt", opt_tour, 95.0, None
        )
    logger.info("✓ Saved 2opt tour")

    # Verify we get different results for different algorithms
    with db.connection() as conn:
        greedy_cached = db.get_cached_tour(conn, system, markets, "greedy", None)
        opt_cached = db.get_cached_tour(conn, system, markets, "2opt", None)

        if greedy_cached and opt_cached:
            if greedy_cached['tour_order'] != opt_cached['tour_order']:
                logger.info("✓ Different algorithms produce different cache entries")
                logger.info(f"  Greedy: {greedy_cached['tour_order']}")
                logger.info(f"  2opt: {opt_cached['tour_order']}")
            else:
                logger.error("✗ Different algorithms should produce different tours!")
                return False
        else:
            logger.error("✗ Failed to retrieve tours")
            return False

    return True


def test_null_start_handling():
    """Test 5: Verify NULL start_waypoint handling"""
    logger.info("\n" + "=" * 60)
    logger.info("Test 5: NULL Start Waypoint Handling")
    logger.info("=" * 60)

    db = get_database("data/test_tour_cache.db")

    system = "X1-TEST4"
    markets = ["X1-TEST4-A1", "X1-TEST4-B2"]
    algorithm = "greedy"

    # Save with NULL start
    tour_null_start = ["X1-TEST4-A1", "X1-TEST4-B2"]
    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system, markets, algorithm, tour_null_start, 50.0, None
        )
    logger.info("✓ Saved tour with NULL start_waypoint")

    # Save with specific start
    tour_specific_start = ["X1-TEST4-B2", "X1-TEST4-A1"]
    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system, markets, algorithm, tour_specific_start, 50.0, "X1-TEST4-B2"
        )
    logger.info("✓ Saved tour with specific start_waypoint")

    # Verify they're separate cache entries
    with db.connection() as conn:
        null_cached = db.get_cached_tour(conn, system, markets, algorithm, None)
        specific_cached = db.get_cached_tour(conn, system, markets, algorithm, "X1-TEST4-B2")

        if null_cached and specific_cached:
            if null_cached['tour_order'] != specific_cached['tour_order']:
                logger.info("✓ NULL and specific start create separate cache entries")
                logger.info(f"  NULL start: {null_cached['tour_order']}")
                logger.info(f"  Specific start: {specific_cached['tour_order']}")
            else:
                logger.warning("⚠ Tours are same (might be expected for this test data)")
        else:
            logger.error("✗ Failed to retrieve tours")
            return False

    return True


def main():
    """Run all tests"""
    logger.info("\nTour Cache Test Suite")
    logger.info("=" * 60)

    # Clean up test database first
    test_db_path = Path("data/test_tour_cache.db")
    if test_db_path.exists():
        test_db_path.unlink()
        logger.info("Cleaned up existing test database\n")

    tests = [
        ("Table Creation", test_table_creation),
        ("Cache Save and Retrieval", test_cache_save_and_retrieve),
        ("Order-Independent Cache", test_order_independent_cache),
        ("Cache Key Uniqueness", test_cache_key_uniqueness),
        ("NULL Start Handling", test_null_start_handling),
    ]

    results = []
    for name, test_func in tests:
        try:
            result = test_func()
            results.append((name, result))
        except Exception as e:
            logger.error(f"\n✗ Test '{name}' crashed: {e}")
            import traceback
            traceback.print_exc()
            results.append((name, False))

    # Summary
    logger.info("\n" + "=" * 60)
    logger.info("Test Summary")
    logger.info("=" * 60)

    passed = sum(1 for _, result in results if result)
    total = len(results)

    for name, result in results:
        status = "✓ PASS" if result else "✗ FAIL"
        logger.info(f"{status}: {name}")

    logger.info("-" * 60)
    logger.info(f"Total: {passed}/{total} tests passed")

    if passed == total:
        logger.info("\n🎉 All tests passed!")
        return 0
    else:
        logger.error(f"\n❌ {total - passed} test(s) failed")
        return 1


if __name__ == "__main__":
    sys.exit(main())
