#!/usr/bin/env python3
"""
Mock Daemon Manager for testing ship assignments

Simulates daemon manager without actually running processes
"""

from typing import Dict, List, Optional
from datetime import datetime, timezone


class MockDaemonManager:
    """Mock daemon manager for testing"""

    def __init__(self):
        self.daemons: Dict[str, Dict] = {}
        self.next_pid = 10000

    def start(self, daemon_id: str, command: List[str], cwd: Optional[str] = None) -> bool:
        """Simulate starting a daemon"""
        if daemon_id in self.daemons and self.daemons[daemon_id]['is_running']:
            return False

        self.daemons[daemon_id] = {
            'daemon_id': daemon_id,
            'pid': self.next_pid,
            'command': command,
            'is_running': True,
            'started_at': datetime.now(timezone.utc).isoformat(),
            'cpu_percent': 2.5,
            'memory_mb': 45.0,
            'runtime_seconds': 0,
            'log_file': f'/fake/logs/{daemon_id}.log',
            'err_file': f'/fake/logs/{daemon_id}.err'
        }

        self.next_pid += 1
        return True

    def stop(self, daemon_id: str, timeout: int = 10) -> bool:
        """Simulate stopping a daemon"""
        if daemon_id not in self.daemons:
            return False

        # Check if daemon is stoppable (for testing unstoppable daemons)
        if not self.daemons[daemon_id].get('stoppable', True):
            return False

        self.daemons[daemon_id]['is_running'] = False
        return True

    def get_pid(self, daemon_id: str) -> Optional[int]:
        """Get PID of daemon"""
        if daemon_id in self.daemons:
            return self.daemons[daemon_id]['pid']
        return None

    def is_running(self, daemon_id: str) -> bool:
        """Check if daemon is running"""
        if daemon_id not in self.daemons:
            return False
        return self.daemons[daemon_id]['is_running']

    def status(self, daemon_id: str) -> Optional[Dict]:
        """Get daemon status"""
        if daemon_id not in self.daemons:
            return None
        return self.daemons[daemon_id].copy()

    def list_all(self) -> List[Dict]:
        """List all daemons"""
        return [d.copy() for d in self.daemons.values()]

    def tail_logs(self, daemon_id: str, lines: int = 20):
        """Simulate showing logs (no-op for mock)"""
        pass

    def cleanup_stopped(self):
        """Remove stopped daemons"""
        stopped = [did for did, d in self.daemons.items() if not d['is_running']]
        for daemon_id in stopped:
            del self.daemons[daemon_id]

    # Test helper methods
    def set_daemon_running(self, daemon_id: str, running: bool = True):
        """Helper to set daemon running state"""
        if daemon_id in self.daemons:
            self.daemons[daemon_id]['is_running'] = running

    def set_stoppable(self, daemon_id: str, stoppable: bool = True):
        """Helper to make daemon stoppable/unstoppable for testing"""
        if daemon_id in self.daemons:
            self.daemons[daemon_id]['stoppable'] = stoppable

    def clear_all(self):
        """Clear all daemons (test cleanup)"""
        self.daemons.clear()
