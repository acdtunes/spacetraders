"""Runs the OR-Tools solves in worker processes.

Every solver here drives GUIDED_LOCAL_SEARCH through a Python transit callback, so the
search re-enters the interpreter on each arc evaluation and holds the GIL for
essentially the whole solve. Threads therefore buy the gRPC server no real concurrency:
however many are configured, concurrent solves share a single core.

What that costs is easy to mis-measure, because a solve's budget is WALL CLOCK rather
than work. A solve starved of CPU still returns on schedule — it just searched less on
the way there. Contention shows up as quietly shallower tours, never as a queue, so a
wall-clock benchmark of this service reports health while the fleet's plans get worse.
Judge it on CPU-seconds consumed per solve.

Worker processes are the split that gives each concurrent solve its whole budget, and
they move WHERE a solve runs, never WHAT it computes: a worker calls the same pure
solver function on the same request payload and returns its result unchanged, so the
objective, the search parameters and the scoring are untouched.
"""
import logging
import multiprocessing
import os
import sys
import threading
import time
from concurrent.futures import ProcessPoolExecutor
from concurrent.futures.process import BrokenProcessPool

logger = logging.getLogger(__name__)

# Workers write to the stdout they inherit from the server, so their solver logs land
# in the same file; one definition keeps the two processes' lines interleaving cleanly.
LOG_FORMAT = '%(asctime)s - %(name)s - %(levelname)s - %(message)s'

WORKERS_ENV_VAR = "ROUTING_SOLVE_WORKERS"
QUEUE_DEPTH_ENV_VAR = "ROUTING_SOLVE_QUEUE_DEPTH"
MAX_WORKERS = 16           # env ROUTING_SOLVE_WORKERS, clamp [0, MAX_WORKERS]
RESERVED_CORES = 2         # left to the server's own threads and the daemon it serves
QUEUED_SOLVES_PER_WORKER = 1
MAX_QUEUE_DEPTH = 64
PREWARM_HOLD_SECONDS = 0.05


def _env_int(name, default, lo, hi):
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        logger.warning("solve pool: %s=%r is not an integer; using %d", name, raw, default)
        return default
    return max(lo, min(hi, value))


def resolve_workers():
    """How many solves may run at once: one per core the box can spare.

    Sizing to the machine is the point — under-sizing hands a fleet-wide planning burst
    back the contention the pool exists to remove. 0 stands the pool down and solves
    inline on the calling thread: the operator's escape hatch, never the default.
    """
    cores = os.cpu_count() or 1
    return _env_int(WORKERS_ENV_VAR, max(1, min(MAX_WORKERS, cores - RESERVED_CORES)),
                    0, MAX_WORKERS)


def resolve_queue_depth(workers):
    """Solves admitted beyond the ones already running. Past this a caller waits for a
    slot instead of handing the workers more work than they can hold."""
    return _env_int(QUEUE_DEPTH_ENV_VAR, workers * QUEUED_SOLVES_PER_WORKER,
                    0, MAX_QUEUE_DEPTH)


def solve_tour_payload(payload):
    """Worker entry point for one trade-tour solve: request dict in, result dict out."""
    from utils.tour_solver import solve_tour
    return solve_tour(**payload)


def partition_fleet_payload(payload):
    """Worker entry point for one fleet partition: request dict in, ship -> markets out.

    The engine is built here rather than shipped in because its only instance state is
    a pathfinding memo, which is per-process by nature.
    """
    from utils.routing_engine import ORToolsRoutingEngine
    engine = ORToolsRoutingEngine(vrp_timeout=payload["vrp_timeout"])
    return engine.optimize_fleet_tour(
        graph=payload["graph"],
        markets=payload["markets"],
        ship_locations=payload["ship_locations"],
        fuel_capacity=payload["fuel_capacity"],
        engine_speed=payload["engine_speed"],
    )


def _init_worker():
    logging.basicConfig(level=logging.INFO, format=LOG_FORMAT,
                        handlers=[logging.StreamHandler(sys.stdout)])


def _prewarm_probe(hold_seconds):
    """Pay the solver imports here instead of inside the first real solve, and hold the
    worker briefly so the submit behind this one has to start a different worker."""
    import utils.routing_engine  # noqa: F401
    import utils.tour_solver  # noqa: F401
    time.sleep(hold_seconds)
    return os.getpid()


class SolvePool:
    """A pre-warmed process pool for solve compute.

    `run` blocks until the solve returns, so a caller's RPC deadline covers both the
    wait for a slot and the solve itself. Admission is bounded by a semaphore because
    the executor's own pending queue is not: without it a burst of callers would pile
    unbounded work on the workers rather than wait.

    A worker that dies takes down only its own solve — the caller sees the failure, the
    executor is retired, and the next solve builds a replacement. The server survives.
    """

    def __init__(self, workers=None, queue_depth=None):
        self._workers = resolve_workers() if workers is None else workers
        depth = (resolve_queue_depth(self._workers) if queue_depth is None
                 else queue_depth)
        self._admission = self._workers + depth if self._workers > 0 else 0
        self._lock = threading.Lock()
        self._slots = threading.BoundedSemaphore(max(1, self._admission))
        self._executor = None
        self._closed = False

    @property
    def admission_limit(self):
        """Solves that may be in flight — running or waiting on a worker — at once.
        0 when the pool is stood down and every solve runs on its caller's thread."""
        return self._admission

    def run(self, fn, payload):
        """Solve `payload` with `fn` on a worker and return the result it produced."""
        if self._workers <= 0:
            return fn(payload)
        with self._slots:
            try:
                executor = self._current()
            except Exception:
                # Workers that will not start cost throughput, not availability: the
                # whole fleet plans through this service, so it keeps answering at the
                # serial rate and tries for workers again on the next solve.
                logger.error("solve pool: could not start workers; solving on the "
                             "request thread instead", exc_info=True)
                return fn(payload)
            try:
                return executor.submit(fn, payload).result()
            except BrokenProcessPool:
                # Deliberately NOT retried in-process. A worker dies on the pathology
                # its solve walked into, and re-running that here is how the server
                # follows it down.
                self._retire(executor)
                raise

    def warm(self):
        """Start the workers now, so the first solve of the day does not pay for them."""
        if self._workers <= 0:
            return
        try:
            self._current()
        except Exception:
            logger.error("solve pool: workers did not start; solves run on the request "
                         "thread until one does", exc_info=True)

    def close(self):
        with self._lock:
            self._closed = True
            executor, self._executor = self._executor, None
        if executor is not None:
            executor.shutdown(wait=False, cancel_futures=True)

    def _current(self):
        with self._lock:
            if self._closed:
                raise RuntimeError("solve pool is closed")
            if self._executor is None:
                self._executor = self._start()
            return self._executor

    def _start(self):
        # spawn, never fork: by the time a pool is rebuilt the server is answering on
        # many threads, and forking one of those can hand the child a lock that nothing
        # in it will ever release.
        executor = ProcessPoolExecutor(
            max_workers=self._workers,
            mp_context=multiprocessing.get_context("spawn"),
            initializer=_init_worker)
        try:
            self._prewarm(executor)
        except Exception:
            executor.shutdown(wait=False)
            raise
        return executor

    def _prewarm(self, executor):
        # A worker is only started when a submit finds none idle, so the probes have to
        # be in flight together for the pool to come up whole. They also prove a worker
        # can import the solvers before any request depends on one.
        probes = [executor.submit(_prewarm_probe, PREWARM_HOLD_SECONDS)
                  for _ in range(self._workers)]
        pids = {probe.result() for probe in probes}
        logger.info("solve pool: %d solve worker(s) ready, %d solve(s) admitted at once",
                    len(pids), self._admission)

    def _retire(self, broken):
        with self._lock:
            if self._executor is not broken:
                return
            self._executor = None
        logger.error("solve pool: a worker died; retired the pool, the next solve "
                     "starts a replacement")
        broken.shutdown(wait=False)


_shared = None
_shared_lock = threading.Lock()


def shared_pool():
    """The process-wide solve pool. One servicer serves one process, so one pool of
    workers backs every handler in it."""
    global _shared
    with _shared_lock:
        if _shared is None:
            _shared = SolvePool()
        return _shared


def close_shared_pool():
    global _shared
    with _shared_lock:
        pool, _shared = _shared, None
    if pool is not None:
        pool.close()
