# Subagent Logging Implementation Guide

Welcome! This directory contains comprehensive documentation for implementing subagent logging in the SpaceTraders Bot/TARS Captain system.

## Documentation Overview

This guide is organized into three complementary documents:

### 1. [SUBAGENT_LOGGING_ARCHITECTURE.md](./SUBAGENT_LOGGING_ARCHITECTURE.md)
**Comprehensive Technical Analysis** (617 lines, 18 KB)

Start here for a deep dive into the current architecture:
- Current directory structure and codebase layout
- Existing logging mechanisms (database, files, sessions)
- Configuration management and agent infrastructure
- Agent/subagent identification methods
- Session tracking mechanisms
- Recommended implementation locations
- Key observations and constraints

**Best for:** Understanding the big picture, code review, architecture decisions

### 2. [SUBAGENT_LOGGING_QUICK_REFERENCE.md](./SUBAGENT_LOGGING_QUICK_REFERENCE.md)
**Implementation Quick-Start Guide** (366 lines, 9.4 KB)

Practical guide with checklists and code examples:
- Overview of container vs subagent logging
- Implementation checklist (6 phases with checkboxes)
- File locations reference with exact paths
- Data model definitions (Python, TypeScript, SQL)
- MCP tool schema definition
- Log categories and metadata examples
- Query examples
- Integration points and status tracker

**Best for:** Implementation planning, quick lookups, following procedures

### 3. [LOGGING_ARCHITECTURE_DIAGRAM.md](./LOGGING_ARCHITECTURE_DIAGRAM.md)
**Visual Architecture Reference** (428 lines, ASCII diagrams)

ASCII diagrams and visual representations:
- Current architecture overview
- Container logging flow (already implemented)
- Planned subagent logging flow
- Session context flow
- File-based logging hierarchy
- Data relationships and ER-like diagrams
- Implementation sequence with phases
- Summary comparison table

**Best for:** Understanding data flow, visual learners, presentation materials

## Quick Summary

### Current State

| Component | Status | Notes |
|-----------|--------|-------|
| Container logging | ✅ Done | Database + Python logging |
| Session tracking | ✅ Done | Works via Agent SDK |
| File-based logs | ✅ Done | mission-logs/ directory |
| **Subagent logging** | ❌ TODO | Needs database + MCP tool |
| **Agent tool access** | ❌ TODO | Need to add to agentConfig.ts |

### 7 Defined Subagents

1. **contract-coordinator** - Contract fulfillment operations
2. **scout-coordinator** - Market intelligence surveys
3. **fleet-manager** - Ship assignments and optimization
4. **bug-reporter** - Error documentation
5. **feature-proposer** - Strategic analysis and improvements
6. **procurement-coordinator** - Ship purchases
7. **captain-logger** - Narrative mission logging

### What Needs to Be Built

1. **Database Layer** - New `subagent_logs` table
2. **Database Methods** - `log_subagent()`, `get_subagent_logs()`
3. **MCP Tool** - Route logging calls to Python backend
4. **CLI Handler** - Parse arguments and insert into database
5. **Tool Definition** - Define MCP tool schema
6. **Agent Configuration** - Add tool to all agents
7. **File-based Logs** - Optional markdown logging

## Implementation Phases

```
Phase 1: Database Schema        (highest priority)
        ↓
Phase 2: Database Methods
        ↓
Phase 3: MCP Tool (index.ts)
        ↓
Phase 4: Tool Definition
        ↓
Phase 5: Agent Configuration
        ↓
Phase 6: Session Tracking Enhancement
        ↓
Phase 7: File-based Logs (optional)
```

## Key Files to Modify

**Python Backend:**
- `/bot/src/adapters/secondary/persistence/database.py` - Add table & methods
- `/bot/mcp/src/index.ts` - Add tool routing
- `/bot/mcp/src/botToolDefinitions.ts` - Define tool schema
- `/bot/src/adapters/primary/cli/main.py` - Add command handler

**TypeScript/Agent SDK:**
- `/claude-captain/tars/src/agentConfig.ts` - Add tool to agents
- `/claude-captain/tars/src/conversationMemory.ts` - Optional: track sessions

**New Directories:**
- `mission-logs/subagent-logs/` - Create for file-based logs

## Quick Navigation

### By Use Case

**I need to understand the system:**
→ Start with [SUBAGENT_LOGGING_ARCHITECTURE.md](./SUBAGENT_LOGGING_ARCHITECTURE.md)

**I want to implement subagent logging:**
→ Follow [SUBAGENT_LOGGING_QUICK_REFERENCE.md](./SUBAGENT_LOGGING_QUICK_REFERENCE.md)

**I need to see how data flows:**
→ Review [LOGGING_ARCHITECTURE_DIAGRAM.md](./LOGGING_ARCHITECTURE_DIAGRAM.md)

### By Topic

**Database Implementation:**
- Architecture: Section 2.1 & 6.1
- Quick Ref: Section 5 (Data Model)
- Diagrams: Container Logging section

**Session Tracking:**
- Architecture: Section 3.2
- Diagrams: Session Context Flow section

**Agent Configuration:**
- Architecture: Section 3.1
- Quick Ref: Section 4 (File Locations)

**MCP Tool Integration:**
- Architecture: Section 3.3
- Diagrams: Subagent Logging flow section

## Implementation Checklist

```
[ ] 1. Read SUBAGENT_LOGGING_ARCHITECTURE.md (understand system)
[ ] 2. Review key code files (database.py, index.ts, agentConfig.ts)
[ ] 3. Create subagent_logs table in database
[ ] 4. Add database methods (log_subagent, get_subagent_logs)
[ ] 5. Add MCP tool case in index.ts
[ ] 6. Add tool definition to botToolDefinitions.ts
[ ] 7. Create CLI handler in main.py
[ ] 8. Add tool to all agents in agentConfig.ts
[ ] 9. Create mission-logs/subagent-logs/ directory
[ ] 10. Test end-to-end: agent -> MCP -> Python -> database
[ ] 11. Optional: Enhance ConversationMemory for subagent tracking
[ ] 12. Optional: Create file-based logging examples
```

## Database Schema (For Reference)

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
    metadata TEXT,
    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
);

CREATE INDEX idx_subagent_logs_session ON subagent_logs(session_id);
CREATE INDEX idx_subagent_logs_agent ON subagent_logs(agent_name);
CREATE INDEX idx_subagent_logs_timestamp ON subagent_logs(timestamp DESC);
CREATE INDEX idx_subagent_logs_level ON subagent_logs(level);
```

## Key Insights

### No External Dependencies Needed
All necessary infrastructure already exists:
- SQLite3 with WAL mode
- MCP server and tool routing
- Python CLI argument parsing
- Agent SDK session tracking
- File I/O capabilities

### Session Context is Automatic
The Agent SDK provides `session_id` automatically to every subagent call. Just pass it along with logging requests.

### Two-Layer Architecture
- **Database logs** - Structured, queryable, permanent
- **File logs** - Human-readable, narrative format, optional

### Example Log Categories
- `execution` - Tool/command results
- `decision` - Strategic decisions
- `error` - Errors and failures
- `metric` - Performance data
- `planning` - Plan formulation
- `analysis` - Data analysis results

## Useful Queries (After Implementation)

```python
# Get all logs for a session
logs = database.get_subagent_logs("session-abc123", limit=1000)

# Get logs for a specific agent
logs = database.get_subagent_logs("session-abc123", agent_name="contract-coordinator")

# Filter by log level
logs = database.get_subagent_logs("session-abc123", level="ERROR")
```

## Support Files

This directory also contains:
- `SUBAGENT_LOGGING_ARCHITECTURE.md` - Detailed technical analysis
- `SUBAGENT_LOGGING_QUICK_REFERENCE.md` - Implementation guide
- `LOGGING_ARCHITECTURE_DIAGRAM.md` - Visual reference
- `README_LOGGING.md` - This file

## Common Questions

**Q: Where do agents run?**
A: Agents run via Claude Agent SDK, which calls MCP tools. Those tools route to the Python backend.

**Q: How does the system know which agent is logging?**
A: The `agent_name` is passed explicitly when logging (e.g., "contract-coordinator"). The `session_id` comes from the Agent SDK.

**Q: What if the database write fails?**
A: Follow the container logging pattern - catch the exception and fall back to standard Python logging (stderr).

**Q: Can agents write files directly?**
A: Yes! Use the Write SDK tool to save markdown files to `mission-logs/subagent-logs/`.

**Q: Do I need to modify the captain-logger agent?**
A: Not initially. The captain-logger has its own purpose. The new subagent logging is for tracking agent execution, not narrative logs.

## Next Steps

1. **Today:** Read SUBAGENT_LOGGING_ARCHITECTURE.md
2. **Tomorrow:** Review database.py and plan Phase 1
3. **Day 3:** Implement database table and methods
4. **Day 4:** Implement MCP tool routing
5. **Day 5:** Test end-to-end integration
6. **Day 6:** Integrate with agents and test
7. **Day 7:** Add file-based logging (optional)

## Questions or Issues?

Refer to the specific document sections:
- Architecture: See SUBAGENT_LOGGING_ARCHITECTURE.md
- Implementation: See SUBAGENT_LOGGING_QUICK_REFERENCE.md
- Data Flow: See LOGGING_ARCHITECTURE_DIAGRAM.md

---

**Generated:** 2025-11-06  
**Last Updated:** 2025-11-06  
**Status:** Complete exploration, ready for implementation
