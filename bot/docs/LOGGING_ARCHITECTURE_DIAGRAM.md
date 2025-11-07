# Logging Architecture Diagram

## Current Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                     SPACETRADERS BOT ARCHITECTURE                   │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────┐
│  TARS Captain (TypeScript)  │
│                             │
│  ┌───────────────────────┐  │
│  │  Agent SDK            │  │
│  │ ┌──────────────────┐  │  │
│  │ │ Subagents:       │  │  │
│  │ │ • contract-coor  │  │  │
│  │ │ • scout-coor     │  │  │
│  │ │ • fleet-mgr      │  │  │
│  │ │ • bug-reporter   │  │  │
│  │ │ • procurement    │  │  │
│  │ │ • captain-logger │  │  │
│  │ └──────────────────┘  │  │
│  └───────────────────────┘  │
│           │                 │
│  ┌────────▼──────────────┐  │
│  │ Session Memory        │  │
│  │ (.tars_session.json)  │  │
│  └───────────────────────┘  │
└──────────────┬──────────────┘
               │ (MCP Protocol)
               │
        ┌──────▼──────────────────────┐
        │  MCP Server (TypeScript)    │
        │  bot/mcp/src/index.ts       │
        │                             │
        │  Tool Mapping:              │
        │  • player_*                 │
        │  • ship_*                   │
        │  • navigate, dock, orbit    │
        │  • contract_*               │
        │  • scout_*                  │
        │  • daemon_*                 │
        │  • config_*                 │
        │  • waypoint_*               │
        │                             │
        │  [MISSING: subagent_log]    │
        └──────┬──────────────────────┘
               │ (spawns Python process)
               │
        ┌──────▼──────────────────────┐
        │  Python CLI (bot/src/)      │
        │  adapters/primary/cli/main  │
        │                             │
        │  Commands:                  │
        │  • player register/list     │
        │  • ship list/info           │
        │  • navigate/dock/orbit      │
        │  • contract batch           │
        │  • scout markets            │
        │  • daemon list/inspect      │
        │  • config show/set          │
        │  • waypoint list            │
        │                             │
        │  [TODO: subagent log]       │
        └──────┬──────────────────────┘
               │
               │
        ┌──────▼──────────────────────────────────────────┐
        │  SQLite Database (var/spacetraders.db)          │
        │  WAL Mode - Concurrent Read/Write              │
        │                                                │
        │  Tables:                                       │
        │  • players                                     │
        │  • ships                                       │
        │  • container_logs       ✅ Fully implemented  │
        │  • containers                                  │
        │  • market_data                                 │
        │  • contracts                                   │
        │  • routes                                      │
        │  • waypoints                                   │
        │  • system_graphs                               │
        │                                                │
        │  [TODO: subagent_logs]                         │
        └────────────────────────────────────────────────┘
```

---

## Container Logging (Currently Working)

```
┌─────────────────────────────────┐
│   CommandContainer (Python)     │
│                                 │
│  async run():                   │
│    while not cancelled:         │
│      ├─ self.log(msg, "INFO")   │◄─────────┐
│      ├─ [execute operation]     │          │
│      ├─ self.log(msg, "ERROR")  │◄─────────┤
│      └─ [update state]          │          │
└─────────────────────────────────┘          │
         │                                   │
         │                                   │
    ┌────▼────────────────────┐             │
    │  BaseContainer.log()    │─────────────┤
    │                         │             │
    │  • DB write (primary)   │             │
    │  • Logger fallback      │             │
    └────┬────────────────────┘             │
         │                                   │
         │                                   │
    ┌────▼────────────────────────┐         │
    │  Database.log_to_database() │◄────────┤
    │                             │         │
    │  INSERT into               │         │
    │  container_logs (          │         │
    │    container_id,           │         │
    │    player_id,              │         │
    │    timestamp,              │         │
    │    level,                  │         │
    │    message                 │         │
    │  )                         │         │
    └────┬────────────────────────┘         │
         │                                   │
         └──────────────────────────────────┘

Database Query Example:
  SELECT * FROM container_logs
  WHERE container_id = 'scout-1'
    AND player_id = 123
    AND timestamp >= '2025-11-06T10:00:00'
  ORDER BY timestamp DESC
  LIMIT 100;
```

---

## Subagent Logging (Planned Implementation)

```
┌────────────────────────────────────────────┐
│  Subagent (contract-coordinator, etc.)    │
│  Running via Agent SDK                    │
│                                           │
│  while handling_requests:                 │
│    ├─ Execute tool X                     │
│    │                                     │
│    ├─ Call mcp__...subagent_log(          │
│    │   session_id="abc123",               │
│    │   agent_name="contract-coor",        │
│    │   message="Executed tool X",         │
│    │   level="INFO",                      │
│    │   category="execution",              │
│    │   metadata={...}                     │
│    │ )                                    │
│    │                                     │
│    ├─ Process results                     │
│    │                                     │
│    └─ Make decision                       │
│       └─ Log decision (category=decision) │
└────────────────────────────────────────────┘
         │
         │ (MCP Call)
         │
    ┌────▼─────────────────────┐
    │  MCP Server (index.ts)   │
    │                          │
    │  buildCliArgs():         │
    │    case "subagent_log":  │
    │      args = [            │
    │        "subagent", "log",│
    │        "--session-id",   │
    │        "--agent-name",   │
    │        "--message",      │
    │        "--level",        │
    │        "--category",     │
    │        "--metadata"      │
    │      ]                   │
    │      return args         │
    │                          │
    │  runPythonScript(args)   │
    └────┬────────────────────┘
         │
         │ (spawns Python process)
         │
    ┌────▼────────────────────────────────┐
    │  Python CLI (main.py)               │
    │                                    │
    │  @subagent_log_command             │
    │  def handle_subagent_log():        │
    │    args = parse_args()             │
    │    database = get_database()       │
    │    log_id = database.log_subagent( │
    │      session_id=args.session_id,   │
    │      agent_name=args.agent_name,   │
    │      player_id=args.player_id,     │
    │      message=args.message,         │
    │      level=args.level,             │
    │      category=args.category,       │
    │      metadata=args.metadata        │
    │    )                               │
    │    print(f"log_id={log_id}")       │
    └────┬────────────────────────────────┘
         │
         │
    ┌────▼──────────────────────────┐
    │  Database.log_subagent()      │
    │                               │
    │  INSERT into subagent_logs (  │
    │    session_id,                │
    │    agent_name,                │
    │    player_id,                 │
    │    timestamp,                 │
    │    level,                     │
    │    category,                  │
    │    message,                   │
    │    metadata                   │
    │  ) VALUES (...)               │
    │  RETURNING log_id             │
    └────┬──────────────────────────┘
         │
         │
    ┌────▼──────────────────────────┐
    │  SQLite Database              │
    │                               │
    │  subagent_logs:               │
    │  ┌────────────────────────┐   │
    │  │ log_id    │ 1024       │   │
    │  │ session   │ abc123     │   │
    │  │ agent     │ contract   │   │
    │  │ timestamp │ 2025-11-06 │   │
    │  │ level     │ INFO       │   │
    │  │ category  │ execution  │   │
    │  │ message   │ "Executed" │   │
    │  │ metadata  │ {...}      │   │
    │  └────────────────────────┘   │
    └───────────────────────────────┘
```

---

## Session Context Flow

```
┌─────────────────────────────────────────┐
│  TARS Captain Starts                   │
└─────────────────────────────────────────┘
         │
         ├─ ConversationMemory.loadFromFile()
         │  └─ Reads .tars_session.json
         │
         ├─ Get session_id (or null if new)
         │
         ├─ Create Agent SDK with session_id
         │
         └─ Agent SDK stores session_id
            automatically in all messages

During Execution:
┌────────────────────────────────────────────┐
│  Agent SDK Session (implicit)              │
│                                            │
│  session_id = "sess_abc123def456"          │
│  (provided to every subagent call)         │
└────────────────────────────────────────────┘
         │
         ├─ Subagent 1: contract-coordinator
         │  └─ logs with session_id = "sess_abc123..."
         │
         ├─ Subagent 2: scout-coordinator
         │  └─ logs with session_id = "sess_abc123..."
         │
         ├─ Subagent 3: fleet-manager
         │  └─ logs with session_id = "sess_abc123..."
         │
         └─ All logs linked to same session

Query Later:
┌────────────────────────────────────────────┐
│  SELECT * FROM subagent_logs               │
│  WHERE session_id = "sess_abc123..."       │
│  ORDER BY timestamp                        │
│                                            │
│  Result: Complete agent execution history │
└────────────────────────────────────────────┘
```

---

## File-based Logging Hierarchy

```
mission-logs/
│
├─ subagent-logs/                    [NEW DIRECTORY]
│  ├─ 2025-11-06_1430_contract-coor_sess123.md
│  ├─ 2025-11-06_1431_scout-coor_sess123.md
│  └─ 2025-11-06_1500_contract-coor_sess456.md
│
├─ agents/                           [OPTIONAL]
│  ├─ contract-coordinator/
│  │  ├─ session-sess123.md
│  │  └─ session-sess456.md
│  ├─ scout-coordinator/
│  │  └─ session-sess123.md
│  └─ fleet-manager/
│     └─ session-sess789.md
│
├─ 2025-11-06_1630_afk-session-blocked.md
├─ 2025-11-06_afk-session-1_infrastructure.md
└─ ...existing mission logs
```

---

## Data Relationships

```
┌──────────────────┐
│     Session      │
│  (from SDK)      │
│                  │
│ session_id: str  │
└────────┬─────────┘
         │
         │ (1:N)
         │
┌────────▼──────────────────────┐
│    Subagent Execution          │
│  (subagent_logs table)         │
│                                │
│ • log_id (PK)                  │
│ • session_id (FK to session)   │
│ • agent_name                   │
│ • player_id (FK)               │
│ • timestamp                    │
│ • level                        │
│ • category                     │
│ • message                      │
│ • metadata (JSON)              │
└────────┬───────────────────────┘
         │
         ├─ timestamp index
         ├─ session_id index
         ├─ agent_name index
         └─ level index

Alternative Relationships:

Session ──1:N─── Subagent Logs ──M:1─── Player


Container Logs (Separate):

Container ──1:N─── container_logs ──M:1─── Player

Both log to same Player but different tables.
```

---

## Implementation Sequence

```
Phase 1: Database (FIRST)
┌─────────────────────────────┐
│ 1. Add subagent_logs table  │
│ 2. Add migration logic      │
│ 3. Create indexes           │
└─────────────────┬───────────┘
                  │
                  ▼
Phase 2: Database Methods
┌─────────────────────────────┐
│ 1. log_subagent()           │
│ 2. get_subagent_logs()      │
│ 3. clear_subagent_logs()    │
└─────────────────┬───────────┘
                  │
                  ▼
Phase 3: MCP Tool
┌─────────────────────────────┐
│ 1. Add index.ts case        │
│ 2. Add tool definition      │
│ 3. Create CLI handler       │
└─────────────────┬───────────┘
                  │
                  ▼
Phase 4: Agent Access
┌─────────────────────────────┐
│ 1. Add to agentConfig.ts    │
│ 2. Add to all agents        │
│ 3. Test tool invocation     │
└─────────────────┬───────────┘
                  │
                  ▼
Phase 5: File Logging
┌─────────────────────────────┐
│ 1. Create directory         │
│ 2. Test Write tool usage    │
│ 3. Add examples             │
└─────────────────┬───────────┘
                  │
                  ▼
Phase 6: Session Tracking
┌─────────────────────────────┐
│ 1. Extend ConversationMemory│
│ 2. Track subagent calls     │
│ 3. Test session reconstruction│
└─────────────────────────────┘
```

---

## Summary Comparison

| Aspect | Container Logs | Subagent Logs |
|--------|---|---|
| **Source** | Python daemon/container | TypeScript Agent SDK |
| **Table** | `container_logs` | `subagent_logs` (TODO) |
| **Identifier** | `container_id` | `session_id` + `agent_name` |
| **Storage** | Database + Python logging | Database + Markdown files |
| **Index** | container_id, timestamp | session_id, agent_name, timestamp |
| **Status** | ✅ Implemented | ❌ Not implemented |
| **Query Pattern** | By container ID | By session ID + agent name |
| **Typical Use** | Monitor background tasks | Track agent decisions |

