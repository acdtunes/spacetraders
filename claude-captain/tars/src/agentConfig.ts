/**
 * Agent configuration and definitions for TARS
 */

import { readFileSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import type { Options } from '@anthropic-ai/claude-agent-sdk';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

/**
 * Load agent prompt from markdown file, stripping YAML frontmatter
 */
function loadPrompt(path: string): string {
  try {
    let content = readFileSync(path, 'utf-8');

    // Strip YAML frontmatter if present
    if (content.startsWith('---')) {
      const parts = content.split('---');
      if (parts.length >= 3) {
        content = parts.slice(2).join('---').trim();
      }
    }

    return content;
  } catch (error) {
    return `Agent prompt not found at ${path}. Please create this file.`;
  }
}

/**
 * Create agent options for TARS Captain
 */
export function createCaptainOptions(): Partial<Options> {
  // Get the parent directory (claude-captain) to access .claude folder
  const projectRoot = join(__dirname, '..', '..');

  return {
    model: 'claude-sonnet-4-5-20250929',
    permissionMode: 'bypassPermissions', // Agents use tools they're configured with - no prompts
    cwd: projectRoot, // Set working directory to claude-captain/ so settings.json is found
    systemPrompt: loadPrompt(join(projectRoot, '.claude/output-styles/tars.md')),

    // Extended thinking mode - Allow TARS to reason deeply about strategic decisions
    maxThinkingTokens: 10000,

    // Main agent tools
    allowedTools: [
      // Core SDK tools
      'Read', 'Write', 'Edit', 'MultiEdit',
      'Grep', 'Glob', 'Task', 'TodoWrite',

      // SpaceTraders MCP tools - READ-ONLY (Captain queries, specialists execute)

      // Player & Fleet Information
      'mcp__spacetraders-bot__player_list',
      'mcp__spacetraders-bot__player_info',
      'mcp__spacetraders-bot__ship_list',
      'mcp__spacetraders-bot__ship_info',

      // Daemon Monitoring
      'mcp__spacetraders-bot__daemon_list',
      'mcp__spacetraders-bot__daemon_inspect',
      'mcp__spacetraders-bot__daemon_logs',

      // System Information
      'mcp__spacetraders-bot__waypoint_list',
      'mcp__spacetraders-bot__plan_route', // Planning only, not execution

      // Configuration
      'mcp__spacetraders-bot__config_show',
      'mcp__spacetraders-bot__config_set_player',

      // NOTE: Captain delegates all EXECUTION to specialist agents:
      // - navigate, dock, orbit, refuel → scouts or dedicated navigator
      // - contract_batch_workflow → contract-coordinator
      // - scout_markets → scout-coordinator
      // - shipyard_batch_purchase → purchasing specialist (future)
      // - daemon_stop, daemon_remove → specialists with oversight
    ],

    // MCP server configuration
    mcpServers: {
      'spacetraders-bot': {
        type: 'stdio',
        command: 'node',
        args: [join(projectRoot, '..', 'bot/mcp/build/index.js')],
        env: {
          MCP_PYTHON_BIN: join(projectRoot, '..', 'bot/uv-python')
        }
      }
    },

    // Subagent definitions
    agents: {
      'contract-coordinator': {
        description: 'Use when you need to run contract fulfillment operations',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/contract-coordinator.md')),
        model: 'haiku',
        tools: [
          'Read', 'Write', 'TodoWrite',
          'mcp__spacetraders-bot__contract_batch_workflow',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__ship_info',
          'mcp__spacetraders-bot__daemon_inspect',
          'mcp__spacetraders-bot__daemon_logs',
        ]
      },

      'scout-coordinator': {
        description: 'Use when you need to manage market intelligence via probe ship network',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/scout-coordinator.md')),
        model: 'haiku',
        tools: [
          'Read', 'Write', 'TodoWrite',
          'mcp__spacetraders-bot__scout_markets',
          'mcp__spacetraders-bot__waypoint_list',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__daemon_inspect',
          'mcp__spacetraders-bot__daemon_logs',
        ]
      },

      'fleet-manager': {
        description: 'Use when you need to optimize ship assignments or analyze fleet composition',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/fleet-manager.md')),
        model: 'haiku',
        tools: [
          'Read', 'Write', 'TodoWrite',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__ship_info',
          'mcp__spacetraders-bot__daemon_list',
          'mcp__spacetraders-bot__daemon_inspect',
        ]
      },

      'bug-reporter': {
        description: 'Use when you encounter persistent errors after retries that need documentation',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/bug-reporter.md')),
        model: 'sonnet',
        tools: [
          'Read', 'Write',
          'mcp__spacetraders-bot__daemon_logs',
          'mcp__spacetraders-bot__daemon_inspect',
          'mcp__spacetraders-bot__ship_info',
        ]
      },

      'feature-proposer': {
        description: 'Use every 2 hours or when performance metrics decline to analyze strategy and propose improvements',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/feature-proposer.md')),
        model: 'sonnet',
        tools: [
          'Read', 'Write', 'Grep', 'Glob',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__daemon_list',
          'mcp__spacetraders-bot__daemon_inspect',
        ]
      },

      'procurement-coordinator': {
        description: 'Use to execute approved ship purchase orders after Admiral approval',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/procurement-coordinator.md')),
        model: 'haiku',
        tools: [
          'Read', 'Write', 'TodoWrite',
          'mcp__spacetraders-bot__shipyard_batch_purchase',
          'mcp__spacetraders-bot__waypoint_list',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__daemon_inspect',
          'mcp__spacetraders-bot__daemon_logs',
        ]
      },

      'captain-logger': {
        description: 'Use to write narrative mission logs for key events (session start/end, major operations, strategic decisions)',
        prompt: loadPrompt(join(projectRoot, '.claude/agents/captain-logger.md')),
        model: 'haiku',
        tools: [
          'Read', 'Write',
          'mcp__spacetraders-bot__captain_log_create',
          'mcp__spacetraders-bot__ship_list',
          'mcp__spacetraders-bot__daemon_list',
          'mcp__spacetraders-bot__daemon_inspect',
          'mcp__spacetraders-bot__player_info',
        ]
      },
    }
  };
}
