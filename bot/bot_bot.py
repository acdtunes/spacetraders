#!/usr/bin/env python3
"""Backward-compatible entry point for the unified bot CLI."""

from spacetraders_bot.cli.main import main

if __name__ == "__main__":
    raise SystemExit(main())
