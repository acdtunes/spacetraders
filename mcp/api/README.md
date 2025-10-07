# SpaceTraders API MCP Server

A Node.js/TypeScript Model Context Protocol (MCP) server for the
[SpaceTraders API](https://spacetraders.io).

## Features

This MCP server exposes the canonical SpaceTraders REST API operations:

- **Agent Management**: Register agents, inspect account information.
- **Navigation & Systems**: Explore systems, waypoints, markets, and shipyards.
- **Contracts & Fleet**: Review contracts, accept them, and control ships
  (navigate, dock, orbit, refuel, scan, trade).

## Installation

```bash
npm install
npm run build
```

## Configuration

Set your SpaceTraders API token as an environment variable:

```bash
export SPACETRADERS_TOKEN="your_token_here"
```

You can obtain a token by registering a new agent using the `register_agent` tool.

## Usage

### Running the server

```bash
npm start
```

### Using with Claude Desktop

Add to your Claude Desktop configuration file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spacetraders": {
      "command": "node",
      "args": ["/path/to/spacetradersV2/mcp/api/build/index.js"],
      "env": {
        "SPACETRADERS_TOKEN": "your_token_here"
      }
    }
  }
}
```

> Need bot automation instead of the raw API? Use the companion server in
> `mcp/bot/`, which shells out to the Python automation stack.

## Available Tools

### Agent & Registration
- `register_agent` - Register a new agent
- `get_agent` - Get your agent details

### Systems & Waypoints
- `list_systems` - List all systems
- `get_system` - Get system details
- `list_waypoints` - List waypoints in a system
- `get_waypoint` - Get waypoint details
- `get_market` - Get market data
- `get_shipyard` - Get shipyard data

### Factions
- `list_factions` - List all factions
- `get_faction` - Get faction details

### Contracts
- `list_contracts` - List your contracts
- `get_contract` - Get contract details
- `accept_contract` - Accept a contract

### Fleet Operations
- `list_ships` - List your ships
- `get_ship` - Get ship details
- `navigate_ship` - Navigate to a waypoint
- `dock_ship` - Dock at current location
- `orbit_ship` - Enter orbit
- `refuel_ship` - Refuel ship
- `extract_resources` - Extract resources
- `sell_cargo` - Sell cargo
- `purchase_cargo` - Purchase cargo
- `scan_systems` - Scan nearby systems
- `scan_waypoints` - Scan nearby waypoints
- `scan_ships` - Scan nearby ships

## Development

```bash
npm run dev  # Watch mode
npm run build  # Production build
```

## License

MIT
