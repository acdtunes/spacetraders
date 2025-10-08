#!/usr/bin/env python3
from __future__ import annotations

"""
Daemon Manager - Python-based background process management

Handles background execution without shell tools (nohup, &, etc.)
"""

import os
import subprocess
from pathlib import Path
from typing import Dict, List, Optional
from datetime import datetime, UTC
import psutil

from ..helpers import paths
from .database import get_database


class DaemonManager:
    """
    Manages background operations as daemon processes using SQLite database (multi-player)

    Usage:
        # Start operation in background
        daemon = DaemonManager(player_id=1)
        daemon.start(
            "mine_SHIP1",
            ["python3", "-m", "spacetraders_bot.cli", "mine", "--ship", "SHIP-1"],
        )

        # Check status
        daemon.status("mine_SHIP1")

        # Stop operation
        daemon.stop("mine_SHIP1")
    """

    def __init__(
        self,
        player_id: int,
        daemon_dir: Optional[Path | str] = None,
        db_path: Optional[Path | str] = None,
    ):
        """
        Initialize daemon manager for a specific player

        Args:
            player_id: Player ID from database
            daemon_dir: Directory for daemon logs (no longer stores PID files)
            db_path: Path to SQLite database
        """
        self.player_id = player_id

        base_dir = Path(daemon_dir) if daemon_dir else paths.DAEMON_DIR
        self.daemon_dir = base_dir
        self.logs_dir = base_dir / "logs"
        self.pids_dir = base_dir / "pids"
        paths.ensure_dirs((self.daemon_dir, self.logs_dir, self.pids_dir))

        database_path = Path(db_path) if db_path else paths.sqlite_path()
        database_path.parent.mkdir(parents=True, exist_ok=True)
        self.db = get_database(str(database_path))

        # Auto-retrieve player info and token
        with self.db.connection() as conn:
            player = self.db.get_player_by_id(conn, player_id)
            if player:
                self.agent_symbol = player['agent_symbol']
                self.token = player['token']
            else:
                self.agent_symbol = None
                self.token = None

        self._api_client = None  # Lazy-loaded API client

    def _fetch_daemon(self, daemon_id: str) -> Optional[Dict]:
        """Return persisted daemon details for this player."""
        with self.db.connection() as conn:
            return self.db.get_daemon(conn, self.player_id, daemon_id)

    def _update_daemon_status(self, daemon_id: str, status: str,
                              stopped_at: Optional[str] = None) -> None:
        """Persist daemon status changes with optional stop timestamp."""
        with self.db.transaction() as conn:
            self.db.update_daemon_status(
                conn,
                player_id=self.player_id,
                daemon_id=daemon_id,
                status=status,
                stopped_at=stopped_at,
            )

    def get_api_client(self):
        """
        Get API client with stored token (lazy-loaded)

        Returns:
            APIClient instance configured with player's token

        Usage:
            api = daemon_manager.get_api_client()
            ship = ShipController(api, "SHIP-1")
        """
        if self._api_client is None and self.token:
            # Import here to avoid circular dependencies
            from spacetraders_bot.core.api_client import APIClient
            self._api_client = APIClient(token=self.token)
        return self._api_client

    @property
    def api(self):
        """Convenient property access to API client"""
        return self.get_api_client()

    def start(self, daemon_id: str, command: List[str], cwd: Optional[str] = None) -> bool:
        """
        Start operation as background daemon

        Args:
            daemon_id: Unique daemon identifier
            command: Command to execute (e.g., ["python3", "bot.py", "mine"])
            cwd: Working directory (default: current directory)

        Returns:
            True if started successfully
        """
        # Check if already running
        if self.is_running(daemon_id):
            print(f"Daemon {daemon_id} is already running (PID: {self.get_pid(daemon_id)})")
            return False

        log_file, err_file = self._prepare_log_files(daemon_id, command)
        stdout_handle, stderr_handle = self._open_log_streams(log_file, err_file)

        # Start process in background
        process = subprocess.Popen(
            command,
            stdout=stdout_handle,
            stderr=stderr_handle,
            cwd=cwd or os.getcwd(),
            start_new_session=True  # Detach from parent
        )

        # Save to database
        with self.db.transaction() as conn:
            self.db.create_daemon(
                conn,
                player_id=self.player_id,
                daemon_id=daemon_id,
                pid=process.pid,
                command=command,
                log_file=str(log_file),
                err_file=str(err_file)
            )

        print(f"✅ Started daemon {daemon_id} (PID: {process.pid})")
        print(f"   Logs: {log_file}")
        print(f"   Errors: {err_file}")

        return True

    def _prepare_log_files(self, daemon_id: str, command: List[str]) -> tuple[Path, Path]:
        """Ensure log files exist and append a start marker."""
        log_file = self.logs_dir / f"{daemon_id}.log"
        err_file = self.logs_dir / f"{daemon_id}.err"

        timestamp = datetime.now(UTC).isoformat()
        with open(log_file, 'a') as stream:
            stream.write(f"\n{'='*70}\n")
            stream.write(f"Daemon {daemon_id} started at {timestamp}\n")
            stream.write(f"Command: {' '.join(command)}\n")
            stream.write(f"{'='*70}\n\n")

        # Touch error file so the path exists even before writes
        err_file.touch(exist_ok=True)

        return log_file, err_file

    def _open_log_streams(self, log_file: Path, err_file: Path) -> tuple:
        """Return file handles for subprocess stdout and stderr."""
        stdout_handle = open(log_file, 'a')
        stderr_handle = open(err_file, 'a')
        return stdout_handle, stderr_handle

    def stop(self, daemon_id: str, timeout: int = 10) -> bool:
        """
        Stop running daemon

        Args:
            daemon_id: Daemon identifier
            timeout: Seconds to wait for graceful shutdown

        Returns:
            True if stopped successfully
        """
        pid = self.get_pid(daemon_id)
        if not pid:
            print(f"Daemon {daemon_id} is not running")
            return False

        try:
            process = psutil.Process(pid)

            # Try graceful shutdown (SIGTERM)
            print(f"Stopping daemon {daemon_id} (PID: {pid})...")
            process.terminate()

            # Wait for process to exit
            try:
                process.wait(timeout=timeout)
                print(f"✅ Daemon {daemon_id} stopped gracefully")
                status = "stopped"
            except psutil.TimeoutExpired:
                # Force kill if still running
                print(f"⚠️  Daemon did not stop gracefully, force killing...")
                process.kill()
                process.wait(timeout=5)
                print(f"✅ Daemon {daemon_id} force killed")
                status = "killed"

            # Update database
            self._update_daemon_status(
                daemon_id,
                status,
                stopped_at=datetime.now(UTC).isoformat(),
            )

            return True

        except psutil.NoSuchProcess:
            print(f"Process {pid} not found (already stopped)")

            # Update database
            self._update_daemon_status(
                daemon_id,
                "crashed",
                stopped_at=datetime.now(UTC).isoformat(),
            )

            return True

        except Exception as e:
            print(f"❌ Error stopping daemon: {e}")
            return False

    def get_pid(self, daemon_id: str) -> Optional[int]:
        """Get PID of running daemon"""
        daemon = self._fetch_daemon(daemon_id)
        return daemon['pid'] if daemon else None

    def is_running(self, daemon_id: str) -> bool:
        """Check if daemon is running"""
        pid = self.get_pid(daemon_id)
        if not pid:
            return False

        try:
            process = psutil.Process(pid)
            is_running = process.is_running()

            # If not running, update database
            if not is_running:
                self._update_daemon_status(
                    daemon_id,
                    "crashed",
                    stopped_at=datetime.now(UTC).isoformat(),
                )

            return is_running

        except psutil.NoSuchProcess:
            # Update database to mark as crashed
            self._update_daemon_status(
                daemon_id,
                "crashed",
                stopped_at=datetime.now(UTC).isoformat(),
            )
            return False

    def status(self, daemon_id: str) -> Optional[Dict]:
        """
        Get daemon status

        Returns:
            Status dict or None if not found
        """
        daemon = self._fetch_daemon(daemon_id)

        if not daemon:
            return None

        pid = daemon['pid']
        is_running = False
        cpu_percent = 0
        memory_mb = 0
        runtime = None

        try:
            process = psutil.Process(pid)
            if process.is_running():
                is_running = True
                cpu_percent = process.cpu_percent(interval=0.1)
                memory_mb = process.memory_info().rss / 1024 / 1024

                # Calculate runtime
                started_at = datetime.fromisoformat(daemon['started_at'].replace('Z', '+00:00'))
                runtime = (datetime.now(UTC) - started_at).total_seconds()

        except psutil.NoSuchProcess:
            pass

        return {
            "daemon_id": daemon_id,
            "pid": pid,
            "is_running": is_running,
            "cpu_percent": cpu_percent,
            "memory_mb": memory_mb,
            "runtime_seconds": runtime,
            "started_at": daemon.get('started_at'),
            "stopped_at": daemon.get('stopped_at'),
            "status": daemon.get('status'),
            "command": daemon.get('command'),
            "log_file": daemon.get('log_file'),
            "err_file": daemon.get('err_file')
        }

    def list_all(self) -> List[Dict]:
        """List all daemons for this player"""
        with self.db.connection() as conn:
            daemons_data = self.db.list_daemons(conn, self.player_id)
            daemons = []

            for daemon in daemons_data:
                daemon_id = daemon['daemon_id']
                status = self.status(daemon_id)
                if status:
                    daemons.append(status)

            return sorted(daemons, key=lambda x: x.get('started_at', ''), reverse=True)

    def tail_logs(self, daemon_id: str, lines: int = 20):
        """Show recent log output"""
        daemon = self._fetch_daemon(daemon_id)

        if not daemon:
            print(f"Daemon {daemon_id} not found")
            return

        log_file = Path(daemon['log_file'])

        if not log_file.exists():
            print(f"Log file not found: {log_file}")
            return

        # Read last N lines
        with open(log_file, 'r') as f:
            all_lines = f.readlines()
            recent_lines = all_lines[-lines:]

        print(f"Last {lines} lines from {log_file}:")
        print("".join(recent_lines))

    def cleanup_stopped(self):
        """Remove database records for stopped daemons"""
        cleaned = 0

        with self.db.connection() as conn:
            daemons = self.db.list_daemons(conn, self.player_id)

            for daemon in daemons:
                daemon_id = daemon['daemon_id']

                if not self.is_running(daemon_id):
                    # Delete from database
                    with self.db.transaction() as trans_conn:
                        self.db.delete_daemon(trans_conn, self.player_id, daemon_id)
                    cleaned += 1

        print(f"Cleaned up {cleaned} stopped daemon(s)")


def daemonize_current_process():
    """
    Daemonize the current Python process

    Call this at the start of your operation to detach from terminal
    """
    # Fork first child
    try:
        pid = os.fork()
        if pid > 0:
            # Parent exits
            sys.exit(0)
    except OSError as e:
        print(f"Fork #1 failed: {e}")
        sys.exit(1)

    # Decouple from parent environment
    os.chdir('/')
    os.setsid()
    os.umask(0)

    # Fork second child
    try:
        pid = os.fork()
        if pid > 0:
            # First child exits
            sys.exit(0)
    except OSError as e:
        print(f"Fork #2 failed: {e}")
        sys.exit(1)

    # Redirect standard file descriptors
    sys.stdout.flush()
    sys.stderr.flush()

    # Note: Caller should redirect stdout/stderr to log files after this


if __name__ == "__main__":
    # CLI for daemon management
    import argparse

    parser = argparse.ArgumentParser(description="Daemon Manager CLI")
    subparsers = parser.add_subparsers(dest='action', help='Action to perform')

    # Start daemon
    start_parser = subparsers.add_parser('start', help='Start daemon')
    start_parser.add_argument('daemon_id', help='Daemon ID')
    start_parser.add_argument('command', nargs='+', help='Command to run')

    # Stop daemon
    stop_parser = subparsers.add_parser('stop', help='Stop daemon')
    stop_parser.add_argument('daemon_id', help='Daemon ID')

    # Status
    status_parser = subparsers.add_parser('status', help='Get daemon status')
    status_parser.add_argument('daemon_id', help='Daemon ID')

    # List all
    list_parser = subparsers.add_parser('list', help='List all daemons')

    # Tail logs
    tail_parser = subparsers.add_parser('logs', help='Show daemon logs')
    tail_parser.add_argument('daemon_id', help='Daemon ID')
    tail_parser.add_argument('--lines', type=int, default=20, help='Number of lines')

    # Cleanup
    cleanup_parser = subparsers.add_parser('cleanup', help='Clean up stopped daemons')

    args = parser.parse_args()

    manager = DaemonManager()

    if args.action == 'start':
        manager.start(args.daemon_id, args.command)

    elif args.action == 'stop':
        manager.stop(args.daemon_id)

    elif args.action == 'status':
        status = manager.status(args.daemon_id)
        if status:
            print(json.dumps(status, indent=2))
        else:
            print(f"Daemon {args.daemon_id} not found")

    elif args.action == 'list':
        daemons = manager.list_all()
        for daemon in daemons:
            running = "✅" if daemon['is_running'] else "❌"
            print(f"{running} {daemon['daemon_id']:30} PID:{daemon['pid']:6} "
                  f"CPU:{daemon['cpu_percent']:5.1f}% MEM:{daemon['memory_mb']:6.1f}MB")

    elif args.action == 'logs':
        manager.tail_logs(args.daemon_id, args.lines)

    elif args.action == 'cleanup':
        manager.cleanup_stopped()

    else:
        parser.print_help()
