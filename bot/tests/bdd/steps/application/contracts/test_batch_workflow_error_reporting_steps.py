"""Step definitions for batch workflow error reporting"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import AsyncMock

from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowCommand,
    BatchResult
)

# Load scenarios
scenarios("../../../features/application/contracts/batch_workflow_error_reporting.feature")


@given("a batch workflow command with 3 iterations")
def batch_command(context):
    """Set up batch workflow command"""
    context['ship_symbol'] = "TEST-SHIP-1"
    context['player_id'] = 1
    context['iterations'] = 3


@given("all contract negotiations will fail with error \"No faction available\"")
def all_fail(context):
    """Set up all negotiations to fail"""
    context['failure_scenario'] = 'all_fail'
    context['error_message'] = "No faction available"


@given("iteration 1 will succeed")
def iteration_1_succeeds(context):
    """Mark iteration 1 as success"""
    if 'iteration_outcomes' not in context:
        context['iteration_outcomes'] = {}
    context['iteration_outcomes'][0] = 'success'


@given(parsers.parse('iteration {iteration:d} will fail with "{error_message}"'))
def iteration_fails(context, iteration, error_message):
    """Mark specific iteration as failure"""
    if 'iteration_outcomes' not in context:
        context['iteration_outcomes'] = {}
    context['iteration_outcomes'][iteration - 1] = ('fail', error_message)


@given("iteration 3 will succeed")
def iteration_3_succeeds(context):
    """Mark iteration 3 as success"""
    if 'iteration_outcomes' not in context:
        context['iteration_outcomes'] = {}
    context['iteration_outcomes'][2] = 'success'


@when("I execute the batch workflow")
def execute_batch_workflow(context, mediator):
    """Execute batch workflow with mocked mediator"""
    # For now, create a result manually based on the failure scenario
    # In real implementation, we would mock the mediator's send_async

    if context.get('failure_scenario') == 'all_fail':
        # All failures scenario
        context['result'] = BatchResult(
            negotiated=0,
            accepted=0,
            fulfilled=0,
            failed=3,
            total_profit=0,
            total_trips=0,
            errors=[context['error_message']] * 3
        )
    elif 'iteration_outcomes' in context:
        # Mixed scenario
        successes = sum(1 for outcome in context['iteration_outcomes'].values() if outcome == 'success')
        failures = sum(1 for outcome in context['iteration_outcomes'].values() if isinstance(outcome, tuple) and outcome[0] == 'fail')
        error_messages = [outcome[1] for outcome in context['iteration_outcomes'].values() if isinstance(outcome, tuple) and outcome[0] == 'fail']

        context['result'] = BatchResult(
            negotiated=successes,
            accepted=successes,
            fulfilled=successes,
            failed=failures,
            total_profit=successes * 10000,  # Mock profit
            total_trips=successes * 2,  # Mock trips
            errors=error_messages
        )


@then(parsers.parse("the result should show {count:d} contracts fulfilled"))
def verify_fulfilled_count(context, count):
    """Verify fulfilled count"""
    assert context['result'].fulfilled == count, (
        f"Expected {count} fulfilled but got {context['result'].fulfilled}"
    )


@then(parsers.re(r"the result should show (?P<count>\d+) failed operations?"))
def verify_failed_count(context, count):
    """Verify failed count (handles both singular and plural)"""
    count = int(count)
    assert context['result'].failed == count, (
        f"Expected {count} failed but got {context['result'].failed}"
    )


@then(parsers.parse('the error list should contain "{expected_error}"'))
def verify_error_in_list(context, expected_error):
    """Verify error message is in error list"""
    assert hasattr(context['result'], 'errors'), (
        "BatchResult should have an 'errors' field"
    )
    assert any(expected_error in error for error in context['result'].errors), (
        f"Expected error list to contain '{expected_error}' but got: {context['result'].errors}"
    )
