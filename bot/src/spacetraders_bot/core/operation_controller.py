#!/usr/bin/env python3
from __future__ import annotations

"""
Operation Controller - Persistent state management for long-running operations

Provides:
- Checkpoint/resume capability
- Crash recovery
- Progress monitoring
- External control (pause/resume/cancel)
"""

import json
import logging
import time
from datetime import datetime
from enum import Enum
from pathlib import Path
from typing import Any, Dict, Optional

from ..helpers import paths

logger = logging.getLogger(__name__)


class OperationStatus(Enum):
    """Operation status states"""
    PENDING = "pending"
    RUNNING = "running"
    PAUSED = "paused"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


class OperationController:
    """
    Controls long-running operations with persistence and recovery

    Usage:
        controller = OperationController(operation_id="mine_SHIP1_001")

        # Start or resume operation
        if controller.can_resume():
            controller.resume()
        else:
            controller.start({"ship": "SHIP-1", "destination": "X1-A"})

        # Save checkpoints during execution
        controller.checkpoint({"current_step": 3, "fuel": 200})

        # Check for external control commands
        if controller.should_pause():
            controller.pause()
            break
    """

    def __init__(self, operation_id: str, state_dir: Optional[Path | str] = None):
        """
        Initialize operation controller

        Args:
            operation_id: Unique operation identifier
            state_dir: Directory for state persistence
        """
        self.operation_id = operation_id
        self.state_dir = Path(state_dir) if state_dir else paths.STATE_DIR
        self.state_dir.mkdir(parents=True, exist_ok=True)
        self.state_file = self.state_dir / f"{operation_id}.json"

        # Load existing state or initialize
        self.state = self._load_state()

    def _load_state(self) -> Dict[str, Any]:
        """Load operation state from disk"""
        if self.state_file.exists():
            with open(self.state_file, 'r') as f:
                return json.load(f)

        # Initialize new state
        return {
            "operation_id": self.operation_id,
            "status": OperationStatus.PENDING.value,
            "created_at": datetime.utcnow().isoformat(),
            "updated_at": datetime.utcnow().isoformat(),
            "checkpoints": [],
            "metadata": {},
            "error": None
        }

    def _save_state(self):
        """Persist state to disk"""
        self.state["updated_at"] = datetime.utcnow().isoformat()
        with open(self.state_file, 'w') as f:
            json.dump(self.state, f, indent=2)

    def start(self, metadata: Dict[str, Any]):
        """
        Start new operation

        Args:
            metadata: Operation metadata (ship, route, params, etc.)
        """
        self.state["status"] = OperationStatus.RUNNING.value
        self.state["metadata"] = metadata
        self.state["started_at"] = datetime.utcnow().isoformat()
        self._save_state()
        logger.info(f"Operation {self.operation_id} started")

    def checkpoint(self, checkpoint_data: Dict[str, Any]):
        """
        Save checkpoint during execution

        Args:
            checkpoint_data: Current state (step, fuel, location, etc.)
        """
        checkpoint = {
            "timestamp": datetime.utcnow().isoformat(),
            "data": checkpoint_data
        }
        self.state["checkpoints"].append(checkpoint)
        self._save_state()
        logger.debug(f"Checkpoint saved: {checkpoint_data}")

    def get_last_checkpoint(self) -> Optional[Dict[str, Any]]:
        """Get most recent checkpoint"""
        if self.state["checkpoints"]:
            return self.state["checkpoints"][-1]["data"]
        return None

    def can_resume(self) -> bool:
        """Check if operation can be resumed"""
        status = self.state["status"]
        has_checkpoints = len(self.state["checkpoints"]) > 0
        return status in [OperationStatus.PAUSED.value, OperationStatus.RUNNING.value] and has_checkpoints

    def resume(self) -> Optional[Dict[str, Any]]:
        """
        Resume operation from last checkpoint

        Returns:
            Last checkpoint data to resume from
        """
        if not self.can_resume():
            logger.warning(f"Cannot resume operation {self.operation_id}")
            return None

        self.state["status"] = OperationStatus.RUNNING.value
        self.state["resumed_at"] = datetime.utcnow().isoformat()
        self._save_state()

        checkpoint = self.get_last_checkpoint()
        logger.info(f"Resuming operation {self.operation_id} from checkpoint: {checkpoint}")
        return checkpoint

    def pause(self):
        """Pause operation (can be resumed later)"""
        self.state["status"] = OperationStatus.PAUSED.value
        self._save_state()
        logger.info(f"Operation {self.operation_id} paused")

    def complete(self, result: Dict[str, Any] = None):
        """Mark operation as completed"""
        self.state["status"] = OperationStatus.COMPLETED.value
        self.state["completed_at"] = datetime.utcnow().isoformat()
        if result:
            self.state["result"] = result
        self._save_state()
        logger.info(f"Operation {self.operation_id} completed")

    def fail(self, error: str):
        """Mark operation as failed"""
        self.state["status"] = OperationStatus.FAILED.value
        self.state["error"] = error
        self.state["failed_at"] = datetime.utcnow().isoformat()
        self._save_state()
        logger.error(f"Operation {self.operation_id} failed: {error}")

    def cancel(self):
        """Cancel operation"""
        self.state["status"] = OperationStatus.CANCELLED.value
        self.state["cancelled_at"] = datetime.utcnow().isoformat()
        self._save_state()
        logger.info(f"Operation {self.operation_id} cancelled")

    def should_pause(self) -> bool:
        """
        Check if external pause command received

        Reloads state from disk to check for external control commands
        """
        # Reload state to check for external changes
        fresh_state = self._load_state()
        return fresh_state.get("control_command") == "pause"

    def should_cancel(self) -> bool:
        """Check if external cancel command received"""
        fresh_state = self._load_state()
        return fresh_state.get("control_command") == "cancel"

    def get_progress(self) -> Dict[str, Any]:
        """
        Get operation progress

        Returns:
            Progress information (status, checkpoints, duration, etc.)
        """
        checkpoints = len(self.state["checkpoints"])

        duration = None
        if "started_at" in self.state:
            start = datetime.fromisoformat(self.state["started_at"])
            now = datetime.utcnow()
            duration = (now - start).total_seconds()

        return {
            "operation_id": self.operation_id,
            "status": self.state["status"],
            "checkpoints": checkpoints,
            "duration_seconds": duration,
            "last_checkpoint": self.get_last_checkpoint(),
            "error": self.state.get("error")
        }

    def cleanup(self):
        """Remove state file after successful completion"""
        if self.state_file.exists():
            self.state_file.unlink()
            logger.info(f"Cleaned up state for {self.operation_id}")


def send_control_command(operation_id: str, command: str, state_dir: Optional[Path | str] = None):
    """
    Send external control command to running operation

    Args:
        operation_id: Target operation ID
        command: 'pause' or 'cancel'
        state_dir: State directory
    """
    base_dir = Path(state_dir) if state_dir else paths.STATE_DIR
    state_file = base_dir / f"{operation_id}.json"

    if not state_file.exists():
        logger.error(f"Operation {operation_id} not found")
        return False

    with open(state_file, 'r') as f:
        state = json.load(f)

    state["control_command"] = command
    state["control_timestamp"] = datetime.utcnow().isoformat()

    with open(state_file, 'w') as f:
        json.dump(state, f, indent=2)

    logger.info(f"Sent {command} command to {operation_id}")
    return True


def list_operations(state_dir: Optional[Path | str] = None) -> list:
    """
    List all operations

    Returns:
        List of operation states
    """
    state_path = Path(state_dir) if state_dir else paths.STATE_DIR
    if not state_path.exists():
        return []

    operations = []
    for state_file in state_path.glob("*.json"):
        with open(state_file, 'r') as f:
            operations.append(json.load(f))

    return sorted(operations, key=lambda x: x["updated_at"], reverse=True)
