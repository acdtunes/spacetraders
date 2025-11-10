#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { botToolDefinitions } from "./botToolDefinitions.js";
import { DaemonClient } from "./daemonClient.js";

// ES module equivalent of __dirname
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const COMMAND_TIMEOUT_MS = 5 * 60 * 1000; // 5 minutes

interface PythonCommandResult {
  success: boolean;
  stdout: string;
  stderr: string;
  exitCode: number | null;
  timedOut: boolean;
  errorMessage?: string;
}

class SpaceTradersBotServer {
  private server: Server;
  private readonly tools = botToolDefinitions;
  private readonly botDir: string;
  private readonly cliScriptPath: string;
  private readonly pythonExecutable: string;
  private readonly daemonClient: DaemonClient;

  constructor() {
    this.daemonClient = new DaemonClient();
    this.server = new Server(
      {
        name: "spacetraders-mcp-bot",
        version: "3.0.0",
      },
      {
        capabilities: {
          tools: {},
        },
      }
    );

    // Navigate to bot directory (2 levels up from mcp/build/index.js)
    this.botDir = path.resolve(__dirname, "..", "..");
    this.cliScriptPath = path.resolve(
      this.botDir,
      "src",
      "adapters",
      "primary",
      "cli",
      "main.py"
    );
    this.pythonExecutable = this.resolvePythonExecutable();

    this.setupHandlers();
  }

  private resolvePythonExecutable(): string {
    const candidate =
      process.env.MCP_PYTHON_BIN || process.env.PYTHON_BIN || "/usr/bin/python3";
    const hasPathSeparator =
      candidate.includes(path.sep) || candidate.includes("/") || candidate.includes("\\");
    if (hasPathSeparator && !fs.existsSync(candidate)) {
      return "python3";
    }
    return candidate;
  }

  private setupHandlers() {
    this.server.setRequestHandler(ListToolsRequestSchema, async () => ({
      tools: this.tools,
    }));

    this.server.setRequestHandler(CallToolRequestSchema, async (request) => {
      const { name, arguments: argsInput } = request.params;

      try {
        const args = (argsInput ?? {}) as Record<string, unknown>;
        return await this.handleToolCall(name, args);
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error);
        return {
          content: [{ type: "text", text: `Error: ${errorMessage}` }],
          isError: true,
        };
      }
    });
  }

  private async handleToolCall(
    toolName: string,
    args: Record<string, unknown>
  ): Promise<CallToolResult> {
    // Use direct daemon client for daemon operations (fast, no Python spawn)
    // Command operations that create background containers
    const daemonCommands = [
      "daemon_list",
      "daemon_inspect",
      "daemon_stop",
      "daemon_remove",
      "daemon_logs",
      "scout_markets",
      "navigate",
      "dock",
      "orbit",
      "refuel",
      "shipyard_purchase",
      "shipyard_batch_purchase",
      "contract_batch_workflow"
    ];

    if (daemonCommands.includes(toolName)) {
      return this.handleDaemonCommand(toolName, args);
    }

    const cliArgs = this.buildCliArgs(toolName, args);

    if (cliArgs === null) {
      return {
        content: [{ type: "text", text: `Unknown tool: ${toolName}` }],
        isError: true,
      };
    }

    return this.runCliCommand(cliArgs);
  }

  private async handleDaemonCommand(
    toolName: string,
    args: Record<string, unknown>
  ): Promise<CallToolResult> {
    try {
      let result: unknown;

      // Resolve player_id - for commands that need it, use CLI to resolve player_id first
      // This handles --agent flag lookup and default player resolution
      const needsPlayerId = [
        "navigate",
        "dock",
        "orbit",
        "refuel",
        "shipyard_purchase",
        "shipyard_batch_purchase",
        "contract_batch_workflow",
        "scout_markets"
      ];

      let playerId: number | undefined = args.player_id !== undefined ? Number(args.player_id) : undefined;

      // If player_id not provided but needed, resolve via CLI
      if (needsPlayerId.includes(toolName) && playerId === undefined) {
        // Use CLI to resolve player_id
        const cliArgs = this.buildCliArgs(toolName, args);
        if (cliArgs === null) {
          return {
            content: [{ type: "text", text: `Failed to resolve player_id for ${toolName}` }],
            isError: true,
          };
        }

        // For daemon commands, let daemon server resolve agent to player_id
        // Don't hardcode playerId - pass agent parameter instead
        playerId = undefined;
      }

      switch (toolName) {
        case "daemon_list":
          result = await this.daemonClient.listContainers(playerId);
          break;

        case "daemon_inspect":
          result = await this.daemonClient.inspectContainer(String(args.container_id));
          break;

        case "daemon_stop":
          result = await this.daemonClient.stopContainer(String(args.container_id));
          break;

        case "daemon_remove":
          result = await this.daemonClient.removeContainer(String(args.container_id));
          break;

        case "daemon_logs":
          result = await this.daemonClient.getLogs(
            String(args.container_id),
            Number(args.player_id),
            args.level !== undefined ? String(args.level) : undefined,
            args.limit !== undefined ? Number(args.limit) : undefined
          );
          break;

        case "scout_markets":
          result = await this.daemonClient.scoutMarkets(
            String(args.ships).split(',').map(s => s.trim()),
            playerId!,
            String(args.system),
            String(args.markets).split(',').map(m => m.trim()),
            args.iterations !== undefined ? Number(args.iterations) : -1
          );
          break;

        case "navigate":
          result = await this.daemonClient.navigateShip(
            String(args.ship),
            String(args.destination),
            playerId!
          );
          break;

        case "dock":
          result = await this.daemonClient.dockShip(String(args.ship), playerId!);
          break;

        case "orbit":
          result = await this.daemonClient.orbitShip(String(args.ship), playerId!);
          break;

        case "refuel":
          result = await this.daemonClient.refuelShip(
            String(args.ship),
            playerId!,
            args.units !== undefined ? Number(args.units) : undefined
          );
          break;

        case "shipyard_purchase":
          result = await this.daemonClient.purchaseShip(
            String(args.ship),
            String(args.type),
            playerId!,
            args.shipyard !== undefined ? String(args.shipyard) : undefined
          );
          break;

        case "shipyard_batch_purchase":
          result = await this.daemonClient.batchPurchaseShips(
            String(args.ship),
            String(args.type),
            Number(args.quantity),
            Number(args.max_budget),
            playerId,
            args.shipyard !== undefined ? String(args.shipyard) : undefined,
            args.agent !== undefined ? String(args.agent) : undefined
          );
          break;

        case "contract_batch_workflow":
          result = await this.daemonClient.batchContractWorkflow(
            String(args.ship),
            playerId!,
            args.count !== undefined ? Number(args.count) : 1
          );
          break;

        default:
          return {
            content: [{ type: "text", text: `Unknown daemon command: ${toolName}` }],
            isError: true,
          };
      }

      // Format result as text
      const text = typeof result === "string" ? result : JSON.stringify(result, null, 2);
      return { content: [{ type: "text", text }] };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      return {
        content: [{ type: "text", text: `❌ Daemon error: ${errorMessage}` }],
        isError: true,
      };
    }
  }

  private buildCliArgs(toolName: string, args: Record<string, unknown>): string[] | null {
    // Map tool names to CLI command structure
    switch (toolName) {
      // ==================== PLAYER MANAGEMENT ====================
      case "player_register": {
        const cmd = ["player", "register", "--agent", String(args.agent_symbol), "--token", String(args.token)];
        if (args.metadata) {
          cmd.push("--metadata", String(args.metadata));
        }
        return cmd;
      }
      case "player_list":
        return ["player", "list"];

      case "player_info": {
        const cmd = ["player", "info"];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent_symbol !== undefined) {
          cmd.push("--agent", String(args.agent_symbol));
        }
        return cmd;
      }

      // ==================== SHIP MANAGEMENT ====================
      case "ship_list": {
        const cmd = ["ship", "list"];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "ship_info": {
        const cmd = ["ship", "info", "--ship", String(args.ship)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      // ==================== NAVIGATION COMMANDS ====================
      case "navigate": {
        const cmd = ["navigate", "--ship", String(args.ship), "--destination", String(args.destination)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "dock": {
        const cmd = ["dock", "--ship", String(args.ship)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "orbit": {
        const cmd = ["orbit", "--ship", String(args.ship)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "refuel": {
        const cmd = ["refuel", "--ship", String(args.ship)];
        if (args.units !== undefined) {
          cmd.push("--units", String(args.units));
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "plan_route": {
        const cmd = ["plan", "--ship", String(args.ship), "--destination", String(args.destination)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      // ==================== SHIPYARD COMMANDS ====================
      case "shipyard_list": {
        const cmd = ["shipyard", "list", "--waypoint", String(args.waypoint)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "shipyard_purchase": {
        const cmd = ["shipyard", "purchase", "--ship", String(args.ship), "--type", String(args.type)];
        if (args.shipyard !== undefined) {
          cmd.push("--shipyard", String(args.shipyard));
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      case "shipyard_batch_purchase": {
        const cmd = ["shipyard", "batch", "--ship", String(args.ship), "--type", String(args.type), "--quantity", String(args.quantity), "--max-budget", String(args.max_budget)];
        if (args.shipyard !== undefined) {
          cmd.push("--shipyard", String(args.shipyard));
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      // ==================== SCOUTING COMMANDS ====================
      case "scout_markets": {
        const cmd = ["scout", "markets", "--ships", String(args.ships), "--system", String(args.system), "--markets", String(args.markets)];
        if (args.iterations !== undefined) {
          cmd.push("--iterations", String(args.iterations));
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      // ==================== CONTRACT OPERATIONS ====================
      case "contract_batch_workflow": {
        const cmd = ["contract", "batch", "--ship", String(args.ship)];
        if (args.count !== undefined) {
          cmd.push("--count", String(args.count));
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      // ==================== DAEMON OPERATIONS ====================
      case "daemon_list":
        return ["daemon", "list"];

      case "daemon_inspect":
        return ["daemon", "inspect", "--container-id", String(args.container_id), "--json"];

      case "daemon_stop":
        return ["daemon", "stop", "--container-id", String(args.container_id)];

      case "daemon_remove":
        return ["daemon", "remove", "--container-id", String(args.container_id)];

      case "daemon_logs": {
        const cmd = ["daemon", "logs", "--container-id", String(args.container_id), "--player-id", String(args.player_id), "--json"];
        if (args.limit !== undefined) {
          cmd.push("--limit", String(args.limit));
        }
        if (args.level !== undefined) {
          cmd.push("--level", String(args.level));
        }
        return cmd;
      }

      // ==================== CONFIGURATION ====================
      case "config_show":
        return ["config", "show"];

      case "config_set_player":
        return ["config", "set-player", String(args.agent_symbol)];

      case "config_clear_player":
        return ["config", "clear-player"];

      // ==================== CAPTAIN LOGGING ====================
      case "captain_log_entry": {
        const cmd = ["captain", "log", "--type", String(args.entry_type), "--narrative", String(args.narrative)];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        if (args.event_data !== undefined) {
          cmd.push("--event-data", String(args.event_data));
        }
        if (args.tags !== undefined) {
          cmd.push("--tags", String(args.tags));
        }
        if (args.fleet_snapshot !== undefined) {
          cmd.push("--fleet-snapshot", String(args.fleet_snapshot));
        }
        return cmd;
      }

      case "captain_get_logs": {
        const cmd = ["captain", "logs"];
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        if (args.limit !== undefined) {
          cmd.push("--limit", String(args.limit));
        }
        if (args.entry_type !== undefined) {
          cmd.push("--type", String(args.entry_type));
        }
        if (args.since !== undefined) {
          cmd.push("--since", String(args.since));
        }
        if (args.tags !== undefined) {
          cmd.push("--tags", String(args.tags));
        }
        return cmd;
      }

      // ==================== WAYPOINT QUERIES ====================
      case "waypoint_list": {
        const cmd = ["waypoint", "list", "--system", String(args.system)];
        if (args.trait !== undefined) {
          cmd.push("--trait", String(args.trait));
        }
        if (args.has_fuel === true) {
          cmd.push("--has-fuel");
        }
        if (args.player_id !== undefined) {
          cmd.push("--player-id", String(args.player_id));
        }
        if (args.agent !== undefined) {
          cmd.push("--agent", String(args.agent));
        }
        return cmd;
      }

      default:
        return null;
    }
  }

  private async runCliCommand(args: string[]): Promise<CallToolResult> {
    const result = await this.runPythonScript(
      this.cliScriptPath,
      args,
      COMMAND_TIMEOUT_MS
    );

    if (result.timedOut) {
      return {
        content: [{ type: "text", text: "❌ Command timed out after 5 minutes" }],
        isError: true,
      };
    }

    if (result.success) {
      const output = result.stdout.trim();
      const message = output || "✅ Command executed successfully";
      return { content: [{ type: "text", text: message }] };
    }

    let text = `❌ Command failed (exit code: ${result.exitCode ?? "unknown"})\n\n`;
    if (result.stderr) {
      text += `Error:\n${result.stderr}\n\n`;
    }
    if (result.stdout) {
      text += `Output:\n${result.stdout}`;
    }
    if (!result.stderr && !result.stdout && result.errorMessage) {
      text += result.errorMessage;
    }

    return { content: [{ type: "text", text }], isError: true };
  }

  private runPythonScript(
    scriptPath: string,
    args: string[],
    timeoutMs: number
  ): Promise<PythonCommandResult> {
    return new Promise((resolve) => {
      // Invoke as module: python -m adapters.primary.cli.main
      // Pass through DATABASE_URL from environment for PostgreSQL connection

      const child = spawn(this.pythonExecutable, ["-m", "adapters.primary.cli.main", ...args], {
        cwd: this.botDir,
        stdio: ["ignore", "pipe", "pipe"],
        env: {
          ...process.env,  // Includes DATABASE_URL for PostgreSQL
          PYTHONPATH: path.join(this.botDir, "src"),
        },
      });

      let stdout = "";
      let stderr = "";
      let timedOut = false;
      let capturedError: string | undefined;
      let settled = false;

      const timer = setTimeout(() => {
        timedOut = true;
        child.kill("SIGKILL");
      }, timeoutMs);

      child.stdout?.on("data", (chunk) => {
        stdout += chunk.toString();
      });

      child.stderr?.on("data", (chunk) => {
        stderr += chunk.toString();
      });

      child.on("close", (code) => {
        if (settled) {
          return;
        }
        settled = true;
        clearTimeout(timer);
        resolve({
          success: !timedOut && code === 0,
          stdout: stdout.trimEnd(),
          stderr: stderr.trimEnd(),
          exitCode: code,
          timedOut,
          errorMessage: capturedError,
        });
      });

      child.on("error", (error) => {
        capturedError = error.message;
        if (settled) {
          return;
        }
        settled = true;
        clearTimeout(timer);
        resolve({
          success: false,
          stdout: stdout.trimEnd(),
          stderr: stderr.trimEnd(),
          exitCode: null,
          timedOut,
          errorMessage: error.message,
        });
      });
    });
  }

  async run() {
    const transport = new StdioServerTransport();
    await this.server.connect(transport);
    console.error("SpaceTraders MCP Bot Server v3.0 running on stdio");
  }
}

const server = new SpaceTradersBotServer();
server.run().catch((error) => {
  console.error("Failed to start SpaceTraders MCP Bot Server:", error);
  process.exit(1);
});
