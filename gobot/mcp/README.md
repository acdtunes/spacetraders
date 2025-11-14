# SpaceTraders Go Bot MCP Server (v3.0)

MCP (Model Context Protocol) server that exposes the SpaceTraders Go bot daemon operations as tools for AI agents like Claude.

## Overview

This MCP server provides a clean interface to all SpaceTraders Go bot operations through the daemon's Unix socket. It supports:

- **Player Management**: Register, list, and query player/agent information
- **Ship Management**: List ships, get ship details, sync from API
- **Navigation**: Navigate, dock, orbit, refuel ships with automatic route planning
- **Daemon Operations**: Manage background containers (list, inspect, stop, logs)
- **Configuration**: Set default players for simplified commands

All tools communicate directly with the Go daemon via Unix socket, ensuring real-time access to bot operations.

## Installation

```bash
cd mcp
npm install
npm run build
```

## Prerequisites

- SpaceTraders Go daemon running (see `../README.md` for setup)
- PostgreSQL database running at `localhost:5432/spacetraders` (configured via `DATABASE_URL`)
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
    "spacetraders-bot": {
      "command": "node",
      "args": [
        "/absolute/path/to/spacetraders/gobot/mcp/build/index.js"
      ],
      "env": {
        "DATABASE_URL": "postgresql://spacetraders:dev_password@localhost:5432/spacetraders"
      }
    }
  }
}
```

**Environment Variables:**
- `DATABASE_URL`: PostgreSQL connection string (required for production database access)
  - Format: `postgresql://user:password@host:port/database`
  - Default: `postgresql://spacetraders:dev_password@localhost:5432/spacetraders`
- `SPACETRADERS_DAEMON_SOCKET`: Path to daemon Unix socket
  - Default: `/tmp/spacetraders-daemon.sock`

## Available Tools (13 Total)

All tools communicate directly with the Go daemon via Unix socket.

### Container Management (5 tools)

- **daemon_list**: List all running background containers
- **daemon_inspect**: Inspect container details
- **daemon_stop**: Stop a running container
- **daemon_remove**: Remove a stopped container
- **daemon_logs**: Get container logs from database

### Ship Operations (4 tools)

- **navigate**: Navigate ship to destination (automatic route planning, fuel management)
- **dock**: Dock ship at current location
- **orbit**: Put ship into orbit
- **refuel**: Refuel ship at current location

### Shipyard Operations (2 tools)

- **shipyard_purchase**: Purchase single ship from shipyard
- **shipyard_batch_purchase**: Batch purchase multiple ships within budget

### Fleet Operations (1 tool)

- **scout_markets**: VRP-optimized fleet distribution for market scouting

### Workflow Operations (1 tool)

- **contract_batch_workflow**: Automated contract negotiation, fulfillment, and execution

## Tool Usage Examples

**List running containers:**
```json
{
  "tool": "daemon_list",
  "args": {
    "player_id": 1
  }
}
```

**Navigate a ship:**
```json
{
  "tool": "navigate",
  "args": {
    "ship": "CHROMESAMURAI-1",
    "destination": "X1-GZ7-B1",
    "player_id": 1
  }
}
```

**Scout markets with VRP optimization:**
```json
{
  "tool": "scout_markets",
  "args": {
    "ships": "SCOUT-1,SCOUT-2,SCOUT-3",
    "system": "X1-GZ7",
    "markets": "X1-GZ7-A1,X1-GZ7-B2,X1-GZ7-C3",
    "iterations": -1,
    "player_id": 1
  }
}
```

**Get container logs:**
```json
{
  "tool": "daemon_logs",
  "args": {
    "container_id": "navigate-12abc34d",
    "player_id": 1,
    "limit": 50
  }
}
```

## Architecture

The MCP server communicates directly with the Go daemon via Unix socket:

```
MCP Client (Claude)
  → MCP Server (Node.js/TypeScript)
    → Unix Socket (/tmp/spacetraders-daemon.sock)
      → SpaceTraders Go Daemon (gRPC)
        → Application Layer (CQRS)
          → Domain Logic + API Client
```

**Benefits:**
- Direct socket communication (no process spawning overhead)
- Real-time access to daemon operations
- Consistent with CLI behavior (both use same daemon)
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

- **v4.0.0**: Daemon-only architecture (removed CLI spawn, Unix socket only)
- **v3.0.0**: Complete rewrite to expose CLI commands as tools
- **v2.0.0**: Bridge-based architecture (deprecated)
- **v1.0.0**: Initial release

## Support

For issues or questions:
- Check the main bot documentation in `../CLAUDE.md`
- Review CLI help: `./bin/spacetraders --help`
- Ensure the daemon is running: `./bin/spacetraders-daemon`
- Check daemon health: `./bin/spacetraders health`
