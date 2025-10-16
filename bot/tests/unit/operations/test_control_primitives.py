from spacetraders_bot.operations.control import CircuitBreaker


def regression_circuit_breaker_records_failures_and_trips():
    breaker = CircuitBreaker(limit=3)

    assert breaker.record_failure() == 1
    assert breaker.failures == 1
    assert breaker.tripped() is False

    assert breaker.record_failure() == 2
    assert breaker.tripped() is False

    assert breaker.record_failure() == 3
    assert breaker.failures == 3
    assert breaker.tripped() is True


def regression_circuit_breaker_resets_on_success():
    breaker = CircuitBreaker(limit=2)

    breaker.record_failure()
    breaker.record_success()

    assert breaker.failures == 0
    assert breaker.tripped() is False
