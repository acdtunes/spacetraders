"""Properties the solve pool has to hold: real concurrency, a bounded queue, and a
worker death that costs one solve rather than the service.

The worker callables live at module scope because the pool spawns its workers, and a
spawned worker resolves a submitted function by importing it.
"""
import os
import threading
import time

import pytest

from utils import solve_pool
from utils.solve_pool import SolvePool


def _burn(payload):
    """CPU-bound, pure-Python, GIL-holding — the shape of a real solve. Returns the
    worker's pid and the window it was busy so a caller can see how many ran at once."""
    started = time.monotonic()
    deadline = started + payload["seconds"]
    total = 0
    while time.monotonic() < deadline:
        total += sum(range(1000))
    return dict(pid=os.getpid(), start=started, end=time.monotonic(), total=total)


def _boom(payload):
    raise ValueError(payload["message"])


def _die(payload):
    """Take the worker down the way a native crash would — no unwinding, no result."""
    os._exit(payload["code"])


def _run_all(pool, count, seconds):
    """Call pool.run from `count` threads at once and collect every result."""
    results = [None] * count

    def call(slot):
        results[slot] = pool.run(_burn, dict(seconds=seconds))

    threads = [threading.Thread(target=call, args=(i,)) for i in range(count)]
    started = time.monotonic()
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    return results, time.monotonic() - started


def _peak_overlap(results):
    """The most solves that were ever running at the same instant."""
    edges = [(r["start"], 1) for r in results] + [(r["end"], -1) for r in results]
    edges.sort()
    live = peak = 0
    for _, delta in edges:
        live += delta
        peak = max(peak, live)
    return peak


@pytest.fixture
def pool_factory():
    pools = []

    def make(**kwargs):
        pool = SolvePool(**kwargs)
        pools.append(pool)
        return pool

    yield make
    for pool in pools:
        pool.close()


def test_workers_solve_in_parallel(pool_factory):
    """The whole point: N GIL-bound solves take about one solve's wall clock, not N.
    Threads cannot do this, which is why the pool exists."""
    pool = pool_factory(workers=3)
    pool.warm()
    results, elapsed = _run_all(pool, 3, seconds=0.6)
    assert len({r["pid"] for r in results}) == 3      # three processes, not one
    assert _peak_overlap(results) == 3                # genuinely at the same time
    assert elapsed < 3 * 0.6                          # and faster than solving serially


def test_solves_run_off_the_calling_process(pool_factory):
    pool = pool_factory(workers=1)
    result = pool.run(_burn, dict(seconds=0.01))
    assert result["pid"] != os.getpid()


def test_queue_depth_admits_without_widening_concurrency(pool_factory):
    """A deeper queue lets more callers in; it must never let more solves RUN. The
    queue is there to absorb a burst, not to oversubscribe the box."""
    pool = pool_factory(workers=2, queue_depth=4)
    pool.warm()
    assert pool.admission_limit == 6
    results, _ = _run_all(pool, 6, seconds=0.3)
    assert _peak_overlap(results) == 2


def test_admission_bounds_solves_in_flight(pool_factory):
    """Past the admission limit a caller waits rather than handing the workers more
    work; with one slot the solves strictly queue."""
    pool = pool_factory(workers=1, queue_depth=0)
    pool.warm()
    assert pool.admission_limit == 1
    results, elapsed = _run_all(pool, 3, seconds=0.3)
    assert _peak_overlap(results) == 1
    assert elapsed >= 3 * 0.3


def test_worker_exception_reaches_the_caller_and_keeps_the_pool(pool_factory):
    pool = pool_factory(workers=1)
    with pytest.raises(ValueError, match="planned failure"):
        pool.run(_boom, dict(message="planned failure"))
    assert pool.run(_burn, dict(seconds=0.01))["pid"] != os.getpid()


def test_worker_death_fails_one_solve_then_the_pool_recovers(pool_factory):
    """A worker that dies mid-solve must cost exactly that solve. The next call comes
    back on a live worker — no restart, no permanently broken servicer."""
    from concurrent.futures.process import BrokenProcessPool

    pool = pool_factory(workers=1)
    pool.warm()
    doomed = pool.run(_burn, dict(seconds=0.01))["pid"]

    with pytest.raises(BrokenProcessPool):
        pool.run(_die, dict(code=1))

    recovered = pool.run(_burn, dict(seconds=0.01))
    assert recovered["pid"] not in (doomed, os.getpid())


def test_workers_that_cannot_start_degrade_to_the_calling_thread(pool_factory, monkeypatch):
    """The fleet plans through this service, so a box that cannot spawn workers costs
    throughput, never availability — warm-up and solves both keep going."""
    def refuse(self):
        raise OSError("no processes available")

    monkeypatch.setattr(SolvePool, "_start", refuse)
    pool = pool_factory(workers=2)
    pool.warm()
    assert pool.run(_burn, dict(seconds=0.01))["pid"] == os.getpid()


def test_stood_down_pool_solves_inline(pool_factory):
    """The operator's escape hatch: no workers, no pool, solves on the calling thread."""
    pool = pool_factory(workers=0)
    assert pool.admission_limit == 0
    assert pool.run(_burn, dict(seconds=0.01))["pid"] == os.getpid()


def test_worker_count_defaults_on_and_reads_the_environment(monkeypatch):
    monkeypatch.delenv(solve_pool.WORKERS_ENV_VAR, raising=False)
    assert solve_pool.resolve_workers() >= 1

    monkeypatch.setenv(solve_pool.WORKERS_ENV_VAR, "3")
    assert solve_pool.resolve_workers() == 3

    monkeypatch.setenv(solve_pool.WORKERS_ENV_VAR, "0")
    assert solve_pool.resolve_workers() == 0

    monkeypatch.setenv(solve_pool.WORKERS_ENV_VAR, str(solve_pool.MAX_WORKERS + 99))
    assert solve_pool.resolve_workers() == solve_pool.MAX_WORKERS

    monkeypatch.setenv(solve_pool.WORKERS_ENV_VAR, "not-a-number")
    assert solve_pool.resolve_workers() >= 1
