# Subagent Logging Architecture - Codebase Exploration Report

## Executive Summary

This document provides a comprehensive analysis of the SpaceTraders Bot / TARS Captain codebase structure, existing logging mechanisms, and recommendations for implementing subagent-specific logging.

---

## 1. Current Directory Structure

### Project Layout
```
spacetraders/
├── bot/                          # Main Python backend
│   ├── src/
│   │   ├── adapters/
│   │   │   ├── primary/
│   │   │   │   ├── cli/          # CLI commands
│   │   │   │   └── daemon/       # Container/daemon system
│   │   │   └── secondary/
│   │   │       └── persistence/  # Database layer
│   │   ├── application/          # Business logic
│   │   ├── configuration/        # Config management
│   │   ├── domain/               # Domain models
│   │   └── ports/                # Interface definitions
│   ├── mcp/
│   │   └── src/
│   │       ├── index.ts          # MCP server
│   │       └── botToolDefinitions.ts
│   ├── tests/
│   │   ├── bdd/                  # Behavior-driven tests
│   │   └── fixtures/
│   └── var/
│       └── spacetraders.db       # SQLite database (WAL mode)
│
├── claude-captain/               # TypeScript Agent SDK frontend
│   ├── .claude/
│   │   ├── agents/               # Subagent prompts
│   │   │   ├── captain-logger.md
│   │   │   ├── contract-coordinator.md
│   │   │   ├── scout-coordinator.md
│   │   │   ├── fleet-manager.md
│   │   │   ├── bug-reporter.md
│   │   │   ├── feature-proposer.md
│   │   │   └── procurement-coordinator.md
│   │   └── output-styles/
│   │       └── tars.md           # TARS system prompt
│   ├── tars/
│   │   ├── src/
│   │   │   ├── agentConfig.ts    # Agent definitions & tools
│   │   │   ├── conversationMemory.ts  # Session management
│   │   │   ├── captain.tsx       # Main entry point
│   │   │   └── ui/               # Ink React UI
│   │   └── .tars_session.json    # Session persistence
│   ├── .mcp.json                 # MCP server config
│   └── mission-logs/             # Log files
│       ├── 2025-11-06_*.md
│       └── ...
│
├── mission-logs/                 # Alternative logs location
└── reports/
    └── bugs/                     # Bug reports directory

---

## 2. Existing Logging Mechanisms

### 2.1 Database Logging (SQLite)

**Location:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/src/adapters/secondary/persistence/database.py`

**Container Logs Table Schema:**
```sql
CREATE TABLE container_logs (
    log_id INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id TEXT NOT NULL,
    player_id INTEGER NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    level TEXT NOT NULL DEFAULT 'INFO',
    message TEXT NOT NULL,
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
)
```

**Key Features:**
- Logs are stored in SQLite database at `var/spacetraders.db`
- Database uses WAL (Write-Ahead Logging) mode for concurrency
- Indexed by `container_id`, `timestamp` for fast queries
- Supports log levels: INFO, WARNING, ERROR, DEBUG
- Can filter logs by level and timestamp range

**API Methods:**
- `log_to_database(container_id, player_id, message, level)` - Insert log entry
- `get_container_logs(container_id, player_id, limit, level, since)` - Query logs

### 2.2 Container/Daemon Logging

**Location:** `/Users/andres.camacho/Development/Personal/spacetraders/bot/src/adapters/primary/daemon/base_container.py`

**Base Container Logging Method:**
```python
def log(self, message: str, level: str = "INFO"):
    """Add log entry to database
    
    Args:
        message: Log message to record
        level: Log level (INFO, WARNING, ERROR, DEBUG)
    """
    try:
        self.database.log_to_database(
            container_id=self.container_id,
            player_id=self.player_id,
            message=message,
            level=level
        )
        logger.info(f"[{self.container_id}] [{level}] {message}")
    except Exception as e:
        # Fallback to logger if database write fails
        logger.error(f"Failed to write log to database: {e}")
        logger.info(f"[{self.container_id}] [{level}] {message}")
```

**Key Features:**
- Dual-logging: Database + Python logging
- Container ID tracking for all logs
- Fallback to standard logging if database fails
- Called via `self.log()` from containers

### 2.3 Session Memory (TypeScript/Agent SDK)

**Location:** `/Users/andres.camacho/Development/Personal/spacetraders/claude-captain/tars/src/conversationMemory.ts`

**Session Persistence:**
```typescript
interface SessionData {
  session_id: string;
  created_at: ISO8601;
  last_active: ISO8601;
  conversation_turns: number;
  messages: SDKMessage[];
}
```

**File Location:** `tars/.tars_session.json`

**Key Features:**
- Persists Agent SDK session_id for session resumption
- Tracks conversation turns
- Stores message history
- JSON-based file storage

### 2.4 Mission Logs (File-based)

**Locations:**
- `mission-logs/2025-11-06_*.md` (markdown files)
- `reports/bugs/` (bug reports)

**Format:** Human-readable markdown with metadata

**Current Files:**
- `2025-11-06_1630_afk-session-blocked.md` (10.6 KB)
- `2025-11-06_afk-session-1_infrastructure-catastrophe.md` (6.3 KB)

### 2.5 Python Standard Logging

**Usage Pattern:**
```python
import logging
logger = logging.getLogger(__name__)

# Throughout bot codebase
logger.debug("Config file not found...")
logger.warning("Invalid JSON in config file...")
logger.error("Error saving config...")
logger.info("Loaded config from...")
```

**Key Points:**
- Decentralized per-module loggers
- No centralized configuration found yet
- Fallback for database logging failures

---

## 3. Session & Agent Infrastructure

### 3.1 Agent Configuration (TypeScript)

**Location:** `claude-captain/tars/src/agentConfig.ts`

**Defined Subagents:**
1. **contract-coordinator** - Contract fulfillment operations
2. **scout-coordinator** - Market intelligence via probe ships
3. **fleet-manager** - Ship assignments and fleet optimization
4. **bug-reporter** - Error documentation after retries
5. **feature-proposer** - Strategic analysis and improvement proposals
6. **procurement-coordinator** - Ship purchases
7. **captain-logger** - Narrative mission logging

**Agent Definition Example:**
```typescript
agents: {
  'contract-coordinator': {
    description: 'Use when you need to run contract fulfillment operations',
    prompt: loadPrompt(join(tarsRoot, '.claude/agents/contract-coordinator.md')),
    model: 'sonnet',
    tools: [
      'Read', 'Write', 'TodoWrite',
      'mcp__spacetraders-bot__contract_batch_workflow',
      // ... other MCP tools
    ]
  },
  // ... more agents
}
```

### 3.2 Session Tracking

**Main Session File:** `tars/.tars_session.json`

**Tracked Data:**
- `session_id` - Agent SDK session identifier
- `created_at` - Session creation timestamp
- `last_active` - Last activity timestamp
- `conversation_turns` - Number of turns in session
- `messages` - SDK message history

**Session Management Methods:**
```typescript
setSessionId(sessionId: string): void
getSessionId(): string | null
incrementTurns(): void
addMessage(message: SDKMessage): void
getMessages(): SDKMessage[]
clear(): void
hasPreviousSession(): boolean
get turnCount(): number
```

### 3.3 MCP Tool Infrastructure

**Location:** `bot/mcp/src/index.ts`

**Key Features:**
- Model Context Protocol server using stdio transport
- Spawns Python CLI processes for each tool call
- Tool timeout: 5 minutes (300s)
- Dynamic CLI argument building

**MCP Tools Available:**
- Player management (register, list, info)
- Ship management (list, info)
- Navigation (navigate, dock, orbit, refuel)
- Contracts (batch workflow)
- Scouting (market surveys)
- Daemon/Container operations (list, inspect, logs, stop, remove)
- Configuration (show, set-player, clear-player)
- Waypoint queries (list)

---

## 4. Configuration Management

### 4.1 User Configuration

**Location:** `bot/src/configuration/config.py`

**Storage:** `~/.spacetraders/config.json`

**Schema:**
```json
{
  "default_player_id": 123,
  "default_agent": "AGENT-SYMBOL"
}
```

**Key Features:**
- Persistent user preferences
- Default player/agent tracking
- Extensible key-value storage

### 4.2 Database Path Resolution

**Priority Order:**
1. Environment variable: `SPACETRADERS_DB_PATH`
2. Default: `var/spacetraders.db`

**Explicitly Set In:** `bot/mcp/src/index.ts` (line 354)

---

## 5. Agent/Subagent Identification

### 5.1 Current Identification Methods

**TypeScript/SDK Level:**
- **Agent Symbol:** `AGENT-SYMBOL` (stored in config)
- **Subagent Name:** From `agents` config object (e.g., `'contract-coordinator'`)
- **Session ID:** Agent SDK provides `session_id`

**Python/Backend Level:**
- **Player ID:** Integer primary key
- **Container ID:** String identifier (e.g., `'scout-container-1'`)
- **Agent Symbol:** For identifying which agent registered the player

### 5.2 Tool Invocation Context

**MCP Calls Include:**
```typescript
// Common parameters
--player-id (optional)
--agent (optional) // agent symbol

// Example from scout_markets:
["scout", "markets", 
 "--ships", "SCOUT-1,SCOUT-2",
 "--system", "X1-GZ7", 
 "--markets", "X1-GZ7-A1,X1-GZ7-B2",
 "--agent", "scout-coordinator"]
```

---

## 6. Recommended Locations for Subagent Logging

### 6.1 Primary Recommendation: Database Table

**Create new table: `subagent_logs`**

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
    metadata TEXT,  -- JSON for structured data
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
)

CREATE INDEX idx_subagent_logs_session ON subagent_logs(session_id)
CREATE INDEX idx_subagent_logs_agent ON subagent_logs(agent_name)
CREATE INDEX idx_subagent_logs_timestamp ON subagent_logs(timestamp DESC)
CREATE INDEX idx_subagent_logs_level ON subagent_logs(level)
```

**Rationale:**
- Separate from container logs for clarity
- Session-based organization
- Agent-based filtering capability
- Structured metadata support (JSON)
- Consistent with existing database patterns
- WAL mode ensures concurrency

### 6.2 Secondary: File-based Logs

**Location:** `mission-logs/subagent-logs/`

**Pattern:** `{YYYY-MM-DD}_{HHmm}_{agent-name}_{session-id}.md`

**Example:** `2025-11-06_1430_contract-coordinator_s7f8d9a2.md`

**Rationale:**
- Human-readable for post-session analysis
- Complements database logs
- Easy to navigate by date and agent
- Supports narrative logs (like captain-logger entries)

### 6.3 Tertiary: Agent-specific Logs Directory

**Location:** `mission-logs/agents/`

**Structure:**
```
mission-logs/agents/
├── contract-coordinator/
│   ├── 2025-11-06_session-s7f8d9a2.md
│   └── 2025-11-06_session-s8g9e0b3.md
├── scout-coordinator/
│   └── 2025-11-06_session-t1h2i3c4.md
└── ...
```

**Rationale:**
- Organize by agent responsibility
- Easy to review specific agent performance
- Facilitates agent-specific analysis

---

## 7. Existing Agent/Logging Integration Points

### 7.1 Captain-Logger Agent

**Purpose:** Narrative mission logging for TARS

**Input Context:**
- Event type (operation_started, operation_completed, critical_error, etc.)
- Fleet snapshot (active miners, credits, etc.)
- Recent history of events
- Narrative context

**Output:** Human-readable markdown entries

**Location of Prompt:** `tars/.claude/agents/captain-logger.md`

**Tool (Referenced but needs implementation):**
- `mcp__spacetraders-bot__captain_log_create`
- Currently listed in agentConfig.ts but not implemented in bot

### 7.2 Agent Tool Access

**Tools Available to ALL Subagents:**
- Read, Write, TodoWrite (SDK tools)
- MCP tools (read-only or execution-based)

**Daemon/Container Inspection Tools:**
- `mcp__spacetraders-bot__daemon_inspect`
- `mcp__spacetraders-bot__daemon_logs`

---

## 8. Key Observations & Constraints

### 8.1 Current State

1. **Container logging works** - Daemon containers log to database
2. **Session tracking exists** - Agent SDK session IDs are persisted
3. **File-based logs exist** - Mission logs already in use
4. **No subagent-specific logging** - Agents lack their own logging infrastructure
5. **Agent tools incomplete** - `captain_log_create` referenced but not implemented

### 8.2 Integration Points

- **Database:** Ready for additional tables (WAL mode, concurrent access)
- **File system:** Existing log directories can be extended
- **MCP server:** Can add new tools for agent logging
- **Agent SDK:** Agents can use Write tool to create logs

### 8.3 Practical Constraints

- Agents run via Agent SDK (model context protocol)
- Agents can read from database via MCP tools
- Agents can write files directly using Write SDK tool
- Agents have limited tool access (by design)
- Session context available through Agent SDK

---

## 9. Data Model for Subagent Logging

### 9.1 Session Tracking

**Agent Session Identifier:**
```typescript
interface AgentSession {
  session_id: string;              // From Agent SDK
  agent_name: string;              // e.g., 'contract-coordinator'
  player_id: number;               // Optional, if agent is player-bound
  created_at: ISO8601;
  last_active: ISO8601;
  status: 'active' | 'completed' | 'failed';
  duration_seconds: number;
  turn_count: number;
}
```

### 9.2 Log Entry Structure

**Database Model:**
```python
class SubagentLogEntry:
    log_id: int
    session_id: str              # Links to agent session
    agent_name: str              # 'contract-coordinator', etc.
    player_id: Optional[int]
    timestamp: datetime
    level: str                   # INFO, WARNING, ERROR, DEBUG
    category: str                # 'execution', 'decision', 'error', 'metric'
    message: str
    metadata: Optional[dict]     # Structured data (JSON)
```

### 9.3 Metadata Examples

```json
{
  "contract_id": "CONTRACT-123",
  "ship_symbol": "SCOUT-1",
  "operation": "market_survey",
  "duration_ms": 4532,
  "success": true,
  "data_points": 47,
  "next_action": "analyze_results"
}
```

---

## 10. Implementation Recommendations

### 10.1 For Python Backend (Bot)

**File:** `bot/src/adapters/secondary/persistence/database.py`

Add methods:
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
    """Log entry from subagent"""
    
def get_subagent_logs(
    self,
    session_id: str,
    agent_name: Optional[str] = None,
    limit: int = 100
) -> List[Dict[str, Any]]:
    """Query subagent logs by session"""
```

### 10.2 For MCP Server (TypeScript)

**File:** `bot/mcp/src/index.ts`

Add tool handler for:
```typescript
case "subagent_log":
  const cmd = [
    "subagent", "log",
    "--session-id", String(args.session_id),
    "--agent-name", String(args.agent_name),
    "--message", String(args.message),
    "--level", String(args.level || "INFO"),
    "--category", String(args.category || "general"),
    // optional metadata
  ];
  return cmd;
```

### 10.3 For Agent SDK

**File:** `claude-captain/tars/src/agentConfig.ts`

Add to ALL agents:
```typescript
agents: {
  'contract-coordinator': {
    // ... existing config
    tools: [
      // ... existing tools
      'mcp__spacetraders-bot__subagent_log',  // NEW
    ]
  }
}
```

### 10.4 For Session Tracking

**Extend:** `claude-captain/tars/src/conversationMemory.ts`

Add methods:
```typescript
getSubagentSessions(): Map<string, SubagentSessionData>
recordSubagentExecution(agentName: string, details: SubagentExecution)
```

---

## 11. File Structure Summary

### Key Files for Logging Implementation

| File | Purpose | Modification |
|------|---------|-------------|
| `bot/src/adapters/secondary/persistence/database.py` | Database schema & queries | Add subagent_logs table + methods |
| `bot/mcp/src/index.ts` | MCP tool definitions | Add subagent_log case |
| `bot/mcp/src/botToolDefinitions.ts` | Tool descriptions | Add subagent_log definition |
| `claude-captain/tars/src/agentConfig.ts` | Agent configuration | Add subagent_log tool to all agents |
| `claude-captain/tars/src/conversationMemory.ts` | Session persistence | Extend for subagent sessions |
| `mission-logs/` | Log directory | Create `subagent-logs/` subdirectory |
| `bot/src/configuration/config.py` | Config management | Optional: subagent-specific settings |

---

## 12. Summary

**Current State:**
- Container logging: ✅ Fully implemented
- Session tracking: ✅ Working (TARS sessions)
- File-based logs: ✅ Existing (mission-logs)
- Subagent logging: ❌ Not implemented
- Captain-logger tool: ⚠️ Referenced but not implemented

**Recommended Implementation:**
1. **Primary:** Database table for structured logging
2. **Secondary:** File-based logs for human review
3. **Integration:** MCP tools for agent logging
4. **Organization:** Session-based and agent-based indexing

**Priority Locations:**
1. Database: `bot/src/adapters/secondary/persistence/database.py`
2. MCP: `bot/mcp/src/index.ts` + `botToolDefinitions.ts`
3. Agent Config: `claude-captain/tars/src/agentConfig.ts`
4. Files: `mission-logs/subagent-logs/` directory

