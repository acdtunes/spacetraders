"""Step definitions for CLI command routing tests."""

import importlib
import sys

import pytest
from pytest_bdd import given, when, then, parsers, scenarios

# Load all CLI scenarios
scenarios('../../features/unit/cli.feature')

# Import CLI main module
cli_main = importlib.import_module('spacetraders_bot.cli.main')


@pytest.fixture
def cli_context():
    """Shared context for CLI test scenarios."""
    return {
        'handlers': {},
        'captured_args': {},
        'exit_code': None,
        'stdout': '',
    }


def _run_cli(monkeypatch, capsys, argv, **handlers):
    """Execute CLI with mocked handlers."""
    # Install handler mocks
    for name, handler in handlers.items():
        monkeypatch.setattr(cli_main, name, handler)

    # Set command line arguments
    monkeypatch.setattr(sys, 'argv', ['spacetraders-bot', *argv])

    # Execute CLI
    exit_code = cli_main.main()

    # Capture stdout
    captured = capsys.readouterr()

    return exit_code, captured.out


@given('the CLI is ready to process commands')
def cli_ready(cli_context):
    """CLI is initialized and ready."""
    cli_context['ready'] = True


@when(parsers.parse('I run "{command}"'))
def run_cli_command(monkeypatch, capsys, cli_context, command):
    """Execute a CLI command with arguments."""
    # Parse command string into argv
    argv = command.split()

    # Create mock handlers that capture their calls
    handlers = {}

    # Define mock handler factory
    def make_handler(handler_name, expected_exit_code=0):
        def handler(args):
            cli_context['captured_args'][handler_name] = args
            cli_context['handlers'][handler_name] = True
            return expected_exit_code
        return handler

    # Register handlers based on command
    if 'graph-build' in command:
        handlers['graph_build_operation'] = make_handler('graph_build_operation', 0)
    elif 'route-plan' in command:
        handlers['route_plan_operation'] = make_handler('route_plan_operation', 5)
    elif 'assignments' in command and 'list' in command:
        handlers['assignment_list_operation'] = make_handler('assignment_list_operation', 0)
    elif 'scout-coordinator' in command:
        handlers['coordinator_status_operation'] = make_handler('coordinator_status_operation', 0)
    elif 'daemon' in command and 'start' in command:
        handlers['daemon_start_operation'] = make_handler('daemon_start_operation', 3)

    # Run CLI
    exit_code, stdout = _run_cli(monkeypatch, capsys, argv, **handlers)

    cli_context['exit_code'] = exit_code
    cli_context['stdout'] = stdout


@then(parsers.parse('the {operation_name} should be called'))
def verify_operation_called(cli_context, operation_name):
    """Verify that the specified operation handler was called."""
    assert cli_context['handlers'].get(operation_name) is True, \
        f"Expected {operation_name} to be called, but it wasn't"


@then(parsers.parse('the operation should receive system "{system}"'))
def verify_system_parameter(cli_context, system):
    """Verify the operation received the correct system parameter."""
    args = cli_context['captured_args'].get('graph_build_operation')
    assert args is not None, "Operation was not called"
    assert args.system == system, f"Expected system {system}, got {args.system}"


@then(parsers.parse('the operation should receive goal waypoint "{goal}"'))
def verify_goal_parameter(cli_context, goal):
    """Verify the operation received the correct goal parameter."""
    args = cli_context['captured_args'].get('route_plan_operation')
    assert args is not None, "Operation was not called"
    assert args.goal == goal, f"Expected goal {goal}, got {args.goal}"


@then(parsers.parse('the operation should receive coordinator action "{action}"'))
def verify_coordinator_action(cli_context, action):
    """Verify the operation received the correct coordinator action."""
    args = cli_context['captured_args'].get('coordinator_status_operation')
    assert args is not None, "Operation was not called"
    assert args.coordinator_action == action, \
        f"Expected coordinator_action {action}, got {args.coordinator_action}"


@then(parsers.parse('the operation should receive daemon action "{action}"'))
def verify_daemon_action(cli_context, action):
    """Verify the operation received the correct daemon action."""
    args = cli_context['captured_args'].get('daemon_start_operation')
    assert args is not None, "Operation was not called"
    assert args.daemon_action == action, \
        f"Expected daemon_action {action}, got {args.daemon_action}"


@then(parsers.parse('the command should succeed with exit code {code:d}'))
def verify_success_exit_code(cli_context, code):
    """Verify the command succeeded with the expected exit code."""
    assert cli_context['exit_code'] == code, \
        f"Expected exit code {code}, got {cli_context['exit_code']}"


@then(parsers.parse('the command should return exit code {code:d}'))
def verify_exit_code(cli_context, code):
    """Verify the command returned the expected exit code."""
    assert cli_context['exit_code'] == code, \
        f"Expected exit code {code}, got {cli_context['exit_code']}"


@then(parsers.parse('the command should fail with exit code {code:d}'))
def verify_failure_exit_code(cli_context, code):
    """Verify the command failed with the expected exit code."""
    assert cli_context['exit_code'] == code, \
        f"Expected failure exit code {code}, got {cli_context['exit_code']}"


@then('usage help should be displayed')
def verify_usage_help(cli_context):
    """Verify that usage help was displayed."""
    assert 'usage' in cli_context['stdout'].lower(), \
        "Expected usage help in output, but didn't find it"
