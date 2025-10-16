# MCP Tool Enhancement: Add contract_count Parameter

## Issue
The `bot_fulfill_contract` MCP tool doesn't expose the `--contract-count` parameter that exists in the CLI.

## Current Workaround
Launch CLI directly with nohup:
```bash
nohup python3 -m spacetraders_bot.cli contract --player-id 6 --ship STARHOPPER-1 --contract-count 50 > logs/contract-batch-50.log 2>&1 & echo $!
```

## Required Changes

### 1. Update botToolDefinitions.ts (line ~479)
Add `contract_count` parameter to `bot_fulfill_contract` schema:

```typescript
"contract_count": {
  "type": "integer",
  "description": "Optional: Number of contracts to negotiate and fulfill in sequence (default: 1). For batch operations, omit contract_id and specify count."
}
```

Update `required` array to make `contract_id` optional when using batch mode:
```typescript
"required": ["player_id", "ship"]
```

### 2. Update index.ts (line ~712)
Add contract_count support in bot_fulfill_contract handler:

```typescript
case "bot_fulfill_contract": {
  this.ensureArgs(name, args, ["player_id", "ship"]);

  // For batch mode, use contract command directly without daemon wrapper
  if (args.contract_count && Number(args.contract_count) > 1) {
    command.push(
      "contract",
      "--player-id",
      String(args.player_id),
      "--ship",
      String(args.ship),
      "--contract-count",
      String(args.contract_count)
    );
  } else {
    // Single contract with daemon wrapper
    const daemonId = `contract-${String(args.ship)}-${Date.now()}`;
    command.push(
      "daemon",
      "start",
      "--player-id",
      String(args.player_id),
      "--daemon-id",
      daemonId,
      "contract",
      "--ship",
      String(args.ship)
    );

    if (args.contract_id) {
      command.push("--contract-id", String(args.contract_id));
    }
  }

  if (args.buy_from) {
    command.push("--buy-from", String(args.buy_from));
  }
  break;
}
```

## Benefits
- Enables 50-contract batch operations directly from MCP/Claude
- No need for manual CLI workarounds
- Consistent with other daemon-based operations
- Supports both single and batch contract modes

## Priority
Medium - workaround exists but proper MCP integration would improve UX
