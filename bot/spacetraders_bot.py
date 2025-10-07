#!/usr/bin/env python3
"""Compatibility wrapper for the legacy CLI path."""

from __future__ import annotations

from pathlib import Path

# Expose the installed package when this shim is imported
_PACKAGE_DIR = Path(__file__).resolve().parent / "src" / "spacetraders_bot"
if _PACKAGE_DIR.exists():
    if __spec__ is not None:  # pragma: no cover - defensive
        __spec__.submodule_search_locations = [str(_PACKAGE_DIR)]
    __path__ = [str(_PACKAGE_DIR)]  # type: ignore[name-defined]

from spacetraders_bot.cli.main import main

if __name__ == "__main__":
    main()
