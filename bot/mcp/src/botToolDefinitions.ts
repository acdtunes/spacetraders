import { Tool } from "@modelcontextprotocol/sdk/types.js";

export const botToolDefinitions: Tool[] = [
  // ==================== PLAYER MANAGEMENT ====================
  {
    name: "player_register",
    description: "Register a new player/agent with the SpaceTraders bot. Stores the agent symbol and API token in the local database for future operations.",
    inputSchema: {
      type: "object",
      properties: {
        agent_symbol: {
          type: "string",
          description: "Agent symbol (e.g., CHROMESAMURAI, AGENT-1). Must be unique."
        },
        token: {
          type: "string",
          description: "SpaceTraders API token (JWT) for this agent"
        },
        metadata: {
          type: "string",
          description: "Optional JSON metadata object as a string (e.g., '{\"faction\": \"COSMIC\"}')"
        }
      },
      required: ["agent_symbol", "token"]
    }
  },
  {
    name: "player_list",
    description: "List all registered players/agents in the local database with their IDs and activity status.",
    inputSchema: {
      type: "object",
      properties: {}
    }
  },
  {
    name: "player_info",
    description: "Get detailed information about a specific player including agent symbol, creation date, last active time, and metadata.",
    inputSchema: {
      type: "object",
      properties: {
        player_id: {
          type: "integer",
          description: "Player ID to query (optional if agent_symbol provided)"
        },
        agent_symbol: {
          type: "string",
          description: "Agent symbol to query (optional if player_id provided)"
        }
      }
    }
  },

  // ==================== SHIP MANAGEMENT ====================
  {
    name: "ship_list",
    description: "List all ships for a player showing their current location, status, fuel, and cargo. Uses default player if not specified.",
    inputSchema: {
      type: "object",
      properties: {
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured or agent specified)"
        },
        agent: {
          type: "string",
          description: "Agent symbol (e.g., CHROMESAMURAI) - alternative to player_id"
        }
      }
    }
  },
  {
    name: "ship_info",
    description: "Get detailed information about a specific ship including full specs, navigation status, fuel levels, cargo contents, and current location.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol (e.g., CHROMESAMURAI-1)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship"]
    }
  },

  // ==================== NAVIGATION COMMANDS ====================
  {
    name: "navigate",
    description: "Navigate a ship to a destination waypoint. Automatically handles route planning, fuel management, refueling stops, and state transitions (dock/orbit). Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to navigate (e.g., CHROMESAMURAI-1)"
        },
        destination: {
          type: "string",
          description: "Destination waypoint symbol (e.g., X1-GZ7-B1). Must be in same system."
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship", "destination"]
    }
  },
  {
    name: "dock",
    description: "Dock a ship at its current location. Ship must be in orbit. Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to dock"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship"]
    }
  },
  {
    name: "orbit",
    description: "Put a ship into orbit from its current docked position. Ship must be docked. Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to put into orbit"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship"]
    }
  },
  {
    name: "refuel",
    description: "Refuel a ship at its current location. Ship must be docked at a waypoint with fuel. Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to refuel"
        },
        units: {
          type: "integer",
          description: "Optional: specific fuel units to purchase. Omit for full tank."
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship"]
    }
  },
  {
    name: "plan_route",
    description: "Plan a route to a destination without executing it. Shows route segments, fuel requirements, travel time estimates, and refueling stops needed.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to plan route for"
        },
        destination: {
          type: "string",
          description: "Destination waypoint symbol"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship", "destination"]
    }
  },

  // ==================== SHIPYARD COMMANDS ====================
  {
    name: "shipyard_list",
    description: "List available ships at a shipyard waypoint. Shows ship types, prices, and specifications.",
    inputSchema: {
      type: "object",
      properties: {
        waypoint: {
          type: "string",
          description: "Shipyard waypoint symbol (e.g., X1-HZ85-A2)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["waypoint"]
    }
  },
  {
    name: "shipyard_purchase",
    description: "Purchase a single ship from a shipyard. Auto-discovers nearest shipyard that sells the ship type if shipyard not specified. Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to use for purchasing (must be at or will navigate to shipyard)"
        },
        type: {
          type: "string",
          description: "Ship type to purchase (e.g., SHIP_PROBE, SHIP_MINING_DRONE, SHIP_LIGHT_SHUTTLE)"
        },
        shipyard: {
          type: "string",
          description: "Optional: Shipyard waypoint symbol (will auto-discover if not provided)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship", "type"]
    }
  },
  {
    name: "shipyard_batch_purchase",
    description: "Batch purchase multiple ships from a shipyard within budget constraints. Auto-discovers nearest shipyard if not specified. Purchases as many ships as possible within quantity and budget limits. Runs as background container.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to use for purchasing (must be at or will navigate to shipyard)"
        },
        type: {
          type: "string",
          description: "Ship type to purchase (e.g., SHIP_PROBE, SHIP_MINING_DRONE, SHIP_LIGHT_SHUTTLE)"
        },
        quantity: {
          type: "integer",
          description: "Maximum number of ships to purchase"
        },
        max_budget: {
          type: "integer",
          description: "Maximum total credits to spend on purchases"
        },
        shipyard: {
          type: "string",
          description: "Optional: Shipyard waypoint symbol (will auto-discover if not provided)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship", "type", "quantity", "max_budget"]
    }
  },

  // ==================== WAYPOINT QUERIES ====================
  {
    name: "waypoint_list",
    description: "List cached waypoints in a system. Query local database for waypoint information without making API calls. Supports filtering by trait (e.g., MARKETPLACE, SHIPYARD) or fuel availability.",
    inputSchema: {
      type: "object",
      properties: {
        system: {
          type: "string",
          description: "System symbol (e.g., X1-HZ85)"
        },
        trait: {
          type: "string",
          description: "Optional: Filter by trait (e.g., MARKETPLACE, SHIPYARD, ASTEROIDS)"
        },
        has_fuel: {
          type: "boolean",
          description: "Optional: Filter for waypoints with fuel stations"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["system"]
    }
  },

  // ==================== SCOUTING COMMANDS ====================
  {
    name: "scout_markets",
    description: "Scout markets with VRP-optimized fleet distribution. Partitions markets across multiple ships using vehicle routing optimization and creates background containers for each ship's tour.",
    inputSchema: {
      type: "object",
      properties: {
        ships: {
          type: "string",
          description: "Comma-separated list of ship symbols (e.g., SCOUT-1,SCOUT-2,SCOUT-3)"
        },
        system: {
          type: "string",
          description: "System symbol (e.g., X1-GZ7)"
        },
        markets: {
          type: "string",
          description: "Comma-separated list of market waypoints (e.g., X1-GZ7-A1,X1-GZ7-B2,X1-GZ7-C3)"
        },
        iterations: {
          type: "integer",
          description: "Number of complete tours to execute (default: 1, use -1 for infinite)"
        },
        return_to_start: {
          type: "boolean",
          description: "Return to starting waypoint after each tour (default: false)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ships", "system", "markets"]
    }
  },

  // ==================== CONTRACT OPERATIONS ====================
  {
    name: "contract_batch_workflow",
    description: "Execute batch contract workflow: For each iteration, negotiates new contract → evaluates profitability (polls market prices until profitable) → accepts contract → automatically purchases required goods from cheapest market → delivers cargo (handles multi-trip delivery if cargo capacity < required units) → fulfills contract. Returns summary with contracts fulfilled, total profit, and trip counts.",
    inputSchema: {
      type: "object",
      properties: {
        ship: {
          type: "string",
          description: "Ship symbol to use for contract operations (e.g., CHROMESAMURAI-1)"
        },
        count: {
          type: "integer",
          description: "Number of contracts to process (default: 1)"
        },
        player_id: {
          type: "integer",
          description: "Player ID (optional if default player configured)"
        },
        agent: {
          type: "string",
          description: "Agent symbol - alternative to player_id"
        }
      },
      required: ["ship"]
    }
  },

  // ==================== DAEMON OPERATIONS ====================
  {
    name: "daemon_list",
    description: "List all running background containers (daemons) showing their IDs, types, and status.",
    inputSchema: {
      type: "object",
      properties: {}
    }
  },
  {
    name: "daemon_inspect",
    description: "Inspect detailed information about a specific container including status, player ID, iteration count, restart count, and timestamps.",
    inputSchema: {
      type: "object",
      properties: {
        container_id: {
          type: "string",
          description: "Container ID to inspect (from daemon_list)"
        }
      },
      required: ["container_id"]
    }
  },
  {
    name: "daemon_stop",
    description: "Stop a running background container (daemon) gracefully. Container will complete current operation before stopping.",
    inputSchema: {
      type: "object",
      properties: {
        container_id: {
          type: "string",
          description: "Container ID to stop"
        }
      },
      required: ["container_id"]
    }
  },
  {
    name: "daemon_remove",
    description: "Remove a stopped container from the registry. Container must be stopped first.",
    inputSchema: {
      type: "object",
      properties: {
        container_id: {
          type: "string",
          description: "Container ID to remove"
        }
      },
      required: ["container_id"]
    }
  },
  {
    name: "daemon_logs",
    description: "Get logs from a container (running or stopped) from the database. Shows operation progress, errors, and events.",
    inputSchema: {
      type: "object",
      properties: {
        container_id: {
          type: "string",
          description: "Container ID to get logs from"
        },
        player_id: {
          type: "integer",
          description: "Player ID"
        },
        limit: {
          type: "integer",
          description: "Maximum number of log entries to retrieve (default: 100)"
        },
        level: {
          type: "string",
          description: "Filter by log level",
          enum: ["INFO", "WARNING", "ERROR", "DEBUG"]
        }
      },
      required: ["container_id", "player_id"]
    }
  },

  // ==================== CONFIGURATION ====================
  {
    name: "config_show",
    description: "Show current SpaceTraders CLI configuration including default player settings.",
    inputSchema: {
      type: "object",
      properties: {}
    }
  },
  {
    name: "config_set_player",
    description: "Set the default player for CLI operations. Allows running commands without specifying player_id or agent each time.",
    inputSchema: {
      type: "object",
      properties: {
        agent_symbol: {
          type: "string",
          description: "Agent symbol to set as default (must be registered)"
        }
      },
      required: ["agent_symbol"]
    }
  },
  {
    name: "config_clear_player",
    description: "Clear the default player setting. Future commands will require explicit player_id or agent specification.",
    inputSchema: {
      type: "object",
      properties: {}
    }
  }
] as const;
