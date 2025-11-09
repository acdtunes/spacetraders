"""Integration tests for captain CLI commands"""
import pytest
import subprocess
import json
import sys
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone

from configuration.container import get_mediator, reset_container
from application.player.commands.register_player import RegisterPlayerCommand
from application.captain.commands.log_captain_entry import LogCaptainEntryCommand
from adapters.secondary.persistence.database import Database

# Load scenarios from feature file
scenarios("../../../features/integration/cli/captain_cli.feature")

# Helper function to get env for subprocess that shares database
def get_subprocess_env():
    """Get environment dict that makes subprocess use same database"""
    import os
    from configuration.container import get_database
    env = os.environ.copy()
    db = get_database()
    if db.db_path:
        env['SPACETRADERS_DB_PATH'] = str(db.db_path)
    return env

# Helper function to sync database for subprocess visibility
def sync_database_for_subprocess():
    """Force WAL checkpoint to ensure subprocess can see all writes"""
    from configuration.container import get_database
    import sqlite3
    db = get_database()
    if db.backend == 'sqlite' and db.db_path != ":memory:":
        # Open a new connection and force a WAL checkpoint
        # This flushes all pending writes from WAL to the main database file
        conn = sqlite3.connect(str(db.db_path))
        try:
            conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
            conn.commit()
        finally:
            conn.close()

@pytest.fixture
def context():
    """Shared test context"""
    return {"player_id": None, "result": None, "exit_code": None, "output": "", "error": ""}

@pytest.fixture(autouse=True)
def setup_teardown(context):
    """Reset container and clean database before each test"""
    reset_container()

    # Clean database for test isolation
    from configuration.container import get_database
    db = get_database()
    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("DELETE FROM captain_logs")
        cursor.execute("DELETE FROM players")

    # Sync so changes are visible
    sync_database_for_subprocess()

    yield

    # Cleanup after test
    reset_container()
    db = get_database()
    try:
        with db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("DELETE FROM captain_logs")
            cursor.execute("DELETE FROM players")
        sync_database_for_subprocess()
    except Exception:
        # Ignore errors during cleanup (e.g., tables don't exist)
        pass

# Background steps

@given('a registered player with agent "ENDURANCE"')
def register_test_player(context):
    """Register a test player"""
    import asyncio
    mediator = get_mediator()
    command = RegisterPlayerCommand(
        agent_symbol="ENDURANCE",
        token="test-token-endurance"
    )
    player = asyncio.run(mediator.send_async(command))
    context["player_id"] = player.player_id

    # Sync database so CLI subprocess can see the player
    sync_database_for_subprocess()

# When steps - Log command

@when('I log a captain entry with:')
def log_captain_entry_cli(context, datatable):
    """Execute captain log CLI command"""
    # Parse table into dict (datatable is list of lists)
    params = {}
    for row in datatable:
        if len(row) >= 2 and row[0] != 'field':
            params[row[0]] = row[1]

    # Build CLI command - Use the spacetraders entry point directly
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "log",
        "--agent", params.get("agent", "ENDURANCE"),
        "--type", params.get("entry_type", "session_start"),
        "--narrative", params.get("narrative", "")
    ]

    # Add optional params
    if "event_data" in params and params["event_data"]:
        cmd.extend(["--event-data", params["event_data"]])
    if "tags" in params and params["tags"]:
        cmd.extend(["--tags", params["tags"]])
    if "fleet_snapshot" in params and params["fleet_snapshot"]:
        cmd.extend(["--fleet-snapshot", params["fleet_snapshot"]])

    # Execute command with shared database environment
    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

# When steps - Logs command

@when(parsers.parse('I retrieve captain logs for agent "{agent}"'))
def retrieve_captain_logs_cli(context, agent):
    """Execute captain logs CLI command"""
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "logs",
        "--agent", agent
    ]

    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

@when(parsers.parse('I retrieve captain logs for agent "{agent}" with type "{entry_type}"'))
def retrieve_captain_logs_with_type(context, agent, entry_type):
    """Execute captain logs CLI command with type filter"""
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "logs",
        "--agent", agent,
        "--type", entry_type
    ]

    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

@when(parsers.parse('I retrieve captain logs for agent "{agent}" with tags "{tags}"'))
def retrieve_captain_logs_with_tags(context, agent, tags):
    """Execute captain logs CLI command with tags filter"""
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "logs",
        "--agent", agent,
        "--tags", tags
    ]

    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

@when(parsers.parse('I retrieve captain logs for agent "{agent}" with limit {limit:d}'))
def retrieve_captain_logs_with_limit(context, agent, limit):
    """Execute captain logs CLI command with limit"""
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "logs",
        "--agent", agent,
        "--limit", str(limit)
    ]

    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

@when(parsers.parse('I retrieve captain logs for agent "{agent}" since "{since}"'))
def retrieve_captain_logs_since(context, agent, since):
    """Execute captain logs CLI command with since filter"""
    cmd = [
        sys.executable,
        "-m", "adapters.primary.cli.main",
        "captain",
        "logs",
        "--agent", agent,
        "--since", since
    ]

    result = subprocess.run(cmd, capture_output=True, text=True, env=get_subprocess_env())
    context["exit_code"] = result.returncode
    context["output"] = result.stdout
    context["error"] = result.stderr

# Given steps - Test data setup

@given('the following captain logs exist:')
def create_captain_logs(context, datatable):
    """Create test captain logs"""
    import asyncio
    mediator = get_mediator()

    for row in datatable:
        if len(row) >= 2 and row[0] != 'entry_type':
            entry_type = row[0]
            narrative = row[1]

            command = LogCaptainEntryCommand(
                player_id=context["player_id"],
                entry_type=entry_type,
                narrative=narrative
            )
            asyncio.run(mediator.send_async(command))

    # Sync database so CLI subprocess can see the logs
    sync_database_for_subprocess()


@given('the following captain logs exist with tags:')
def create_captain_logs_with_tags(context, datatable):
    """Create test captain logs with tags"""
    import asyncio
    mediator = get_mediator()

    for row in datatable:
        if len(row) >= 3 and row[0] != 'entry_type':
            entry_type = row[0]
            narrative = row[1]
            tags = row[2].split(',')

            command = LogCaptainEntryCommand(
                player_id=context["player_id"],
                entry_type=entry_type,
                narrative=narrative,
                tags=tags
            )
            asyncio.run(mediator.send_async(command))

    # Sync database so CLI subprocess can see the logs
    sync_database_for_subprocess()


@given(parsers.parse('{count:d} captain log entries exist for "{agent}"'))
def create_multiple_logs(context, count, agent):
    """Create multiple test logs"""
    import asyncio
    mediator = get_mediator()

    for i in range(count):
        command = LogCaptainEntryCommand(
            player_id=context["player_id"],
            entry_type="operation_started",
            narrative=f"Test log entry {i+1}"
        )
        asyncio.run(mediator.send_async(command))

    # Sync database so CLI subprocess can see the logs
    sync_database_for_subprocess()


@given('captain logs exist with timestamps:')
def create_logs_with_timestamps(context, datatable):
    """Create logs with specific timestamps by inserting directly into DB"""
    from configuration.container import get_database
    db = get_database()

    for row in datatable:
        if len(row) >= 3 and row[0] != 'entry_type':
            entry_type = row[0]
            narrative = row[1]
            timestamp = row[2]

            # Insert directly with specific timestamp
            with db.transaction() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    INSERT INTO captain_logs (player_id, timestamp, entry_type, narrative)
                    VALUES (?, ?, ?, ?)
                """, (context["player_id"], timestamp, entry_type, narrative))

    # Sync database so CLI subprocess can see the logs
    sync_database_for_subprocess()

# Then steps - Assertions

@then('the captain log command should succeed')
def check_log_command_success(context):
    """Verify log command succeeded"""
    assert context["exit_code"] == 0, f"Command failed with exit code {context['exit_code']}\nError: {context['error']}"

@then('the captain log command should fail')
def check_log_command_failure(context):
    """Verify log command failed"""
    assert context["exit_code"] != 0, f"Command should have failed but succeeded"

@then('the logs command should succeed')
def check_logs_command_success(context):
    """Verify logs command succeeded"""
    assert context["exit_code"] == 0, f"Command failed with exit code {context['exit_code']}\nError: {context['error']}"

@then('the logs command should fail')
def check_logs_command_failure(context):
    """Verify logs command failed"""
    assert context["exit_code"] != 0, f"Command should have failed but succeeded"

@then('the log entry should be saved to the database')
def check_log_saved(context):
    """Verify log was saved (by checking success message includes log ID)"""
    # The CLI command returns "✅ Captain log entry #N created successfully"
    # If it succeeded, the log was saved
    assert "Captain log entry #" in context["output"], f"Expected log ID in output, got: {context['output']}"
    assert "created successfully" in context["output"], f"Expected success message in output, got: {context['output']}"

@then(parsers.parse('I should see {count:d} log entries'))
def check_log_count(context, count):
    """Verify number of logs in output"""
    # Count occurrences of entry type in output (each log should have one)
    # This is a simple heuristic - actual implementation may vary
    output_lines = context["output"].strip().split('\n')
    # Filter for non-empty lines that aren't headers
    log_lines = [line for line in output_lines if line.strip() and not line.startswith('===')]
    # Each log entry should have at least a timestamp, type, and narrative line
    # For now, we'll count based on "Entry Type:" occurrences
    entry_count = context["output"].count("Entry Type:")
    assert entry_count == count, f"Expected {count} log entries, found {entry_count}"

@then('the entries should be in reverse chronological order')
def check_reverse_chronological(context):
    """Verify logs are in reverse chronological order"""
    # Extract timestamps from output
    import re
    timestamps = re.findall(r'Timestamp: (\S+)', context["output"])

    # Verify they're in descending order
    for i in range(len(timestamps) - 1):
        assert timestamps[i] >= timestamps[i+1], f"Logs not in reverse chronological order: {timestamps[i]} should be >= {timestamps[i+1]}"

@then(parsers.parse('all entries should have type "{entry_type}"'))
def check_all_entries_have_type(context, entry_type):
    """Verify all logs have specific type"""
    assert entry_type in context["output"], f"Entry type '{entry_type}' not found in output"
    # Check that no other entry types appear
    other_types = ["session_start", "operation_started", "operation_completed", "critical_error", "strategic_decision", "session_end"]
    other_types.remove(entry_type)
    for other_type in other_types:
        # Make sure we're not finding these types in the output
        # (except in enum listings or help text)
        lines_with_type = [line for line in context["output"].split('\n') if other_type in line and 'Entry Type:' in line]
        assert len(lines_with_type) == 0, f"Found unexpected entry type '{other_type}' in filtered results"

@then(parsers.parse('the error should mention "{message}"'))
def check_error_message(context, message):
    """Verify error message contains expected text"""
    error_output = context["error"] + context["output"]  # Check both stdout and stderr
    assert message in error_output, f"Expected error message '{message}' not found in output:\n{error_output}"
