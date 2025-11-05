#!/usr/bin/env python3
"""Compatibility wrapper for the MCP bridge entry point."""

from spacetraders_bot.integrations.mcp_bridge import main

if __name__ == "__main__":
    raise SystemExit(main())
