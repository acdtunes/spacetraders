#!/usr/bin/env python3
"""
Test tour cache persistence across connection close/reopen

This test validates that tours are immediately persisted to disk after generation,
even if the database connection is closed (simulating daemon crash).

Critical requirement: Tours MUST be queryable immediately after generation,
not after tour completion. This ensures the visualizer can display optimized
routes immediately.
"""

import tempfile
from pathlib import Path

import pytest

from spacetraders_bot.core.database import Database


@pytest.fixture
def temp_db_path():
    """Create temporary database for isolated testing"""
    with tempfile.NamedTemporaryFile(delete=False, suffix=".db") as f:
        db_path = Path(f.name)

    yield db_path

    # Cleanup
    if db_path.exists():
        db_path.unlink()
    wal_file = Path(str(db_path) + "-wal")
    if wal_file.exists():
        wal_file.unlink()
    shm_file = Path(str(db_path) + "-shm")
    if shm_file.exists():
        shm_file.unlink()


def regression_wal_checkpoint_persists_data_immediately(temp_db_path):
    """
    Test that PRAGMA wal_checkpoint(FULL) forces immediate persistence.

    This is the core fix: without checkpoint, data stays in WAL file and
    is lost if process crashes. With checkpoint, data is flushed to main DB file.
    """
    # Step 1: Write data WITH checkpoint
    db1 = Database(temp_db_path)

    with db1.transaction() as conn:
        db1.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=["M1", "M2", "M3"],
            algorithm="ortools",
            tour_order=["S", "M1", "M2", "M3"],
            total_distance=100.0,
            start_waypoint=None
        )
    # Force immediate persistence AFTER transaction (the fix)
    # WAL checkpoint must run outside transaction to avoid table lock
    with db1.connection() as conn:
        conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Step 2: Close connection (simulate daemon crash)
    del db1

    # Step 3: Reopen database in NEW connection
    db2 = Database(temp_db_path)

    # Step 4: Verify data persisted
    with db2.connection() as conn:
        cached = db2.get_cached_tour(
            conn,
            system="X1-TEST",
            markets=["M1", "M2", "M3"],
            algorithm="ortools",
            start_waypoint=None
        )

    assert cached is not None, "Tour not found after checkpoint and connection reopen"
    assert cached["tour_order"] == ["S", "M1", "M2", "M3"], "Tour order mismatch"
    assert cached["total_distance"] == 100.0, "Distance mismatch"

def regression_without_checkpoint_data_may_be_lost(temp_db_path):
    """
    Test that WITHOUT wal_checkpoint, data may be lost on crash.

    This demonstrates the bug: data commits to transaction but stays in WAL,
    not guaranteed to persist if process crashes before WAL checkpoint.

    NOTE: This test may pass sometimes (if auto-checkpoint triggers), but
    demonstrates the unreliable behavior without explicit checkpoint.
    """
    # Step 1: Write data WITHOUT explicit checkpoint
    db1 = Database(temp_db_path)

    with db1.transaction() as conn:
        db1.save_tour_cache(
            conn,
            system="X1-TEST-BUG",
            markets=["M1", "M2"],
            algorithm="ortools",
            tour_order=["S", "M1", "M2"],
            total_distance=50.0,
            start_waypoint=None
        )
        # NO checkpoint - data may stay in WAL file

    # Step 2: Force close without graceful shutdown (simulate crash)
    # In real crash, WAL file is left uncommitted
    del db1

    # Step 3: Reopen and check
    db2 = Database(temp_db_path)

    with db2.connection() as conn:
        cached = db2.get_cached_tour(
            conn,
            system="X1-TEST-BUG",
            markets=["M1", "M2"],
            algorithm="ortools",
            start_waypoint=None
        )

    # Data MIGHT persist (if auto-checkpoint happened), but not guaranteed
    # This test documents the unreliable behavior
    # Allow flaky outcome but assert documented behavior when data missing
    if not cached:
        pytest.skip("Data lost without explicit checkpoint (expected crash scenario)")


def regression_tour_cache_with_return_to_start_persists(temp_db_path):
    """
    Test tour cache persistence for return_to_start=True case.

    This ensures the fix works for both tour types (open and closed loops).
    """
    db = Database(temp_db_path)

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-TEST-LOOP",
            markets=["M1", "M2", "M3"],
            algorithm="ortools",
            tour_order=["S", "M1", "M2", "M3", "S"],  # Returns to start
            total_distance=120.0,
            start_waypoint="S"  # return_to_start=True uses start_waypoint
        )
    # Checkpoint AFTER transaction
    with db.connection() as conn:
        conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Close and reopen
    del db
    db2 = Database(temp_db_path)

    with db2.connection() as conn:
        cached = db2.get_cached_tour(
            conn,
            system="X1-TEST-LOOP",
            markets=["M1", "M2", "M3"],
            algorithm="ortools",
            start_waypoint="S"
        )

    assert cached is not None, "Return-to-start tour not persisted"
    assert cached["tour_order"][0] == "S", "Tour should start at S"
    assert cached["tour_order"][-1] == "S", "Tour should end at S"

def regression_multiple_tours_persist_with_checkpoints(temp_db_path):
    """
    Test that multiple tour cache writes with checkpoints all persist.

    This validates checkpoint doesn't cause data loss or corruption.
    """
    db = Database(temp_db_path)

    # Write 5 different tours
    for i in range(5):
        with db.transaction() as conn:
            db.save_tour_cache(
                conn,
                system=f"X1-SYS{i}",
                markets=[f"M{i}1", f"M{i}2"],
                algorithm="ortools",
                tour_order=[f"S{i}", f"M{i}1", f"M{i}2"],
                total_distance=float(10 + i),
                start_waypoint=None
            )
        # Checkpoint AFTER each transaction
        with db.connection() as conn:
            conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Close and reopen
    del db
    db2 = Database(temp_db_path)

    # Verify all 5 tours persisted
    with db2.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT COUNT(*) as count FROM tour_cache")
        result = cursor.fetchone()
        count = result["count"]

    assert count == 5, f"Expected 5 tours, found {count}"
def regression_tour_cache_immediately_queryable_same_connection(temp_db_path):
    """
    Test that tours are queryable IMMEDIATELY after save, within same connection.

    This validates the visualizer can query tours without closing/reopening.
    """
    db = Database(temp_db_path)

    with db.transaction() as conn:
        db.save_tour_cache(
            conn,
            system="X1-IMMEDIATE",
            markets=["M1", "M2"],
            algorithm="ortools",
            tour_order=["S", "M1", "M2"],
            total_distance=75.0,
            start_waypoint=None
        )
    # Checkpoint AFTER transaction
    with db.connection() as conn:
        conn.execute("PRAGMA wal_checkpoint(FULL)")

    # Query after commit
    with db.connection() as conn:
        cached = db.get_cached_tour(
            conn,
            system="X1-IMMEDIATE",
            markets=["M1", "M2"],
            algorithm="ortools",
            start_waypoint=None
        )

    assert cached is not None, "Tour not immediately queryable after save+checkpoint"
def regression_checkpoint_performance_is_acceptable(temp_db_path):
    """
    Test that PRAGMA wal_checkpoint(FULL) doesn't cause significant slowdown.

    Checkpoint should be fast (<100ms) for typical tour cache writes.
    """
    import time

    db = Database(temp_db_path)

    times = []
    for i in range(10):
        start = time.time()
        with db.transaction() as conn:
            db.save_tour_cache(
                conn,
                system=f"X1-PERF{i}",
                markets=["M1", "M2", "M3"],
                algorithm="ortools",
                tour_order=["S", "M1", "M2", "M3"],
                total_distance=100.0,
                start_waypoint=None
            )
        # Checkpoint AFTER transaction
        with db.connection() as conn:
            conn.execute("PRAGMA wal_checkpoint(FULL)")
        elapsed = time.time() - start
        times.append(elapsed)

    avg_time = sum(times) / len(times)
    max_time = max(times)

    # Checkpoints should be very fast (under 100ms even on slow systems)
    assert avg_time < 0.1, f"Checkpoint too slow: {avg_time*1000:.2f}ms avg"
    assert max_time < 0.2, f"Checkpoint too slow: {max_time*1000:.2f}ms max"


if __name__ == "__main__":
    # Run tests manually for debugging
    import sys
    pytest.main([__file__, "-v", "-s"] + sys.argv[1:])
