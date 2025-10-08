"""Control primitives shared across operations modules."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class CircuitBreaker:
    """Track consecutive failures and trip once the limit is reached."""

    limit: int
    failures: int = 0

    def record_success(self) -> None:
        self.failures = 0

    def record_failure(self) -> int:
        self.failures += 1
        return self.failures

    def tripped(self) -> bool:
        return self.failures >= self.limit

