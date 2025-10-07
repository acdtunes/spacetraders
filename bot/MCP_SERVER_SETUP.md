# MCP Server Quick Setup Guide

This guide will get you up and running with the SpaceTraders MCP server in Claude Desktop.

> **Heads up:** The active MCP server is now implemented in Node.js/TypeScript
> under `mcp/api/`. Install with `npm install`, build with `npm run build`, and
> point Claude Desktop at `node /path/to/spacetradersV2/mcp/api/build/index.js`.
> The Python-oriented instructions below remain for legacy reference only.

## Prerequisites

- Python 3.10 or higher
- Claude Desktop installed
- SpaceTraders agent token

## Step 1: Install Dependencies

```bash
cd /Users/andres.camacho/Development/Personal/spacetradersV2/bot

# Install required packages
pip install -r requirements.txt
```

This installs:
- `requests` - HTTP client for SpaceTraders API
- `python-dateutil` - Date/time utilities
- `mcp` - Model Context Protocol SDK

## Step 2: Test the Bot

Verify the bot works before setting up MCP:

```bash
# Get your agent token from SpaceTraders
export SPACETRADERS_TOKEN="your_token_here"

# Test a simple command
python3 spacetraders_bot.py status --token $SPACETRADERS_TOKEN
```

You should see your agent and ship status. If this works, continue.

## Step 3: Configure Claude Desktop

### Find Your Config File

**macOS**:
```bash
open ~/Library/Application\ Support/Claude/
# Edit: claude_desktop_config.json
```

**Windows**:
```
%APPDATA%\Claude\claude_desktop_config.json
```

### Add MCP Server Configuration

Edit `claude_desktop_config.json` and add:

```json
{
  "mcpServers": {
    "spacetraders": {
      "command": "node",
      "args": [
        "/Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js"
      ]
    }
  }
}
```

**Important**: Replace the path with your actual absolute path to `build/index.js`.

### With Environment Variable (Optional)

You can also set your token as an environment variable:

```json
{
  "mcpServers": {
    "spacetraders": {
      "command": "node",
      "args": [
        "/Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js"
      ],
      "env": {
        "SPACETRADERS_TOKEN": "your_token_here"
      }
    }
  }
}
```

**Warning**: Don't commit this file with your token to git!

## Step 4: Restart Claude Desktop

1. Quit Claude Desktop completely
2. Relaunch Claude Desktop
3. Check for MCP server in the bottom-right corner (🔌 icon)

## Step 5: Test in Claude

Try these commands in Claude Desktop:

### Test 1: Check Fleet Status

```
Check my SpaceTraders fleet status using token: YOUR_TOKEN
```

Claude should use `spacetraders_status` tool and show your fleet.

### Test 2: Calculate Distance

```
Calculate distance between X1-HU87-A1 and X1-HU87-B9 using token: YOUR_TOKEN
```

### Test 3: Check Daemon Status

```
Show me all running SpaceTraders daemons
```

## Troubleshooting

### Server Not Showing Up

1. **Check JSON syntax**:
   ```bash
   # Validate JSON
   node -m json.tool ~/Library/Application\ Support/Claude/claude_desktop_config.json
   ```

2. **Check path**:
   ```bash
   # Verify file exists
   ls -l /Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js
   ```

3. **Check Python version**:
   ```bash
   node --version
   # Should be 3.10 or higher
   ```

4. **Check Claude logs** (macOS):
   ```bash
   tail -f ~/Library/Logs/Claude/mcp*.log
   ```

### MCP Package Not Found

```bash
pip install --upgrade mcp
```

### Permission Denied

```bash
chmod +x /Users/andres.camacho/Development/Personal/spacetradersV2/mcp/bot/build/index.js
```

## What's Available?

Once configured, Claude can:

✅ **Fleet Management**
- Check agent and ship status
- Monitor fleet continuously

✅ **Mining Operations**
- Start/stop mining operations
- Monitor mining progress

✅ **Trading Operations**
- Execute trading routes
- Scout markets for best prices

✅ **Contract Operations**
- Negotiate new contracts
- Fulfill contract requirements

✅ **Daemon Management**
- Start operations in background
- Monitor daemon status and logs
- Stop running operations

✅ **Ship Assignments**
- Check ship availability
- Assign/release ships
- Prevent double-booking

✅ **Navigation**
- Build system graphs
- Plan optimal routes
- Calculate distances

## Example Workflows

### Autonomous Fleet Manager

```
Claude, I want to maximize profits for 4 hours:
1. Check my fleet status
2. Scout markets in X1-HU87
3. Find the best trade routes
4. Start the most profitable operations as daemons
5. Monitor progress every 30 minutes
```

### Contract Automation

```
Claude, negotiate a new contract using SHIP-1.
If the ROI is >10% and profit >10,000 credits, fulfill it.
Report back when complete.
```

### Mining Fleet Coordination

```
Claude, I have 3 mining ships (SHIP-3, SHIP-4, SHIP-5).
Start continuous mining at the best asteroid near X1-HU87-B7.
Run for 100 cycles each.
```

## Security Notes

🔒 **Token Security**
- Never commit `claude_desktop_config.json` with tokens
- Use environment variables or pass tokens via tool arguments
- Keep your token private

🔒 **Local Execution Only**
- MCP server runs locally on your machine
- No network exposure by default
- Anyone with access to your Claude Desktop can execute operations

## Next Steps

1. Read `MCP_SERVER_README.md` for complete tool documentation
2. Review `GAME_GUIDE.md` for operational strategies
3. Check `AGENT_ARCHITECTURE.md` for multi-agent system design

## Support

If you encounter issues:

1. Check Claude Desktop logs: `~/Library/Logs/Claude/`
2. Test `spacetraders_bot.py` directly from command line
3. Verify MCP package installed: `pip list | grep mcp`
4. Check Python version: `node --version`

---

**Ready to automate your SpaceTraders empire! o7** 🚀
