"""Container types and enums for daemon system"""
import asyncio
from enum import Enum
from dataclasses import dataclass
from typing import Optional, Dict, Any
from datetime import datetime


class ContainerStatus(Enum):
    """Container lifecycle status"""
    STARTING = "STARTING"
    RUNNING = "RUNNING"
    STOPPING = "STOPPING"
    STOPPED = "STOPPED"
    FAILED = "FAILED"


class RestartPolicy(Enum):
    """Container restart policy"""
    NO = "no"
    ON_FAILURE = "on-failure"
    ALWAYS = "always"
    UNLESS_STOPPED = "unless-stopped"


@dataclass
class ContainerInfo:
    """Container metadata and state"""
    container_id: str
    player_id: int
    container_type: str
    status: ContainerStatus
    restart_policy: RestartPolicy
    restart_count: int
    max_restarts: int
    config: Dict[str, Any]
    task: Optional[asyncio.Task]
    logs: list
    started_at: Optional[datetime]
    stopped_at: Optional[datetime]
    exit_code: Optional[int]
    exit_reason: Optional[str]
