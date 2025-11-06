# Claude Captain - TARS Fleet Command

AI-powered fleet management for SpaceTraders using Claude Code CLI.

## Overview

This project configures Claude Code to run as **TARS**, an AI assistant based on the Interstellar character, for managing SpaceTraders fleet operations.

## Quick Start

**To use TARS personality:**
```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/claude-captain/tars
claude
```

TARS will greet you and provide access to fleet operations through MCP tools.

**To use Claude Code normally (without TARS):**
```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/claude-captain
claude
```

This gives you access to MCP tools without the TARS personality.

## Project Structure

```
claude-captain/
├── .claude/
│   └── settings.json              # Project-wide config (MCP servers, permissions)
├── .mcp.json                      # SpaceTraders bot MCP server config
├── strategies.md                  # Research-backed strategies
├── tars/                          # TARS workspace (TypeScript + Claude Code)
│   ├── .claude/
│   │   ├── settings.json          # TARS personality activation
│   │   ├── output-styles/tars.md  # TARS personality definition
│   │   └── agents/                # Specialist agents (6 total)
│   │       ├── fleet-manager.md
│   │       ├── contract-coordinator.md
│   │       ├── scout-coordinator.md
│   │       ├── bug-reporter.md
│   │       ├── feature-proposer.md
│   │       └── captain-logger.md
│   ├── src/                       # TypeScript implementation
│   ├── dist/                      # Compiled output
│   └── package.json
└── README.md                      # This file
```

## TARS Personality

- **Humor:** 75% (witty, dry observations)
- **Honesty:** 90% (brutally truthful about failures)
- **Expertise:** Fleet operations, profit optimization, strategic planning

## How It Works

### 1. Claude Code Integration
When you run `claude` in the `tars/` directory, Claude Code loads:
- TARS personality from `tars/.claude/output-styles/tars.md`
- Pre-approved MCP tool permissions (from root `.claude/settings.json`)
- Access to 6 specialist agents in `tars/.claude/agents/`

When you run `claude` in the root directory, you get normal Claude Code behavior with MCP tools but no TARS personality.

### 2. MCP Tools (SpaceTraders Bot Interface)
TARS controls the SpaceTraders bot via MCP:
- `ship_list`, `ship_info` - Fleet management
- `contract_batch_workflow` - Automated contracts
- `scout_markets` - Market intelligence
- `daemon_list`, `daemon_inspect`, `daemon_logs` - Operations monitoring
- `waypoint_list` - Navigation

### 3. Agent Delegation
TARS delegates complex tasks to specialists using the Task tool:
- **Fleet Manager** - Optimize ship assignments and fleet composition
- **Contract Coordinator** - Execute contract fulfillment operations
- **Scout Coordinator** - Deploy probes for market intelligence
- **Bug Reporter** - Document failures with comprehensive evidence
- **Feature Proposer** - Analyze metrics and propose improvements
- **Captain Logger** - Transform events into narrative mission logs

## Typical Workflow

```
Admiral: "What's the fleet status?"
TARS: [Uses ship_list MCP tool]
TARS: "7 mining drones active, 3 probes scouting, 1 command ship.
       Credits: 47,832 (+12% today). Humor setting: 75%."

Admiral: "Fulfill the current contract"
TARS: [Delegates to contract-coordinator agent]
TARS: "Contract completed with 12,400 credits profit. Not bad for an
       automated system. The contract-coordinator handled the logistics."

Admiral: "Analyze fleet performance"
TARS: [Delegates to fleet-manager agent]
TARS: "Fleet-manager reports miners averaging 5.2k credits/hour.
       Recommend adding 2 more drones if ore markets remain stable."
```

## Standalone TypeScript Implementation

The `tars/` folder contains a standalone TypeScript implementation using the Claude Agent SDK with Ink UI. This is separate from the Claude Code CLI integration.

**To run standalone:**
```bash
cd tars
npm start
```

This provides an alternative way to run TARS with a terminal UI, but the recommended approach is using Claude Code CLI from the root directory.

## Configuration Files

### Root `.claude/settings.json`
- Enables MCP servers project-wide
- Pre-approves SpaceTraders bot tools
- No outputStyle (normal Claude Code behavior at root)

### `tars/.claude/settings.json`
- Sets `outputStyle: "TARS"` to activate personality in tars/ directory only

### `tars/.claude/output-styles/tars.md`
- Defines TARS personality (humor/honesty settings)
- Explains delegation pattern to specialist agents
- Provides communication style examples

### `.mcp.json`
- Configures SpaceTraders bot MCP server
- Points to `../bot/mcp/build/index.js`
- Sets Python binary path for bot execution

### `strategies.md`
- Research-backed strategies for fleet operations
- Referenced by feature-proposer agent
- Contains profitability targets and best practices

## Agent Responsibilities

### Fleet Manager
- Optimize ship assignments based on performance metrics
- Recommend fleet composition changes
- Calculate profitability per ship

### Contract Coordinator
- Execute contract fulfillment using `contract_batch_workflow`
- Calculate contract economics (profit after costs)
- Monitor daemon execution and handle errors

### Scout Coordinator
- Deploy probes for market intelligence
- Maintain optimal scout coverage (1 probe per 2-3 markets)
- Identify trade route opportunities

### Bug Reporter
- Document operational failures with comprehensive evidence
- Collect logs and ship state at time of error
- Provide root cause analysis and fix suggestions

### Feature Proposer
- Analyze performance metrics every 2 hours
- Compare current state vs proven strategies
- Generate feature proposals with ROI estimates

### Captain Logger
- Transform operational data into narrative mission logs
- Maintain TARS voice (75% humor, 90% honesty)
- Provide continuity across sessions

## SpaceTraders Bot

The bot lives in a separate repository at `../bot/`. TARS interacts with it exclusively through MCP tools - it does NOT read or modify bot code directly.

**Bot Responsibilities:**
- Execute ship operations (mining, trading, navigation)
- Provide MCP interface for fleet control
- Store operational state and metrics

**TARS Responsibilities:**
- Strategic decisions (which contracts, fleet composition)
- Delegation to specialist agents
- Metric analysis and optimization

## Notes

- Output styles are deprecated (removing Nov 5, 2025) but still work for now
- MCP tools are pre-approved in settings (no permission prompts)
- Agent files do NOT use YAML frontmatter (Claude Code doesn't require it)
- Reports will be written to `reports/bugs/` and `reports/features/` when generated

## Development

To modify TARS behavior:
1. Edit `tars/.claude/output-styles/tars.md` for personality changes
2. Edit `tars/.claude/agents/*.md` for specialist agent behavior
3. Edit `strategies.md` for strategic guidance updates
4. Edit root `.claude/settings.json` for MCP tool permissions
5. Edit `tars/.claude/settings.json` for TARS activation

Run `claude` from the `tars/` directory to test TARS changes immediately.
