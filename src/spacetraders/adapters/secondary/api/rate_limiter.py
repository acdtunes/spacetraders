import time
import threading

class RateLimiter:
    """Token bucket rate limiter"""

    def __init__(self, max_requests: int, time_window: float):
        self.max_requests = max_requests
        self.time_window = time_window
        self.tokens = max_requests
        self.last_update = time.time()
        self.lock = threading.Lock()

    def acquire(self) -> None:
        """Acquire token, blocking if necessary"""
        with self.lock:
            now = time.time()
            elapsed = now - self.last_update

            # Replenish tokens
            self.tokens = min(
                self.max_requests,
                self.tokens + (elapsed / self.time_window) * self.max_requests
            )
            self.last_update = now

            # Wait for token if needed
            if self.tokens < 1:
                wait_time = (1 - self.tokens) * (self.time_window / self.max_requests)
                time.sleep(wait_time)
                self.tokens = 0
            else:
                self.tokens -= 1
