# Subagent Logging - Quick Reference Guide

## Overview

This project has two distinct logging systems:

1. **Container Logging** - For background daemon processes
2. **Subagent Logging** - For Claude Agent SDK subagents (NOT YET IMPLEMENTED)

---

## 1. Current Logging System (Container Logs)

### What Gets Logged
- Background daemon/container operations
- Each container logs via `self.log(message, level)` in base_container.py

### Where It's Stored
- **Database:** `var/spacetraders.db` → `container_logs` table
- **Files:** Python logging to stderr/stdout

### How to Query
```python
# Get logs for a specific container
logs = database.get_container_logs(
    container_id="scout-1",
    player_id=123,
    limit=100,
    level="ERROR",  # Optional filter
    since="2025-11-06T10:00:00"  # Optional time filter
)
```

### Database Schema
```sql
container_logs(
    log_id INTEGER PRIMARY KEY,
    container_id TEXT,
    player_id INTEGER,
    timestamp TIMESTAMP,
    level TEXT,          -- INFO, WARNING, ERROR, DEBUG
    message TEXT
)
```

---

## 2. Planned Subagent Logging System

### What Will Be Logged
- Agent decision points
- Tool execution results
- Errors and warnings
- Performance metrics
- Execution flow

### Where It Will Be Stored
1. **Primary:** New `subagent_logs` table in SQLite
2. **Secondary:** Markdown files in `mission-logs/subagent-logs/`

### Key Identifiers
- **session_id** - From Agent SDK (session context)
- **agent_name** - e.g., 'contract-coordinator', 'scout-coordinator'
- **player_id** - Optional, if agent is player-bound

---

## 3. Implementation Checklist

### Phase 1: Database Schema
- [ ] Add `subagent_logs` table to database.py
- [ ] Add migration for existing databases
- [ ] Create indexes for session, agent, timestamp, level

### Phase 2: Database Methods (database.py)
- [ ] `log_subagent(session_id, agent_name, message, level, category, metadata)`
- [ ] `get_subagent_logs(session_id, agent_name, limit, level, since)`
- [ ] `clear_subagent_logs(session_id)` - Optional cleanup

### Phase 3: MCP Tool
- [ ] Add `subagent_log` case in index.ts buildCliArgs()
- [ ] Add tool definition to botToolDefinitions.ts
- [ ] Create CLI handler in bot's CLI main.py

### Phase 4: Agent Configuration
- [ ] Add `mcp__spacetraders-bot__subagent_log` tool to all agents in agentConfig.ts
- [ ] Update agent definitions with logging guidelines

### Phase 5: File-based Logs
- [ ] Create `mission-logs/subagent-logs/` directory
- [ ] Agents write narrative logs here using Write SDK tool
- [ ] Format: `{YYYY-MM-DD}_{HHmm}_{agent-name}_{session-id}.md`

### Phase 6: Session Tracking Enhancement
- [ ] Extend ConversationMemory.ts with subagent session tracking
- [ ] Add metadata about which subagents were invoked per session

---

## 4. File Locations Reference

### Python Backend
```
bot/
├── src/
│   ├── adapters/secondary/persistence/
│   │   └── database.py               # MODIFY: Add subagent_logs table & methods
│   └── adapters/primary/cli/
│       └── main.py                   # ADD: subagent log command handler
└── mcp/src/
    ├── index.ts                      # MODIFY: Add subagent_log tool case
    └── botToolDefinitions.ts         # MODIFY: Add tool definition
```

### TypeScript/Agent SDK
```
claude-captain/
├── tars/src/
│   ├── agentConfig.ts                # MODIFY: Add tool to all agents
│   └── conversationMemory.ts         # MODIFY: Track subagent sessions
└── mission-logs/
    └── subagent-logs/                # CREATE: New directory for logs
```

---

## 5. Data Model

### Table Schema (SQL)
```sql
CREATE TABLE subagent_logs (
    log_id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    player_id INTEGER,
    timestamp TIMESTAMP NOT NULL,
    level TEXT NOT NULL DEFAULT 'INFO',
    category TEXT NOT NULL,
    message TEXT NOT NULL,
    metadata TEXT,  -- JSON string
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
);

-- Indexes for common queries
CREATE INDEX idx_subagent_logs_session ON subagent_logs(session_id);
CREATE INDEX idx_subagent_logs_agent ON subagent_logs(agent_name);
CREATE INDEX idx_subagent_logs_timestamp ON subagent_logs(timestamp DESC);
CREATE INDEX idx_subagent_logs_level ON subagent_logs(level);
```

### Python Method Signature
```python
def log_subagent(
    self,
    session_id: str,
    agent_name: str,
    player_id: Optional[int],
    message: str,
    level: str = "INFO",
    category: str = "general",
    metadata: Optional[Dict[str, Any]] = None
) -> int:
    """
    Log message from subagent to database.
    
    Args:
        session_id: Agent SDK session ID
        agent_name: Name of subagent (e.g., 'contract-coordinator')
        player_id: Player ID (optional)
        message: Log message
        level: Log level (INFO, WARNING, ERROR, DEBUG)
        category: Log category (execution, decision, error, metric, etc.)
        metadata: Additional structured data as dict
        
    Returns:
        log_id of inserted entry
    """
```

### MCP Tool Definition
```typescript
{
  name: "subagent_log",
  description: "Log entry from a subagent during execution",
  inputSchema: {
    type: "object",
    properties: {
      session_id: {
        type: "string",
        description: "Agent SDK session ID"
      },
      agent_name: {
        type: "string",
        description: "Name of subagent"
      },
      message: {
        type: "string",
        description: "Log message"
      },
      level: {
        type: "string",
        enum: ["INFO", "WARNING", "ERROR", "DEBUG"],
        description: "Log level"
      },
      category: {
        type: "string",
        description: "Log category (execution, decision, error, metric)"
      },
      player_id: {
        type: "number",
        description: "Player ID (optional)"
      },
      metadata: {
        type: "string",
        description: "JSON string with additional data (optional)"
      }
    },
    required: ["session_id", "agent_name", "message"]
  }
}
```

---

## 6. Log Categories (Recommended)

- **execution** - Tool/command execution logs
- **decision** - Strategic decisions made
- **error** - Errors and failures
- **metric** - Performance metrics, profitability, timing
- **planning** - Plan formulation and updates
- **navigation** - Movement between states
- **analysis** - Data analysis results
- **general** - Default/other categories

---

## 7. Metadata Examples

```json
// Execution metrics
{
  "tool_name": "contract_batch_workflow",
  "duration_ms": 5432,
  "success": true,
  "contracts_completed": 3,
  "total_profit": 14500
}

// Decision metadata
{
  "decision_type": "ship_purchase",
  "rationale": "daily_profit > 20000",
  "impact_estimate": "+45% daily revenue",
  "risk_level": "medium"
}

// Error metadata
{
  "error_type": "InsufficientFunds",
  "operation": "purchase_ship",
  "required": 45000,
  "available": 32000,
  "retry_possible": true
}
```

---

## 8. Existing Logging Reference

### Container/Daemon Logging (Already Implemented)
```python
# In a container class
self.log("Starting mining operation at X1-JV40-AB12", level="INFO")
self.log("Unexpected API response", level="WARNING")
self.log("Failed to extract resources", level="ERROR")
```

### File-based Logging (Already in Use)
- `mission-logs/2025-11-06_*.md` - Narrative mission logs
- `reports/bugs/` - Bug reports

### Session Tracking (Already Implemented)
```typescript
// In ConversationMemory
const memory = new ConversationMemory();
memory.setSessionId(sessionId);
memory.getSessionId();  // Returns session_id
```

---

## 9. Integration Points

### Agent SDK to Backend
1. Agent calls `mcp__spacetraders-bot__subagent_log` tool
2. MCP server routes to Python CLI
3. Python CLI inserts into database
4. Returns confirmation (log_id)

### File-based Logs
1. Agents use Write SDK tool directly
2. File saved to `mission-logs/subagent-logs/`
3. Can be read back with Read SDK tool

### Session Context
1. Agent SDK provides session_id automatically
2. Agent always includes session_id in log calls
3. ConversationMemory tracks subagent executions
4. Can reconstruct agent workflow from logs + session data

---

## 10. Query Examples (After Implementation)

### Get all logs for a session
```python
logs = database.get_subagent_logs(
    session_id="session-abc123",
    limit=1000
)
```

### Get logs for a specific agent in a session
```python
logs = database.get_subagent_logs(
    session_id="session-abc123",
    agent_name="contract-coordinator",
    limit=100
)
```

### Filter errors only
```python
logs = database.get_subagent_logs(
    session_id="session-abc123",
    agent_name="scout-coordinator",
    level="ERROR"
)
```

---

## 11. Status

| Component | Status | Notes |
|-----------|--------|-------|
| Container logging | ✅ Done | Fully implemented |
| Session tracking | ✅ Done | Works via Agent SDK |
| File-based logs | ✅ Done | mission-logs/ directory |
| **Subagent DB logging** | ❌ TODO | Needs implementation |
| **Subagent MCP tool** | ❌ TODO | Needs implementation |
| **Agent tool access** | ❌ TODO | Need to add to agentConfig.ts |

---

## 12. Dependencies & Prerequisites

- SQLite3 database (already set up)
- Agent SDK session tracking (already in place)
- MCP server infrastructure (already in place)
- CLI argument parsing (already in place)

**No external dependencies needed** - all infrastructure exists, just needs connection layer.

