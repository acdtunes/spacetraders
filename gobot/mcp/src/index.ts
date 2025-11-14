#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { botToolDefinitions } from "./botToolDefinitions.js";
import { DaemonClient } from "./daemonClient.js";

class SpaceTradersBotServer {
  private server: Server;
  private readonly tools = botToolDefinitions;
  private readonly daemonClient: DaemonClient;

  constructor() {
    this.daemonClient = new DaemonClient();
    this.server = new Server(
      {
        name: "spacetraders-mcp-gobot",
        version: "4.0.0",
      },
      {
        capabilities: {
          tools: {},
        },
      }
    );

    this.setupHandlers();
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
    try {
      let result: unknown;

      // Extract player_id from args (defaults to undefined if not provided)
      let playerId: number | undefined = args.player_id !== undefined ? Number(args.player_id) : undefined;

      switch (toolName) {
        // ==================== CONTAINER MANAGEMENT ====================
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

        // ==================== FLEET OPERATIONS ====================
        case "scout_markets":
          result = await this.daemonClient.scoutMarkets(
            String(args.ships).split(',').map(s => s.trim()),
            playerId!,
            String(args.system),
            String(args.markets).split(',').map(m => m.trim()),
            args.iterations !== undefined ? Number(args.iterations) : -1
          );
          break;

        // ==================== SHIP OPERATIONS ====================
        case "ship_list":
          result = await this.daemonClient.listShips(
            playerId,
            args.agent !== undefined ? String(args.agent) : undefined
          );
          break;

        case "ship_info":
          result = await this.daemonClient.getShip(
            String(args.ship),
            playerId,
            args.agent !== undefined ? String(args.agent) : undefined
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

        // ==================== SHIPYARD OPERATIONS ====================
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

        // ==================== WORKFLOW OPERATIONS ====================
        case "contract_batch_workflow":
          result = await this.daemonClient.batchContractWorkflow(
            String(args.ship),
            playerId,
            args.count !== undefined ? Number(args.count) : 1,
            args.agent !== undefined ? String(args.agent) : undefined
          );
          break;

        default:
          return {
            content: [{ type: "text", text: `Unknown tool: ${toolName}` }],
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

  async run() {
    const transport = new StdioServerTransport();
    await this.server.connect(transport);
    console.error("SpaceTraders MCP Go Bot Server v4.0 running on stdio");
  }
}

const server = new SpaceTradersBotServer();
server.run().catch((error) => {
  console.error("Failed to start SpaceTraders MCP Bot Server:", error);
  process.exit(1);
});
