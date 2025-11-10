# SpaceTraders Bot MCP Server (v3.0)

MCP (Model Context Protocol) server that exposes the SpaceTraders bot CLI commands as tools for AI agents like Claude.

## Overview

This MCP server provides a clean interface to all SpaceTraders bot operations through the CLI. It supports:

- **Player Management**: Register, list, and query player/agent information
- **Ship Management**: List ships, get ship details, sync from API
- **Navigation**: Navigate, dock, orbit, refuel ships with automatic route planning
- **Daemon Operations**: Manage background containers (list, inspect, stop, logs)
- **Configuration**: Set default players for simplified commands

All tools directly invoke the Python CLI, ensuring consistency with the bot's command-line interface.

## Installation

```bash
cd mcp
npm install
npm run build
```

## Prerequisites

- Python 3.12+ with bot dependencies installed via `uv sync` (see `../pyproject.toml`)
- PostgreSQL database running at `localhost:5432/spacetraders` (configured via `DATABASE_URL` in `../.env`)
- At least one player registered in the database

## Running

The MCP server runs on stdio and is typically invoked by an MCP client like Claude Desktop:

```bash
npm start
```

## Claude Desktop Configuration

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "spacetraders": {
      "command": "node",
      "args": [
        "/absolute/path/to/spacetraders/bot/mcp/build/index.js"
      ],
      "env": {
        "MCP_PYTHON_BIN": "/absolute/path/to/spacetraders/bot/.venv/bin/python",
        "DATABASE_URL": "postgresql://spacetraders:dev_password@localhost:5432/spacetraders"
      }
    }
  }
}
```

**Environment Variables:**
- `MCP_PYTHON_BIN`: Path to Python executable (defaults to `/usr/bin/python3`)
  - For uv virtual environments: Use `/path/to/project/.venv/bin/python`
- `DATABASE_URL`: PostgreSQL connection string (required for production database access)
  - Format: `postgresql://user:password@host:port/database`
- `PYTHON_BIN`: Alternative to `MCP_PYTHON_BIN`

## Available Tools

### Player Management

- **player_register**: Register a new agent with token
- **player_list**: List all registered players
- **player_info**: Get detailed player information

### Ship Management

- **ship_list**: List all ships for a player
- **ship_info**: Get detailed ship information

### Navigation

- **navigate**: Navigate ship to destination (automatic route planning, fuel management)
- **dock**: Dock ship at current location
- **orbit**: Put ship into orbit
- **refuel**: Refuel ship at current location
- **plan_route**: Plan route without executing (shows fuel requirements, time estimates)

### Data Sync

- **sync_ships**: Sync ship data from SpaceTraders API to local database

### Daemon Operations

- **daemon_list**: List all running background containers
- **daemon_inspect**: Inspect container details
- **daemon_stop**: Stop a running container
- **daemon_remove**: Remove a stopped container
- **daemon_logs**: Get container logs from database

### Configuration

- **config_show**: Show current CLI configuration
- **config_set_player**: Set default player (simplifies subsequent commands)
- **config_clear_player**: Clear default player

## Tool Usage Examples

**Register a player:**
```json
{
  "tool": "player_register",
  "args": {
    "agent_symbol": "CHROMESAMURAI",
    "token": "eyJhbGci..."
  }
}
```

**List ships:**
```json
{
  "tool": "ship_list",
  "args": {
    "agent": "CHROMESAMURAI"
  }
}
```

**Navigate a ship:**
```json
{
  "tool": "navigate",
  "args": {
    "ship": "CHROMESAMURAI-1",
    "destination": "X1-GZ7-B1"
  }
}
```

**Set default player (simplifies future commands):**
```json
{
  "tool": "config_set_player",
  "args": {
    "agent_symbol": "CHROMESAMURAI"
  }
}
```

After setting a default player, you can omit `player_id` or `agent` from most commands.

## Architecture

The MCP server is a thin wrapper around the Python CLI:

```
MCP Client (Claude)
  → MCP Server (Node.js/TypeScript)
    → SpaceTraders CLI (Python)
      → Bot Application Layer (CQRS)
        → Domain Logic + API Client
```

**Benefits:**
- Direct mapping to CLI commands (no duplicate logic)
- Consistent behavior between MCP and CLI
- Automatic support for new CLI features
- Simple, maintainable codebase

## Development

Watch mode for development:
```bash
npm run dev
```

Build for production:
```bash
npm run build
```

## Error Handling

The MCP server handles:
- Command timeouts (5 minutes max)
- Python execution errors
- CLI validation errors
- Database connection issues

All errors are returned to the MCP client with descriptive messages.

## Version History

- **v3.0.0**: Complete rewrite to expose CLI commands as tools
- **v2.0.0**: Bridge-based architecture (deprecated)
- **v1.0.0**: Initial release

## Support

For issues or questions:
- Check the main bot documentation in `/docs`
- Review CLI help: `uv run python -m spacetraders.adapters.primary.cli.main --help`
- Ensure Python dependencies are installed: `uv sync`
