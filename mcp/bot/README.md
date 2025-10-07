# SpaceTraders Bot MCP Server (TypeScript)

Node.js/TypeScript MCP server that exposes the SpaceTraders bot workflows. It
invokes the existing Python automation stack via `spacetraders_bot.py` and
`bot/mcp_bridge.py`, so make sure the Python dependencies from `bot/requirements.txt`
are installed.

## Installation

```bash
npm install
npm run build
```

## Running

```bash
npm start
```

### Claude Desktop configuration

```json
{
  "mcpServers": {
    "spacetraders-bot": {
      "command": "node",
      "args": ["/path/to/spacetradersV2/mcp/bot/build/index.js"],
      "env": {
        "SPACETRADERS_TOKEN": "your_token_here"
      }
    }
  }
}
```

The server launches the Python bot as needed, so keep your virtual environment
(or system Python) available and ensure any custom executable path is exported
via `MCP_PYTHON_BIN` if `/usr/bin/python3` is not correct on your machine.
