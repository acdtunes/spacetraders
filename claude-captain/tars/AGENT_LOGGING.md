# Subagent Filesystem Logging

## Overview

All subagent invocations are now automatically logged to the filesystem, organized by sessionId.

## Log Location

```
/claude-captain/tars/agent-logs/<sessionId>/
```

Each subagent invocation creates a separate log file:
```
agent-logs/
└── <sessionId>/
    ├── contract-coordinator_1699300000000.log
    ├── scout-coordinator_1699300010000.log
    ├── fleet-manager_1699300020000.log
    └── ...
```

## Log Format

Each log file contains:

```
================================================================================
SUBAGENT INVOCATION
================================================================================
Agent: <agent-name>
Session: <session-id>
Timestamp: <ISO timestamp>

INPUT PROMPT:
--------------------------------------------------------------------------------
<full prompt sent to subagent>

WAITING FOR RESULT...
================================================================================

RESULT RECEIVED:
--------------------------------------------------------------------------------
Timestamp: <ISO timestamp>
Duration: <duration in seconds>

OUTPUT:
--------------------------------------------------------------------------------
<full output from subagent>

================================================================================
END OF INVOCATION
================================================================================
```

## How It Works

1. **Automatic Detection**: The `AgentLogger` class monitors the SDK message stream for Task tool calls
2. **Session Organization**: Logs are organized in directories by sessionId
3. **Complete Logging**: Both input prompts and output results are logged
4. **Timing Information**: Duration of each subagent call is tracked

## Implementation Files

- `tars/src/agentLogger.ts` - Core logging implementation
- `tars/src/ui/TarsApp.tsx:143-144` - Integration point in message stream

## Logged Subagents

The following subagents are automatically logged:
- `contract-coordinator` - Contract fulfillment operations
- `scout-coordinator` - Market intelligence operations
- `fleet-manager` - Fleet optimization and analysis
- `bug-reporter` - Error documentation
- `feature-proposer` - Strategic improvements
- `procurement-coordinator` - Ship purchases
- `captain-logger` - Mission narrative logging

## Usage

No configuration needed - logging happens automatically when TARS Captain runs. Logs are created in real-time as subagents are invoked.

To view logs for a specific session:
```bash
ls -la agent-logs/<sessionId>/
cat agent-logs/<sessionId>/contract-coordinator_*.log
```

## Notes

- Logs persist across sessions
- Each invocation gets its own timestamped file
- Log directory is created automatically if it doesn't exist
- Logs include the full prompt and response, making debugging easier
