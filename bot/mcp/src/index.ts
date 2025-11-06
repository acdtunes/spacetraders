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

  constructor() {
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
      "spacetraders",
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
    const cliArgs = this.buildCliArgs(toolName, args);

    if (cliArgs === null) {
      return {
        content: [{ type: "text", text: `Unknown tool: ${toolName}` }],
        isError: true,
      };
    }

    return this.runCliCommand(cliArgs);
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

      // ==================== SCOUTING COMMANDS ====================
      case "scout_markets": {
        const cmd = ["scout", "markets", "--ships", String(args.ships), "--system", String(args.system), "--markets", String(args.markets)];
        if (args.iterations !== undefined) {
          cmd.push("--iterations", String(args.iterations));
        }
        if (args.return_to_start === true) {
          cmd.push("--return-to-start");
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
        return ["daemon", "inspect", "--container-id", String(args.container_id)];

      case "daemon_stop":
        return ["daemon", "stop", "--container-id", String(args.container_id)];

      case "daemon_remove":
        return ["daemon", "remove", "--container-id", String(args.container_id)];

      case "daemon_logs": {
        const cmd = ["daemon", "logs", "--container-id", String(args.container_id), "--player-id", String(args.player_id)];
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
      const child = spawn(this.pythonExecutable, ["-m", "adapters.primary.cli.main", ...args], {
        cwd: this.botDir,
        stdio: ["ignore", "pipe", "pipe"],
        env: {
          ...process.env,
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
