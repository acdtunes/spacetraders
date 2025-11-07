"""BDD steps for daemon CLI execution"""
import subprocess
import sys
import time
import signal
import os
from pathlib import Path
from pytest_bdd import scenarios, when, then, parsers

scenarios('../../features/daemon/daemon_cli.feature')


@when(parsers.parse('I run the daemon module with "{command}"'))
def run_daemon_module(context, command):
    """Execute daemon module"""
    # Add environment setup
    env = os.environ.copy()
    env['PYTHONPATH'] = 'src'
    env['SPACETRADERS_TOKEN'] = 'test-token'
    env['PYTHONUNBUFFERED'] = '1'  # Force unbuffered output
    # Use in-memory database for daemon CLI test to avoid recovery of production containers
    env['SPACETRADERS_DB_PATH'] = ':memory:'
    # Use test socket path to avoid interfering with production daemon
    env['SPACETRADERS_DAEMON_SOCKET'] = 'var/test-daemon.sock'

    # Replace 'python' with sys.executable
    cmd_parts = command.split()
    if cmd_parts[0] == 'python':
        cmd_parts[0] = sys.executable

    # Start the process
    proc = subprocess.Popen(
        cmd_parts,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        preexec_fn=lambda: signal.signal(signal.SIGINT, signal.SIG_IGN)
    )

    context['daemon_process'] = proc

    # Give it 2 seconds to start and flush logs
    time.sleep(2)

    # Send SIGTERM to stop
    proc.terminate()

    try:
        stdout, stderr = proc.communicate(timeout=2)
        context['daemon_stdout'] = stdout.decode()
        context['daemon_stderr'] = stderr.decode()
        context['daemon_returncode'] = proc.returncode
    except subprocess.TimeoutExpired:
        proc.kill()
        stdout, stderr = proc.communicate()
        context['daemon_stdout'] = stdout.decode()
        context['daemon_stderr'] = stderr.decode()
        context['daemon_returncode'] = proc.returncode

    # Cleanup test socket
    socket_path = Path("var/test-daemon.sock")
    if socket_path.exists():
        socket_path.unlink()


@then("the module should start without errors")
def module_starts_without_errors(context):
    """Verify module started"""
    stderr = context.get('daemon_stderr', '')

    # Filter out expected messages:
    # - RuntimeWarning about sys.modules
    # - INFO logs (these are normal)
    # - CancelledError traceback (expected when we send SIGTERM)
    # - Traceback lines (from clean shutdown)
    filtered_errors = [
        line for line in stderr.split('\n')
        if line and
        'RuntimeWarning' not in line and
        'found in sys.modules' not in line and
        ' - INFO - ' not in line and
        'CancelledError' not in line and
        'Traceback' not in line and
        'File "' not in line and
        'asyncio' not in line and
        not line.strip().startswith('return') and
        not line.strip().startswith('await') and
        not line.strip().startswith('^') and  # Traceback markers
        not line.strip().startswith('~') and  # Python 3.13+ traceback markers
        not line.strip().startswith('main()')  # Function calls in traceback
    ]

    assert not filtered_errors, f"Module had unexpected errors: {filtered_errors}"


@then("the main function should be called")
def main_function_called(context):
    """Verify main was called by checking for daemon startup"""
    stderr = context.get('daemon_stderr', '')
    stdout = context.get('daemon_stdout', '')

    # The daemon should log either:
    # - "Daemon server started" (normal startup)
    # - "Recovering N RUNNING container(s)" (startup with recovery)
    # Both indicate main() was called and startup sequence began
    daemon_started = 'Daemon server started' in stderr
    recovery_started = 'Recovering' in stderr and 'container' in stderr

    main_was_called = daemon_started or recovery_started

    assert main_was_called, \
        f"Main function was not called - no daemon startup or recovery log found.\n" \
        f"Return code: {context['daemon_returncode']}\n" \
        f"stderr: {stderr}\n" \
        f"stdout: {stdout}"
