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
  Tool,
} from "@modelcontextprotocol/sdk/types.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { botToolDefinitions } from "./botToolDefinitions.js";

// ES module equivalent of __dirname
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const BOT_COMMAND_TIMEOUT_MS = 5 * 60 * 1000; // 5 minutes
const BRIDGE_COMMAND_TIMEOUT_MS = 60 * 1000; // 60 seconds

interface SpaceTradersConfig {
  token?: string;
}

type ToolHandler = (args: Record<string, unknown>) => Promise<CallToolResult>;

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
  private readonly toolHandlers: Map<string, ToolHandler> = new Map();
  private readonly botDir: string;
  private readonly botScriptPath: string;
  private readonly bridgeScriptPath: string;
  private readonly pythonExecutable: string;

  constructor(config: SpaceTradersConfig = {}) {
    this.server = new Server(
      {
        name: "spacetraders-mcp-bot",
        version: "2.0.0",
      },
      {
        capabilities: {
          tools: {},
        },
      }
    );

    this.botDir = path.resolve(__dirname, "..", "..", "..", "bot");
    this.botScriptPath = path.resolve(this.botDir, "bot_bot.py");
    this.bridgeScriptPath = path.resolve(this.botDir, "mcp_bridge.py");
    this.pythonExecutable = this.resolvePythonExecutable();

    if (config.token) {
      process.env.SPACETRADERS_TOKEN = config.token;
    }

    this.registerHandlers();
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
      const handler = this.toolHandlers.get(name);
      if (!handler) {
        return {
          content: [{ type: "text", text: `Error: Unknown tool: ${name}` }],
          isError: true,
        };
      }

      try {
        const args = (argsInput ?? {}) as Record<string, unknown>;
        return await handler(args);
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error);
        return {
          content: [{ type: "text", text: `Error: ${errorMessage}` }],
          isError: true,
        };
      }
    });
  }

  private registerHandlers() {
    const directHandlers = new Map<string, ToolHandler>([
      ["bot_list_players", () => this.listPlayers()],
      ["bot_register_player", (args) => this.registerPlayer(args)],
      ["bot_get_player_info", (args) => this.getPlayerInfo(args)],
      ["bot_market_waypoint", (args) => this.marketWaypoint(args)],
      ["bot_market_find_sellers", (args) => this.marketFindSellers(args)],
      ["bot_market_find_buyers", (args) => this.marketFindBuyers(args)],
      ["bot_market_recent_updates", (args) => this.marketRecentUpdates(args)],
      ["bot_market_find_stale", (args) => this.marketStale(args)],
      ["bot_market_summarize_good", (args) => this.marketSummarizeGood(args)],
      ["bot_wait_minutes", (args) => this.waitMinutes(args)],
    ]);

    for (const [name, handler] of directHandlers) {
      this.toolHandlers.set(name, handler);
    }

    for (const tool of this.tools) {
      if (this.toolHandlers.has(tool.name)) {
        continue;
      }
      this.toolHandlers.set(tool.name, (args) =>
        this.handleBotCliTool(tool.name, args as Record<string, unknown>)
      );
    }
  }

  private async listPlayers(): Promise<CallToolResult> {
    const data = await this.runBridgeCommand<{ players: Array<Record<string, unknown>> }>([
      "players",
      "list",
    ]);

    const players = data.players ?? [];
    if (!players.length) {
      return { content: [{ type: "text", text: "No players registered in database." }] };
    }

    let text = "Registered Players:\n\n";
    for (const player of players) {
      const id = player.player_id;
      const symbol = player.agent_symbol;
      const created = player.created_at;
      const lastActive = player.last_active;
      text += `• Player ID ${id}: ${symbol}\n`;
      text += `  Registered: ${created}\n`;
      if (lastActive) {
        text += `  Last Active: ${lastActive}\n`;
      }
      text += "\n";
    }

    return { content: [{ type: "text", text: text.trimEnd() }] };
  }

  private async registerPlayer(args: Record<string, unknown>): Promise<CallToolResult> {
    this.ensureArgs("bot_register_player", args, ["agent_symbol", "token"]);

    const payload = await this.runBridgeCommand<{ player: Record<string, unknown> }>([
      "players",
      "register",
      "--agent-symbol",
      String(args.agent_symbol),
      "--token",
      String(args.token),
    ]);

    const player = payload.player;
    const token = String(args.token);
    const tokenPreview = this.formatTokenPreview(token);

    let text = "✅ Player registered successfully!\n\n";
    text += `Player ID: ${player.player_id}\n`;
    text += `Agent Symbol: ${player.agent_symbol}\n`;
    text += `Token stored: ${tokenPreview}\n\n`;
    text += `Use player_id=${player.player_id} for all operations.`;

    return { content: [{ type: "text", text }] };
  }

  private async waitMinutes(args: Record<string, unknown>): Promise<CallToolResult> {
    const minutesRaw = Number(args.minutes);
    if (!Number.isFinite(minutesRaw)) {
      return {
        content: [{ type: "text", text: "❌ 'minutes' must be a number" }],
        isError: true,
      };
    }

    if (minutesRaw <= 0) {
      return {
        content: [{ type: "text", text: "❌ 'minutes' must be greater than zero" }],
        isError: true,
      };
    }

    const MAX_MINUTES = 60;
    const minutes = Math.min(minutesRaw, MAX_MINUTES);
    const milliseconds = Math.round(minutes * 60 * 1000);

    await new Promise((resolve) => setTimeout(resolve, milliseconds));

    const reason = args.reason ? String(args.reason) : undefined;
    let message = `⏱️ Waited ${minutes.toFixed(2)} minute(s).`;
    if (minutesRaw > MAX_MINUTES) {
      message += ` (Requested ${minutesRaw} minutes but capped at ${MAX_MINUTES}.)`;
    }
    if (reason) {
      message += ` Reason: ${reason}`;
    }

    return { content: [{ type: "text", text: message }] };
  }

  private async getPlayerInfo(args: Record<string, unknown>): Promise<CallToolResult> {
    if (args.player_id === undefined && args.agent_symbol === undefined) {
      return {
        content: [{ type: "text", text: "❌ Must provide player_id or agent_symbol" }],
        isError: true,
      };
    }

    const command = ["players", "info"];
    if (args.player_id !== undefined) {
      command.push("--player-id", String(args.player_id));
    }
    if (args.agent_symbol !== undefined) {
      command.push("--agent-symbol", String(args.agent_symbol));
    }

    const payload = await this.runBridgeCommand<{ player: Record<string, unknown> | null }>(
      command
    );

    const player = payload.player;
    if (!player) {
      return { content: [{ type: "text", text: "❌ Player not found" }], isError: true };
    }

    const token = String(player.token ?? "");
    const tokenPreview = token ? this.formatTokenPreview(token) : "<missing>";

    let text = "Player Information:\n\n";
    text += `Player ID: ${player.player_id}\n`;
    text += `Agent Symbol: ${player.agent_symbol}\n`;
    text += `Token: ${tokenPreview}\n`;
    text += `Registered: ${player.created_at}\n`;
    if (player.last_active) {
      text += `Last Active: ${player.last_active}\n`;
    }

    return { content: [{ type: "text", text }] };
  }

  private async marketWaypoint(args: Record<string, unknown>): Promise<CallToolResult> {
    this.ensureArgs("bot_market_waypoint", args, ["waypoint_symbol"]);

    const command = [
      "market",
      "waypoint",
      "--waypoint-symbol",
      String(args.waypoint_symbol),
    ];

    if (args.good_symbol) {
      command.push("--good-symbol", String(args.good_symbol));
      const payload = await this.runBridgeCommand<{ entry: Record<string, unknown> | null }>(
        command
      );
      const entry = payload.entry;
      if (!entry) {
        return {
          content: [
            {
              type: "text",
              text: `No stored market data for ${args.good_symbol} at ${args.waypoint_symbol}.`,
            },
          ],
        };
      }
      const text = `Market intel for ${args.waypoint_symbol} (good ${args.good_symbol}):\n${this.formatMarketEntry(entry)}`;
      return { content: [{ type: "text", text }] };
    }

    const payload = await this.runBridgeCommand<{ goods: Array<Record<string, unknown>> }>(
      command
    );
    const goods = payload.goods ?? [];
    if (!goods.length) {
      return {
        content: [
          {
            type: "text",
            text: `No stored market data for waypoint ${args.waypoint_symbol}.`,
          },
        ],
      };
    }

    const lines = [`Market intel for ${args.waypoint_symbol}:`];
    for (const entry of goods) {
      lines.push(this.formatMarketEntry(entry));
    }

    return { content: [{ type: "text", text: lines.join("\n") }] };
  }

  private async marketFindSellers(args: Record<string, unknown>): Promise<CallToolResult> {
    this.ensureArgs("bot_market_find_sellers", args, ["good_symbol"]);

    const command = ["market", "find_sellers", "--good-symbol", String(args.good_symbol)];
    if (args.system) {
      command.push("--system", String(args.system));
    }
    if (args.min_supply) {
      command.push("--min-supply", String(args.min_supply));
    }
    if (args.updated_within_hours !== undefined) {
      command.push("--updated-within-hours", String(args.updated_within_hours));
    }
    if (args.limit !== undefined) {
      command.push("--limit", String(args.limit));
    }

    const payload = await this.runBridgeCommand<{ markets: Array<Record<string, unknown>> }>(
      command
    );

    const markets = payload.markets ?? [];
    if (!markets.length) {
      return {
        content: [
          {
            type: "text",
            text: `No selling markets found for ${args.good_symbol}.`,
          },
        ],
      };
    }

    const lines = [`Best selling markets for ${args.good_symbol}:`];
    markets.forEach((row, index) => {
      const price = row.purchase_price ?? "?";
      const supply = row.supply ?? "?";
      const updated = row.last_updated ?? "unknown";
      lines.push(
        `${index + 1}. ${row.waypoint_symbol} → buy ${price} cr (supply ${supply}, updated ${updated})`
      );
    });

    return { content: [{ type: "text", text: lines.join("\n") }] };
  }

  private async marketFindBuyers(args: Record<string, unknown>): Promise<CallToolResult> {
    this.ensureArgs("bot_market_find_buyers", args, ["good_symbol"]);

    const command = ["market", "find_buyers", "--good-symbol", String(args.good_symbol)];
    if (args.system) {
      command.push("--system", String(args.system));
    }
    if (args.min_activity) {
      command.push("--min-activity", String(args.min_activity));
    }
    if (args.updated_within_hours !== undefined) {
      command.push("--updated-within-hours", String(args.updated_within_hours));
    }
    if (args.limit !== undefined) {
      command.push("--limit", String(args.limit));
    }

    const payload = await this.runBridgeCommand<{ markets: Array<Record<string, unknown>> }>(
      command
    );

    const markets = payload.markets ?? [];
    if (!markets.length) {
      return {
        content: [
          {
            type: "text",
            text: `No buying markets found for ${args.good_symbol}.`,
          },
        ],
      };
    }

    const lines = [`Best buyers for ${args.good_symbol}:`];
    markets.forEach((row, index) => {
      const price = row.sell_price ?? "?";
      const activity = row.activity ?? "?";
      const updated = row.last_updated ?? "unknown";
      lines.push(
        `${index + 1}. ${row.waypoint_symbol} → sell ${price} cr (activity ${activity}, updated ${updated})`
      );
    });

    return { content: [{ type: "text", text: lines.join("\n") }] };
  }

  private async marketRecentUpdates(args: Record<string, unknown>): Promise<CallToolResult> {
    const command = ["market", "recent_updates"];
    if (args.system) {
      command.push("--system", String(args.system));
    }
    if (args.limit !== undefined) {
      command.push("--limit", String(args.limit));
    }

    const payload = await this.runBridgeCommand<{ updates: Array<Record<string, unknown>> }>(
      command
    );

    const updates = payload.updates ?? [];
    if (!updates.length) {
      return { content: [{ type: "text", text: "No recent market updates recorded." }] };
    }

    const lines = ["Most recent market updates:"];
    updates.forEach((row, index) => {
      const purchase = row.purchase_price;
      const sell = row.sell_price;
      const purchaseText =
        purchase !== null && purchase !== undefined ? `buy ${purchase} cr` : "buy n/a";
      const sellText = sell !== null && sell !== undefined ? `sell ${sell} cr` : "sell n/a";
      const updated = row.last_updated ?? "unknown";
      lines.push(
        `${index + 1}. ${row.waypoint_symbol} ${row.good_symbol} → ${purchaseText}, ${sellText} (updated ${updated})`
      );
    });

    return { content: [{ type: "text", text: lines.join("\n") }] };
  }

  private async marketStale(args: Record<string, unknown>): Promise<CallToolResult> {
    this.ensureArgs("bot_market_find_stale", args, ["max_age_hours"]);

    const command = [
      "market",
      "stale",
      "--max-age-hours",
      String(args.max_age_hours),
    ];
    if (args.system) {
      command.push("--system", String(args.system));
    }

    const payload = await this.runBridgeCommand<{ stale: Array<Record<string, unknown>> }>(
      command
    );

    const rows = payload.stale ?? [];
    if (!rows.length) {
      const systemText = args.system ? ` in system ${args.system}` : "";
      return {
        content: [
          {
            type: "text",
            text: `No market entries older than ${args.max_age_hours} hours${systemText}.`,
          },
        ],
      };
    }

    const lines = [`Market intel older than ${args.max_age_hours} hours:`];
    rows.forEach((row, index) => {
      const updated = row.last_updated ?? "unknown";
      lines.push(
        `${index + 1}. ${row.waypoint_symbol} ${row.good_symbol} (last updated ${updated})`
      );
    });

    return { content: [{ type: "text", text: lines.join("\n") }] };
  }

  private async marketSummarizeGood(
    args: Record<string, unknown>
  ): Promise<CallToolResult> {
    this.ensureArgs("bot_market_summarize_good", args, ["good_symbol"]);

    const command = ["market", "summarize_good", "--good-symbol", String(args.good_symbol)];
    if (args.system) {
      command.push("--system", String(args.system));
    }

    const payload = await this.runBridgeCommand<{ summary: Record<string, unknown> | null }>(
      command
    );

    const summary = payload.summary;
    if (!summary) {
      const systemText = args.system ? ` in system ${args.system}` : "";
      return {
        content: [
          {
            type: "text",
            text: `No market summary available for ${args.good_symbol}${systemText}.`,
          },
        ],
      };
    }

    const format = (value: unknown) => (value === null || value === undefined ? "n/a" : value);
    const systemSuffix = args.system ? ` in ${args.system}` : "";

    const text =
      `Summary for ${args.good_symbol}${systemSuffix}:\n` +
      `Markets tracked: ${format(summary.market_count)}\n` +
      `Purchase price (min/avg/max): ${format(summary.min_purchase_price)} / ${format(summary.avg_purchase_price)} / ${format(summary.max_purchase_price)}\n` +
      `Sell price (min/avg/max): ${format(summary.min_sell_price)} / ${format(summary.avg_sell_price)} / ${format(summary.max_sell_price)}\n` +
      `Last updated: ${format(summary.last_updated)}`;

    return { content: [{ type: "text", text }] };
  }

  private async handleBotCliTool(
    name: string,
    args: Record<string, unknown>
  ): Promise<CallToolResult> {
    const command: string[] = [];

    switch (name) {
      case "bot_fleet_status": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("status", "--player-id", String(args.player_id));
        if (args.ships) {
          command.push("--ships", String(args.ships));
        }
        break;
      }
      case "bot_fleet_monitor": {
        this.ensureArgs(name, args, ["player_id", "ships"]);
        command.push(
          "monitor",
          "--player-id",
          String(args.player_id),
          "--ships",
          String(args.ships)
        );
        if (args.interval !== undefined) {
          command.push("--interval", String(args.interval));
        }
        if (args.duration !== undefined) {
          command.push("--duration", String(args.duration));
        }
        break;
      }
      case "bot_run_mining": {
        this.ensureArgs(name, args, ["player_id", "ship", "asteroid", "market"]);
        // Auto-generate daemon ID if not provided
        const daemonId = `mine-${String(args.ship)}-${Date.now()}`;
        command.push(
          "daemon",
          "start",
          "--player-id",
          String(args.player_id),
          "--daemon-id",
          daemonId,
          "mine",
          "--ship",
          String(args.ship),
          "--asteroid",
          String(args.asteroid),
          "--market",
          String(args.market)
        );
        if (args.cycles !== undefined) {
          command.push("--cycles", String(args.cycles));
        }
        break;
      }
      case "bot_run_trading": {
        this.ensureArgs(name, args, [
          "player_id",
          "ship",
          "good",
          "buy_from",
          "sell_to",
        ]);
        // Auto-generate daemon ID if not provided
        const daemonId = `trade-${String(args.ship)}-${Date.now()}`;
        command.push(
          "daemon",
          "start",
          "--player-id",
          String(args.player_id),
          "--daemon-id",
          daemonId,
          "trade",
          "--ship",
          String(args.ship),
          "--good",
          String(args.good),
          "--buy-from",
          String(args.buy_from),
          "--sell-to",
          String(args.sell_to)
        );
        if (args.duration !== undefined) {
          command.push("--duration", String(args.duration));
        }
        if (args.min_profit !== undefined) {
          command.push("--min-profit", String(args.min_profit));
        }
        break;
      }
      case "bot_purchase_ship": {
        this.ensureArgs(name, args, [
          "player_id",
          "ship",
          "shipyard",
          "ship_type",
          "max_budget",
        ]);
        command.push(
          "purchase-ship",
          "--player-id",
          String(args.player_id),
          "--ship",
          String(args.ship),
          "--shipyard",
          String(args.shipyard),
          "--ship-type",
          String(args.ship_type),
          "--max-budget",
          String(args.max_budget)
        );
        if (args.quantity !== undefined) {
          command.push("--quantity", String(args.quantity));
        }
        break;
      }
      case "bot_trade_plan": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "trade-plan",
          "--player-id",
          String(args.player_id),
          "--ship",
          String(args.ship)
        );
        if (args.max_stops !== undefined) {
          command.push("--max-stops", String(args.max_stops));
        }
        if (args.system) {
          command.push("--system", String(args.system));
        }
        break;
      }
      case "bot_multileg_trade": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        // Auto-generate daemon ID if not provided
        const daemonId = `multileg-${String(args.ship)}-${Date.now()}`;
        command.push(
          "daemon",
          "start",
          "--player-id",
          String(args.player_id),
          "--daemon-id",
          daemonId,
          "trade",
          "--ship",
          String(args.ship)
        );
        if (args.system !== undefined) {
          command.push("--system", String(args.system));
        }
        if (args.max_stops !== undefined) {
          command.push("--max-stops", String(args.max_stops));
        }
        if (args.cycles !== undefined) {
          command.push("--cycles", String(args.cycles));
        }
        if (args.duration !== undefined) {
          command.push("--duration", String(args.duration));
        }
        break;
      }
      case "bot_negotiate_contract": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "negotiate",
          "--player-id",
          String(args.player_id),
          "--ship",
          String(args.ship)
        );
        break;
      }
      case "bot_fulfill_contract": {
        this.ensureArgs(name, args, ["player_id", "ship", "contract_id"]);
        // Auto-generate daemon ID if not provided
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
          String(args.ship),
          "--contract-id",
          String(args.contract_id)
        );
        if (args.buy_from) {
          command.push("--buy-from", String(args.buy_from));
        }
        break;
      }
      case "bot_build_graph": {
        this.ensureArgs(name, args, ["player_id", "system"]);
        command.push(
          "graph-build",
          "--player-id",
          String(args.player_id),
          "--system",
          String(args.system)
        );
        break;
      }
      case "bot_plan_route": {
        this.ensureArgs(name, args, [
          "player_id",
          "ship",
          "system",
          "start",
          "goal",
        ]);
        command.push(
          "route-plan",
          "--player-id",
          String(args.player_id),
          "--ship",
          String(args.ship),
          "--system",
          String(args.system),
          "--start",
          String(args.start),
          "--goal",
          String(args.goal)
        );
        break;
      }
      case "bot_find_mining_opportunities": {
        this.ensureArgs(name, args, ["player_id", "system"]);
        command.push(
          "util",
          "--player-id",
          String(args.player_id),
          "--type",
          "find-mining",
          "--system",
          String(args.system)
        );
        break;
      }
      case "bot_daemon_start": {
        this.ensureArgs(name, args, ["player_id", "operation", "args"]);
        const extraArgs = Array.isArray(args.args)
          ? (args.args as unknown[]).map((value) => String(value))
          : [];
        command.push(
          "daemon",
          "start",
          "--player-id",
          String(args.player_id)
        );
        if (args.daemon_id) {
          command.push("--daemon-id", String(args.daemon_id));
        }
        command.push(String(args.operation), ...extraArgs);
        break;
      }
      case "bot_daemon_stop": {
        this.ensureArgs(name, args, ["player_id", "daemon_id"]);
        command.push(
          "daemon",
          "stop",
          String(args.daemon_id),
          "--player-id",
          String(args.player_id)
        );
        break;
      }
      case "bot_daemon_status": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("daemon", "status", "--player-id", String(args.player_id));
        if (args.daemon_id) {
          command.push(String(args.daemon_id));
        }
        break;
      }
      case "bot_daemon_logs": {
        this.ensureArgs(name, args, ["player_id", "daemon_id"]);
        command.push(
          "daemon",
          "logs",
          String(args.daemon_id),
          "--player-id",
          String(args.player_id)
        );
        if (args.lines !== undefined) {
          command.push("--lines", String(args.lines));
        }
        break;
      }
      case "bot_daemon_cleanup": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("daemon", "cleanup", "--player-id", String(args.player_id));
        break;
      }
      case "bot_assignments_list": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("assignments", "list", "--player-id", String(args.player_id));
        if (args.include_stale) {
          command.push("--include-stale");
        }
        break;
      }
      case "bot_assignments_assign": {
        this.ensureArgs(name, args, [
          "player_id",
          "ship",
          "operator",
          "daemon_id",
          "operation",
        ]);
        command.push(
          "assignments",
          "assign",
          "--ship",
          String(args.ship),
          "--operator",
          String(args.operator),
          "--daemon-id",
          String(args.daemon_id),
          "--op-type",
          String(args.operation),
          "--player-id",
          String(args.player_id)
        );
        if (args.duration !== undefined) {
          command.push("--duration", String(args.duration));
        }
        break;
      }
      case "bot_assignments_available": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "assignments",
          "available",
          String(args.ship),
          "--player-id",
          String(args.player_id)
        );
        break;
      }
      case "bot_assignments_release": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "assignments",
          "release",
          String(args.ship),
          "--player-id",
          String(args.player_id)
        );
        if (args.reason) {
          command.push("--reason", String(args.reason));
        }
        break;
      }
      case "bot_assignments_find": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("assignments", "find", "--player-id", String(args.player_id));
        if (args.cargo_min !== undefined) {
          command.push("--cargo-min", String(args.cargo_min));
        }
        if (args.fuel_min !== undefined) {
          command.push("--fuel-min", String(args.fuel_min));
        }
        break;
      }
      case "bot_assignments_status": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "assignments",
          "status",
          String(args.ship),
          "--player-id",
          String(args.player_id)
        );
        break;
      }
      case "bot_assignments_sync": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("assignments", "sync", "--player-id", String(args.player_id));
        break;
      }
      case "bot_assignments_reassign": {
        this.ensureArgs(name, args, ["player_id", "ships", "from_operation"]);
        command.push(
          "assignments",
          "reassign",
          "--ships",
          String(args.ships),
          "--from-operation",
          String(args.from_operation),
          "--player-id",
          String(args.player_id)
        );
        if (args.no_stop) {
          command.push("--no-stop");
        }
        if (args.timeout !== undefined) {
          command.push("--timeout", String(args.timeout));
        }
        break;
      }
      case "bot_assignments_init": {
        this.ensureArgs(name, args, ["player_id"]);
        command.push("assignments", "init", "--player-id", String(args.player_id));
        break;
      }
      case "bot_find_fuel": {
        this.ensureArgs(name, args, ["player_id", "ship"]);
        command.push(
          "util",
          "--player-id",
          String(args.player_id),
          "--type",
          "find-fuel",
          "--ship",
          String(args.ship)
        );
        break;
      }
      case "bot_calculate_distance": {
        this.ensureArgs(name, args, ["player_id", "waypoint1", "waypoint2"]);
        command.push(
          "util",
          "--player-id",
          String(args.player_id),
          "--type",
          "distance",
          "--waypoint1",
          String(args.waypoint1),
          "--waypoint2",
          String(args.waypoint2)
        );
        break;
      }
      case "bot_captain_log_init": {
        this.ensureArgs(name, args, ["agent"]);
        command.push("captain-log", "init", "--agent", String(args.agent));
        if (args.player_id !== undefined) {
          command.push("--player-id", String(args.player_id));
        }
        break;
      }
      case "bot_captain_log_session_start": {
        this.ensureArgs(name, args, ["agent", "objective"]);
        command.push(
          "captain-log",
          "session-start",
          "--agent",
          String(args.agent),
          "--objective",
          String(args.objective)
        );
        if (args.player_id !== undefined) {
          command.push("--player-id", String(args.player_id));
        }
        if (args.operator) {
          command.push("--operator", String(args.operator));
        }
        break;
      }
      case "bot_captain_log_session_end": {
        this.ensureArgs(name, args, ["agent"]);
        command.push("captain-log", "session-end", "--agent", String(args.agent));
        if (args.player_id !== undefined) {
          command.push("--player-id", String(args.player_id));
        }
        break;
      }
      case "bot_captain_log_entry": {
        this.ensureArgs(name, args, ["agent", "entry_type"]);
        command.push(
          "captain-log",
          "entry",
          "--agent",
          String(args.agent),
          "--type",
          String(args.entry_type)
        );
        if (args.player_id !== undefined) {
          command.push("--player-id", String(args.player_id));
        }
        if (args.operator) {
          command.push("--operator", String(args.operator));
        }
        if (args.ship) {
          command.push("--ship", String(args.ship));
        }
        if (args.daemon_id) {
          command.push("--daemon-id", String(args.daemon_id));
        }
        if (args.op_type) {
          command.push("--op-type", String(args.op_type));
        }
        if (args.error) {
          command.push("--error", String(args.error));
        }
        if (args.resolution) {
          command.push("--resolution", String(args.resolution));
        }
        break;
      }
      case "bot_captain_log_search": {
        this.ensureArgs(name, args, ["agent"]);
        command.push("captain-log", "search", "--agent", String(args.agent));
        if (args.tag) {
          command.push("--tag", String(args.tag));
        }
        if (args.timeframe !== undefined) {
          command.push("--timeframe", String(args.timeframe));
        }
        break;
      }
      case "bot_captain_log_report": {
        this.ensureArgs(name, args, ["agent"]);
        command.push("captain-log", "report", "--agent", String(args.agent));
        if (args.player_id !== undefined) {
          command.push("--player-id", String(args.player_id));
        }
        if (args.duration !== undefined) {
          command.push("--duration", String(args.duration));
        }
        break;
      }
      case "bot_scout_coordinator_start": {
        this.ensureArgs(name, args, ["player_id", "system", "ships"]);
        command.push(
          "scout-coordinator",
          "start",
          "--player-id",
          String(args.player_id),
          "--system",
          String(args.system),
          "--ships",
          String(args.ships)
        );
        if (args.algorithm) {
          command.push("--algorithm", String(args.algorithm));
        }
        break;
      }
      case "bot_scout_coordinator_stop": {
        this.ensureArgs(name, args, ["system"]);
        command.push(
          "scout-coordinator",
          "stop",
          "--system",
          String(args.system)
        );
        break;
      }
      case "bot_scout_coordinator_status": {
        this.ensureArgs(name, args, ["system"]);
        command.push(
          "scout-coordinator",
          "status",
          "--system",
          String(args.system)
        );
        break;
      }
      case "bot_waypoint_query": {
        this.ensureArgs(name, args, ["player_id", "system"]);
        command.push(
          "waypoint-query",
          "--player-id",
          String(args.player_id),
          "--system",
          String(args.system)
        );
        if (args.type) {
          command.push("--type", String(args.type));
        }
        if (args.trait) {
          command.push("--trait", String(args.trait));
        }
        if (args.exclude) {
          command.push("--exclude", String(args.exclude));
        }
        if (args.has_fuel) {
          command.push("--has-fuel");
        }
        break;
      }
      default:
        return {
          content: [{ type: "text", text: `Unknown tool: ${name}` }],
          isError: true,
        };
    }

    return this.runBotCommand(command);
  }

  private async runBotCommand(args: string[]): Promise<CallToolResult> {
    const result = await this.runPythonScript(
      this.botScriptPath,
      args,
      BOT_COMMAND_TIMEOUT_MS
    );

    if (result.timedOut) {
      return {
        content: [{ type: "text", text: "❌ Command timed out after 5 minutes" }],
        isError: true,
      };
    }

    if (result.success) {
      const output = result.stdout.trim();
      const message = output
        ? `✅ Command executed successfully\n\n${output}`
        : "✅ Command executed successfully";
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

  private async runBridgeCommand<T>(args: string[]): Promise<T> {
    const result = await this.runPythonScript(
      this.bridgeScriptPath,
      args,
      BRIDGE_COMMAND_TIMEOUT_MS
    );

    let payload: any = {};
    if (result.stdout.trim()) {
      try {
        payload = JSON.parse(result.stdout.trim());
      } catch (error) {
        throw new Error(
          `Failed to parse bridge output: ${(error as Error).message}`
        );
      }
    }

    if (!result.success) {
      const message =
        payload?.data?.error || result.stderr || result.errorMessage || "Bridge command failed";
      throw new Error(String(message));
    }

    if (!payload?.success) {
      const message = payload?.data?.error || "Bridge command returned no data";
      throw new Error(String(message));
    }

    return payload.data as T;
  }

  private runPythonScript(
    scriptPath: string,
    args: string[],
    timeoutMs: number
  ): Promise<PythonCommandResult> {
    return new Promise((resolve) => {
      const child = spawn(this.pythonExecutable, [scriptPath, ...args], {
        cwd: this.botDir,
        stdio: ["ignore", "pipe", "pipe"],
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

  private ensureArgs(name: string, args: Record<string, unknown>, fields: string[]) {
    for (const field of fields) {
      if (args[field] === undefined || args[field] === null) {
        throw new Error(`Missing required argument '${field}' for ${name}`);
      }
    }
  }

  private formatTokenPreview(token: string): string {
    if (token.length <= 20) {
      return token;
    }
    return `${token.slice(0, 10)}...${token.slice(-10)}`;
  }

  private formatMarketEntry(entry: Record<string, unknown>): string {
    const purchase = entry.purchase_price;
    const sell = entry.sell_price;
    const supply = entry.supply ?? "?";
    const activity = entry.activity ?? "?";
    const volume = entry.trade_volume;
    const updated = entry.last_updated ?? "unknown";
    const purchaseText =
      purchase !== null && purchase !== undefined ? `buy ${purchase} cr` : "buy n/a";
    const sellText = sell !== null && sell !== undefined ? `sell ${sell} cr` : "sell n/a";
    const volumeText = volume !== null && volume !== undefined ? `volume ${volume}` : "volume n/a";
    return `• ${entry.good_symbol}: ${purchaseText} / ${sellText} | supply ${supply}, activity ${activity}, ${volumeText}, updated ${updated}`;
  }

  async run() {
    const transport = new StdioServerTransport();
    await this.server.connect(transport);
    console.error("SpaceTraders MCP Bot Server running on stdio");
  }
}

const token = process.env.SPACETRADERS_TOKEN;
const server = new SpaceTradersBotServer({ token });
server.run().catch((error) => {
  console.error("Failed to start SpaceTraders MCP Bot Server:", error);
});
