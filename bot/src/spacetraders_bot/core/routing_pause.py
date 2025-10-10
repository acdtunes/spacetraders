#!/usr/bin/env python3
from __future__ import annotations

"""
Simple persistence layer for routing pause state.
"""

import json
from pathlib import Path
from typing import Dict, Optional

from ..helpers import paths

PAUSE_FILE = paths.VAR_DIR / "routing_pause.json"


def _ensure_directory():
    PAUSE_FILE.parent.mkdir(parents=True, exist_ok=True)


def pause(reason: str, metadata: Optional[Dict[str, str]] = None) -> None:
    """Persist a pause flag with reason for downstream operations."""
    _ensure_directory()
    payload = {
        "paused": True,
        "reason": reason,
        "metadata": metadata or {},
    }
    with open(PAUSE_FILE, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2)


def resume() -> None:
    """Clear pause flag so operations can continue."""
    if PAUSE_FILE.exists():
        PAUSE_FILE.unlink()


def is_paused() -> bool:
    """Check if routing operations are currently paused."""
    return PAUSE_FILE.exists()


def get_pause_details() -> Optional[Dict]:
    """Return pause details if present."""
    if not PAUSE_FILE.exists():
        return None
    with open(PAUSE_FILE, "r", encoding="utf-8") as handle:
        try:
            return json.load(handle)
        except json.JSONDecodeError:
            return {"paused": True, "reason": "Invalid pause file"}

