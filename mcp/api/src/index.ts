#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  Tool,
} from "@modelcontextprotocol/sdk/types.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { SpaceTradersClient } from "./client.js";

const API_BASE_URL = "https://api.spacetraders.io/v2";

interface SpaceTradersConfig {
  token?: string;
}

type ToolHandler = (args: Record<string, unknown>) => Promise<CallToolResult>;

const apiToolDefinitions: Tool[] = [
  {
    name: "register_agent",
    description: "Register a new agent and get an authentication token",
    inputSchema: {
      type: "object",
      properties: {
        symbol: {
          type: "string",
          description: "Agent call sign (3-14 characters)",
        },
        faction: {
          type: "string",
          description: "Starting faction symbol (e.g., COSMIC, VOID, etc.)",
        },
      },
      required: ["symbol", "faction"],
    },
  },
  {
    name: "get_agent",
    description: "Get your agent details",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
      },
    },
  },
  {
    name: "list_systems",
    description: "List all systems in the universe",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        page: {
          type: "number",
          description: "Page number (default: 1)",
        },
        limit: {
          type: "number",
          description: "Results per page (default: 20, max: 20)",
        },
      },
    },
  },
  {
    name: "get_system",
    description: "Get details about a specific system",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        systemSymbol: {
          type: "string",
          description: "System symbol (e.g., X1-DF55)",
        },
      },
      required: ["systemSymbol"],
    },
  },
  {
    name: "list_waypoints",
    description: "List all waypoints in a system",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        systemSymbol: {
          type: "string",
          description: "System symbol",
        },
        page: {
          type: "number",
          description: "Page number",
        },
        limit: {
          type: "number",
          description: "Results per page",
        },
      },
      required: ["systemSymbol"],
    },
  },
  {
    name: "get_waypoint",
    description: "Get details about a specific waypoint",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        systemSymbol: {
          type: "string",
          description: "System symbol",
        },
        waypointSymbol: {
          type: "string",
          description: "Waypoint symbol",
        },
      },
      required: ["systemSymbol", "waypointSymbol"],
    },
  },
  {
    name: "get_market",
    description: "Get market data for a waypoint",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        systemSymbol: {
          type: "string",
          description: "System symbol",
        },
        waypointSymbol: {
          type: "string",
          description: "Waypoint symbol",
        },
      },
      required: ["systemSymbol", "waypointSymbol"],
    },
  },
  {
    name: "get_shipyard",
    description: "Get shipyard data for a waypoint",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        systemSymbol: {
          type: "string",
          description: "System symbol",
        },
        waypointSymbol: {
          type: "string",
          description: "Waypoint symbol",
        },
      },
      required: ["systemSymbol", "waypointSymbol"],
    },
  },
  {
    name: "list_factions",
    description: "List all factions",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        page: {
          type: "number",
          description: "Page number",
        },
        limit: {
          type: "number",
          description: "Results per page",
        },
      },
    },
  },
  {
    name: "get_faction",
    description: "Get details about a specific faction",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        factionSymbol: {
          type: "string",
          description: "Faction symbol",
        },
      },
      required: ["factionSymbol"],
    },
  },
  {
    name: "list_contracts",
    description: "List your contracts",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        page: {
          type: "number",
          description: "Page number",
        },
        limit: {
          type: "number",
          description: "Results per page",
        },
      },
    },
  },
  {
    name: "get_contract",
    description: "Get details about a specific contract",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        contractId: {
          type: "string",
          description: "Contract ID",
        },
      },
      required: ["contractId"],
    },
  },
  {
    name: "accept_contract",
    description: "Accept a contract",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        contractId: {
          type: "string",
          description: "Contract ID",
        },
      },
      required: ["contractId"],
    },
  },
  {
    name: "list_ships",
    description: "List your ships",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        page: {
          type: "number",
          description: "Page number",
        },
        limit: {
          type: "number",
          description: "Results per page",
        },
      },
    },
  },
  {
    name: "get_ship",
    description: "Get details about a specific ship",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "navigate_ship",
    description: "Navigate a ship to a waypoint",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
        waypointSymbol: {
          type: "string",
          description: "Destination waypoint symbol",
        },
      },
      required: ["shipSymbol", "waypointSymbol"],
    },
  },
  {
    name: "dock_ship",
    description: "Dock a ship at its current location",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "orbit_ship",
    description: "Put a ship into orbit",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "refuel_ship",
    description: "Refuel a ship",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
        units: {
          type: "number",
          description: "Amount of fuel to purchase (optional, defaults to full tank)",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "extract_resources",
    description: "Extract resources with a ship",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "sell_cargo",
    description: "Sell cargo from a ship",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
        symbol: {
          type: "string",
          description: "Trade good symbol",
        },
        units: {
          type: "number",
          description: "Number of units to sell",
        },
      },
      required: ["shipSymbol", "symbol", "units"],
    },
  },
  {
    name: "purchase_cargo",
    description: "Purchase cargo for a ship",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
        symbol: {
          type: "string",
          description: "Trade good symbol",
        },
        units: {
          type: "number",
          description: "Number of units to purchase",
        },
      },
      required: ["shipSymbol", "symbol", "units"],
    },
  },
  {
    name: "scan_systems",
    description: "Scan for nearby systems",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "scan_waypoints",
    description: "Scan for nearby waypoints",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
  {
    name: "scan_ships",
    description: "Scan for nearby ships",
    inputSchema: {
      type: "object",
      properties: {
        agentToken: {
          type: "string",
          description:
            "Agent authentication token (optional, uses account token if not provided)",
        },
        shipSymbol: {
          type: "string",
          description: "Ship symbol",
        },
      },
      required: ["shipSymbol"],
    },
  },
];

class SpaceTradersApiServer {
  private server: Server;
  private client: SpaceTradersClient;
  private readonly tools = apiToolDefinitions;
  private readonly toolHandlers: Map<string, ToolHandler> = new Map();

  constructor(config: SpaceTradersConfig = {}) {
    this.server = new Server(
      {
        name: "spacetraders-mcp-api",
        version: "2.0.0",
      },
      {
        capabilities: {
          tools: {},
        },
      }
    );

    this.client = new SpaceTradersClient(API_BASE_URL, config.token);
    this.registerHandlers();
    this.setupHandlers();
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
    this.toolHandlers.set("register_agent", (args) => this.registerAgent(args));
    this.toolHandlers.set("get_agent", (args) => this.getAgent(args));
    this.toolHandlers.set("list_systems", (args) => this.listSystems(args));
    this.toolHandlers.set("get_system", (args) => this.getSystem(args));
    this.toolHandlers.set("list_waypoints", (args) => this.listWaypoints(args));
    this.toolHandlers.set("get_waypoint", (args) => this.getWaypoint(args));
    this.toolHandlers.set("get_market", (args) => this.getMarket(args));
    this.toolHandlers.set("get_shipyard", (args) => this.getShipyard(args));
    this.toolHandlers.set("list_factions", (args) => this.listFactions(args));
    this.toolHandlers.set("get_faction", (args) => this.getFaction(args));
    this.toolHandlers.set("list_contracts", (args) => this.listContracts(args));
    this.toolHandlers.set("get_contract", (args) => this.getContract(args));
    this.toolHandlers.set("accept_contract", (args) => this.acceptContract(args));
    this.toolHandlers.set("list_ships", (args) => this.listShips(args));
    this.toolHandlers.set("get_ship", (args) => this.getShip(args));
    this.toolHandlers.set("navigate_ship", (args) => this.navigateShip(args));
    this.toolHandlers.set("dock_ship", (args) => this.dockShip(args));
    this.toolHandlers.set("orbit_ship", (args) => this.orbitShip(args));
    this.toolHandlers.set("refuel_ship", (args) => this.refuelShip(args));
    this.toolHandlers.set("extract_resources", (args) => this.extractResources(args));
    this.toolHandlers.set("sell_cargo", (args) => this.sellCargo(args));
    this.toolHandlers.set("purchase_cargo", (args) => this.purchaseCargo(args));
    this.toolHandlers.set("scan_systems", (args) => this.scanSystems(args));
    this.toolHandlers.set("scan_waypoints", (args) => this.scanWaypoints(args));
    this.toolHandlers.set("scan_ships", (args) => this.scanShips(args));
  }

  private async registerAgent(args: any): Promise<CallToolResult> {
    const data = await this.client.post("/register", {
      symbol: args.symbol,
      faction: args.faction,
    });
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getAgent(args: any = {}): Promise<CallToolResult> {
    const data = await this.client.get("/my/agent", args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async listSystems(args: any): Promise<CallToolResult> {
    const params = new URLSearchParams();
    if (args.page) params.append("page", String(args.page));
    if (args.limit) params.append("limit", String(args.limit));
    const query = params.toString() ? `?${params.toString()}` : "";
    const data = await this.client.get(`/systems${query}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getSystem(args: any): Promise<CallToolResult> {
    const data = await this.client.get(`/systems/${args.systemSymbol}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async listWaypoints(args: any): Promise<CallToolResult> {
    const params = new URLSearchParams();
    if (args.page) params.append("page", String(args.page));
    if (args.limit) params.append("limit", String(args.limit));
    const query = params.toString() ? `?${params.toString()}` : "";
    const data = await this.client.get(
      `/systems/${args.systemSymbol}/waypoints${query}`,
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getWaypoint(args: any): Promise<CallToolResult> {
    const data = await this.client.get(
      `/systems/${args.systemSymbol}/waypoints/${args.waypointSymbol}`,
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getMarket(args: any): Promise<CallToolResult> {
    const data = await this.client.get(
      `/systems/${args.systemSymbol}/waypoints/${args.waypointSymbol}/market`,
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getShipyard(args: any): Promise<CallToolResult> {
    const data = await this.client.get(
      `/systems/${args.systemSymbol}/waypoints/${args.waypointSymbol}/shipyard`,
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async listFactions(args: any): Promise<CallToolResult> {
    const params = new URLSearchParams();
    if (args.page) params.append("page", String(args.page));
    if (args.limit) params.append("limit", String(args.limit));
    const query = params.toString() ? `?${params.toString()}` : "";
    const data = await this.client.get(`/factions${query}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getFaction(args: any): Promise<CallToolResult> {
    const data = await this.client.get(`/factions/${args.factionSymbol}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async listContracts(args: any): Promise<CallToolResult> {
    const params = new URLSearchParams();
    if (args.page) params.append("page", String(args.page));
    if (args.limit) params.append("limit", String(args.limit));
    const query = params.toString() ? `?${params.toString()}` : "";
    const data = await this.client.get(`/my/contracts${query}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getContract(args: any): Promise<CallToolResult> {
    const data = await this.client.get(`/my/contracts/${args.contractId}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async acceptContract(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/contracts/${args.contractId}/accept`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async listShips(args: any): Promise<CallToolResult> {
    const params = new URLSearchParams();
    if (args.page) params.append("page", String(args.page));
    if (args.limit) params.append("limit", String(args.limit));
    const query = params.toString() ? `?${params.toString()}` : "";
    const data = await this.client.get(`/my/ships${query}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async getShip(args: any): Promise<CallToolResult> {
    const data = await this.client.get(`/my/ships/${args.shipSymbol}`, args.agentToken);
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async navigateShip(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/navigate`,
      {
        waypointSymbol: args.waypointSymbol,
      },
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async dockShip(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/dock`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async orbitShip(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/orbit`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async refuelShip(args: any): Promise<CallToolResult> {
    const body: any = {};
    if (args.units) body.units = args.units;
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/refuel`,
      body,
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async extractResources(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/extract`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async sellCargo(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/sell`,
      {
        symbol: args.symbol,
        units: args.units,
      },
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async purchaseCargo(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/purchase`,
      {
        symbol: args.symbol,
        units: args.units,
      },
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async scanSystems(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/scan/systems`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async scanWaypoints(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/scan/waypoints`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  private async scanShips(args: any): Promise<CallToolResult> {
    const data = await this.client.post(
      `/my/ships/${args.shipSymbol}/scan/ships`,
      {},
      args.agentToken
    );
    return {
      content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
    };
  }

  async run() {
    const transport = new StdioServerTransport();
    await this.server.connect(transport);
    console.error("SpaceTraders MCP API Server running on stdio");
  }
}

const token = process.env.SPACETRADERS_TOKEN;
const server = new SpaceTradersApiServer({ token });
server.run().catch((error) => {
  console.error("Failed to start SpaceTraders MCP API Server:", error);
});
