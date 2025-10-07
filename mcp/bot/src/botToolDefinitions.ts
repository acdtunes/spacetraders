import { Tool } from "@modelcontextprotocol/sdk/types.js";

export const botToolDefinitions: Tool[] = [
  {
    "name": "bot_list_players",
    "description": "List every registered bot player from the shared multiplayer database.",
    "inputSchema": {
      "type": "object",
      "properties": {}
    }
  },
  {
    "name": "bot_register_player",
    "description": "Register or update a bot player token and return the assigned player_id.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent_symbol": {
          "type": "string",
          "description": "Agent callsign (e.g., CMDR_AC_2025)"
        },
        "token": {
          "type": "string",
          "description": "SpaceTraders API token (JWT)"
        }
      },
      "required": [
        "agent_symbol",
        "token"
      ]
    }
  },
  {
    "name": "bot_get_player_info",
    "description": "Show stored token, registration time, and last activity for one player.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional if agent_symbol provided)"
        },
        "agent_symbol": {
          "type": "string",
          "description": "Agent symbol (optional if player_id provided)"
        }
      }
    }
  },
  {
    "name": "bot_fleet_status",
    "description": "Fetch a one-shot fleet status snapshot for the given player, including ships, cargo, and credits.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ships": {
          "type": "string",
          "description": "Comma-separated ship symbols to check (e.g., 'SHIP-1,SHIP-2'). Omit to check all ships in fleet."
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_fleet_monitor",
    "description": "Monitor selected ships on a timer and stream status snapshots for the duration of the run.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ships": {
          "type": "string",
          "description": "Comma-separated ship symbols to monitor continuously"
        },
        "interval": {
          "type": "integer",
          "description": "Minutes between each status check (default: 5). Shorter = more frequent updates."
        },
        "duration": {
          "type": "integer",
          "description": "Total number of status checks to perform (default: 12). Example: interval=5, duration=12 = 60 minutes total."
        }
      },
      "required": [
        "player_id",
        "ships"
      ]
    }
  },
  {
    "name": "bot_run_mining",
    "description": "Start the autonomous mining workflow for a ship between an asteroid and market.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol with mining mount (e.g., SHIP-3). Check ship role/capabilities first."
        },
        "asteroid": {
          "type": "string",
          "description": "Asteroid waypoint to mine at (e.g., X1-HU87-B9). Prefer asteroids with COMMON_METAL_DEPOSITS or PRECIOUS_METAL_DEPOSITS traits."
        },
        "market": {
          "type": "string",
          "description": "Market waypoint to sell mined resources (e.g., X1-HU87-B7). Must be EXCHANGE or marketplace that buys raw materials."
        },
        "cycles": {
          "type": "integer",
          "description": "Number of complete mine-sell-refuel cycles (default: 30). Each cycle takes 10-15 minutes depending on cargo capacity and yields."
        }
      },
      "required": [
        "player_id",
        "ship",
        "asteroid",
        "market"
      ]
    }
  },
  {
    "name": "bot_run_trading",
    "description": "Run the trading automation loop for a ship with the supplied route and targets.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Trading ship with cargo capacity \u226540 recommended (e.g., SHIP-1, light hauler)."
        },
        "good": {
          "type": "string",
          "description": "Trade good symbol matching market exports/imports (e.g., IRON_ORE, SHIP_PARTS, ADVANCED_CIRCUITRY). Verify markets actually trade this good."
        },
        "buy_from": {
          "type": "string",
          "description": "Market waypoint that SELLS (exports) the good at low price (e.g., X1-HU87-D42)."
        },
        "sell_to": {
          "type": "string",
          "description": "Market waypoint that BUYS (imports) the good at high price (e.g., X1-HU87-A2)."
        },
        "duration": {
          "type": "number",
          "description": "How long to trade in hours (default: 1.0). Can use decimals (e.g., 0.5 = 30 minutes, 4.0 = 4 hours)."
        },
        "min_profit": {
          "type": "integer",
          "description": "Minimum profit per trip in credits (default: 5000). Stops if 3 trips fall below this. Use 150000+ for high-value routes."
        }
      },
      "required": [
        "player_id",
        "ship",
        "good",
        "buy_from",
        "sell_to"
      ]
    }
  },
  {
    "name": "bot_wait_minutes",
    "description": "Pause execution for the specified number of minutes so the Flag Captain can idle between status checks.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "minutes": {
          "type": "number",
          "description": "Number of minutes to wait (e.g., 5)."
        },
        "reason": {
          "type": "string",
          "description": "Optional note explaining why the wait is needed."
        }
      },
      "required": [
        "minutes"
      ]
    }
  },
  {
    "name": "bot_purchase_ship",
    "description": "Purchase one or more ships from a shipyard using a designated hauler.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol that will travel to the shipyard and perform the purchase"
        },
        "shipyard": {
          "type": "string",
          "description": "Shipyard waypoint symbol (e.g., X1-HU87-A1)"
        },
        "ship_type": {
          "type": "string",
          "description": "Ship type to purchase (e.g., SHIP_EXPLORER)"
        },
        "quantity": {
          "type": "integer",
          "description": "Number of ships to purchase (default: 1)"
        },
        "max_budget": {
          "type": "integer",
          "description": "Maximum total credits to spend"
        }
      },
      "required": [
        "player_id",
        "ship",
        "shipyard",
        "ship_type",
        "max_budget"
      ]
    }
  },
  {
    "name": "bot_trade_plan",
    "description": "Analyze current market intel and propose a multi-leg trading route for a ship without executing it.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol"
        },
        "system": {
          "type": "string",
          "description": "Optional: system to evaluate (defaults to ship's current system)"
        },
        "max_stops": {
          "type": "integer",
          "description": "Maximum number of route stops to consider (default: 4)"
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_multileg_trade",
    "description": "Run autonomous multi-leg trading optimizer to find and execute the most profitable trade route. Supports looping with --cycles or --duration.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol"
        },
        "system": {
          "type": "string",
          "description": "System symbol (optional, defaults to ship's current system)"
        },
        "max_stops": {
          "type": "integer",
          "description": "Maximum stops for route optimization (default: 4)"
        },
        "cycles": {
          "type": "integer",
          "description": "Number of cycles to repeat (-1 for infinite). Mutually exclusive with duration."
        },
        "duration": {
          "type": "number",
          "description": "Run for N hours. Mutually exclusive with cycles."
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_market_waypoint",
    "description": "Display cached market intel for a waypoint or a single trade good.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "waypoint_symbol": {
          "type": "string",
          "description": "Waypoint to inspect (e.g., X1-HU87-B7)"
        },
        "good_symbol": {
          "type": "string",
          "description": "Optional: specific trade good to filter to (e.g., IRON_ORE)"
        }
      },
      "required": [
        "waypoint_symbol"
      ]
    }
  },
  {
    "name": "bot_market_find_sellers",
    "description": "List the best recorded sellers for a trade good, filtered by supply and freshness.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "good_symbol": {
          "type": "string",
          "description": "Trade good to search for (e.g., COPPER, IRON_ORE)"
        },
        "system": {
          "type": "string",
          "description": "Optional: limit to waypoints inside a system (prefix match, e.g., X1-HU87)"
        },
        "min_supply": {
          "type": "string",
          "description": "Optional: minimum supply level (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)"
        },
        "updated_within_hours": {
          "type": "number",
          "description": "Optional: only results updated within this many hours"
        },
        "limit": {
          "type": "integer",
          "description": "Optional: maximum number of results to return (default 10)"
        }
      },
      "required": [
        "good_symbol"
      ]
    }
  },
  {
    "name": "bot_market_find_buyers",
    "description": "List the best recorded buyers for a trade good, filtered by activity and freshness.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "good_symbol": {
          "type": "string",
          "description": "Trade good to search for (e.g., COPPER, IRON_ORE)"
        },
        "system": {
          "type": "string",
          "description": "Optional: limit to waypoints inside a system (prefix match, e.g., X1-HU87)"
        },
        "min_activity": {
          "type": "string",
          "description": "Optional: minimum market activity (WEAK, FAIR, STRONG, EXCESSIVE)"
        },
        "updated_within_hours": {
          "type": "number",
          "description": "Optional: only results updated within this many hours"
        },
        "limit": {
          "type": "integer",
          "description": "Optional: maximum number of results to return (default 10)"
        }
      },
      "required": [
        "good_symbol"
      ]
    }
  },
  {
    "name": "bot_market_recent_updates",
    "description": "Show the most recent market cache entries that were recorded.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "system": {
          "type": "string",
          "description": "Optional: limit to waypoints inside a system (prefix match)"
        },
        "limit": {
          "type": "integer",
          "description": "Optional: maximum number of rows to return (default 25)"
        }
      }
    }
  },
  {
    "name": "bot_market_find_stale",
    "description": "Find cached market rows older than the provided age threshold.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "max_age_hours": {
          "type": "number",
          "description": "Age threshold in hours. Entries older than this are returned."
        },
        "system": {
          "type": "string",
          "description": "Optional: limit to waypoints inside a system (prefix match)"
        }
      },
      "required": [
        "max_age_hours"
      ]
    }
  },
  {
    "name": "bot_market_summarize_good",
    "description": "Summarize price statistics and counts for a trade good across the cache.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "good_symbol": {
          "type": "string",
          "description": "Trade good to summarize (e.g., ALUMINUM_ORE)"
        },
        "system": {
          "type": "string",
          "description": "Optional: limit summary to a system (prefix match)"
        }
      },
      "required": [
        "good_symbol"
      ]
    }
  },
  {
    "name": "bot_negotiate_contract",
    "description": "Negotiate a new contract for the player using the specified ship.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Any ship in your fleet (negotiation doesn't require specific ship type). Ship's location doesn't matter."
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_fulfill_contract",
    "description": "Fulfil an accepted contract, including buying or mining cargo as required.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship to execute contract fulfillment. Needs cargo capacity \u2265 contract units (or will multi-trip if >40)."
        },
        "contract_id": {
          "type": "string",
          "description": "Contract ID from negotiate response. Must be an ACCEPTED contract."
        },
        "buy_from": {
          "type": "string",
          "description": "Optional: Specific market waypoint to purchase resources from (e.g., X1-HU87-B7). If omitted, finds nearest market automatically."
        }
      },
      "required": [
        "player_id",
        "ship",
        "contract_id"
      ]
    }
  },
  {
    "name": "bot_build_graph",
    "description": "Build or refresh the navigation graph for a system and store it in the database.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "system": {
          "type": "string",
          "description": "System symbol to build graph for (e.g., X1-HU87, X1-MM38). Must be within your operational range."
        }
      },
      "required": [
        "player_id",
        "system"
      ]
    }
  },
  {
    "name": "bot_plan_route",
    "description": "Plan a navigation route inside a system using stored graph data.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to plan route for. Ship's fuel capacity affects route (larger tanks = fewer refuel stops)."
        },
        "system": {
          "type": "string",
          "description": "System symbol containing both waypoints. Must have pre-built graph (use build_graph first)."
        },
        "start": {
          "type": "string",
          "description": "Starting waypoint where ship currently is (e.g., X1-HU87-A1)"
        },
        "goal": {
          "type": "string",
          "description": "Destination waypoint (e.g., X1-HU87-B9)"
        }
      },
      "required": [
        "player_id",
        "ship",
        "system",
        "start",
        "goal"
      ]
    }
  },
  {
    "name": "bot_navigate",
    "description": "Navigate a ship to a destination using SmartNavigator with automatic fuel management and refuel stops.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to navigate (e.g., VEILSTORM-1)"
        },
        "destination": {
          "type": "string",
          "description": "Destination waypoint (e.g., X1-NF92-B9). Must be in same system as ship's current location."
        }
      },
      "required": [
        "player_id",
        "ship",
        "destination"
      ]
    }
  },
  {
    "name": "bot_daemon_start",
    "description": "Launch a background daemon for a bot operation and record it.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "operation": {
          "type": "string",
          "description": "Operation type: 'mine', 'trade', or 'contract'"
        },
        "daemon_id": {
          "type": "string",
          "description": "Unique daemon identifier (e.g., 'miner-ship3', 'trader-1'). Auto-generated if omitted. Use descriptive names for easy management."
        },
        "args": {
          "type": "array",
          "items": {
            "type": "string"
          },
          "description": "Complete CLI argument list for the operation. Example (mine): ['--player-id', '42', '--ship', 'SHIP-3', '--asteroid', 'X1-HU87-B9', '--market', 'X1-HU87-B7', '--cycles', '50']"
        }
      },
      "required": [
        "player_id",
        "operation",
        "args"
      ]
    }
  },
  {
    "name": "bot_daemon_stop",
    "description": "Gracefully stop a running bot daemon by daemon id.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "daemon_id": {
          "type": "string",
          "description": "Daemon ID to stop (from daemon_status or daemon_start response). Must be exact match."
        }
      },
      "required": [
        "player_id",
        "daemon_id"
      ]
    }
  },
  {
    "name": "bot_daemon_status",
    "description": "Report the status of all daemons or a specific daemon for a player.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "daemon_id": {
          "type": "string",
          "description": "Optional: specific daemon to check. Omit to list ALL daemons (recommended for fleet overview and health monitoring)."
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_daemon_logs",
    "description": "Tail recent log output for a daemon to inspect progress or errors.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "daemon_id": {
          "type": "string",
          "description": "Daemon ID whose logs to retrieve. Works for both running and stopped daemons."
        },
        "lines": {
          "type": "integer",
          "description": "Number of most recent log lines to show (default: 20). Use 50-100 for detailed troubleshooting, 10-20 for quick checks."
        }
      },
      "required": [
        "player_id",
        "daemon_id"
      ]
    }
  },
  {
    "name": "bot_daemon_cleanup",
    "description": "Scan the daemon registry for stopped processes and clean up their records after crashes or manual kills.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_assignments_list",
    "description": "List ship assignments and their statuses for a player.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "include_stale": {
          "type": "boolean",
          "description": "Include stale assignments where daemon stopped but ship not released (default: false). Use true for troubleshooting."
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_assignments_assign",
    "description": "Assign a ship to an operator/daemon and mark it active.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to assign (e.g., SHIP-3, CMDR_AC_2025-1). Must not be currently assigned."
        },
        "operator": {
          "type": "string",
          "description": "Operator name controlling this ship (e.g., 'trading_operator', 'mining_operator', 'contract_operator'). Use consistent naming."
        },
        "daemon_id": {
          "type": "string",
          "description": "Associated daemon ID from daemon_start. This links registry to running process."
        },
        "operation": {
          "type": "string",
          "description": "Operation type: 'trade', 'mine', 'contract'. Matches daemon operation."
        },
        "duration": {
          "type": "number",
          "description": "Optional: Expected operation duration in hours. Helps with planning and conflict detection."
        }
      },
      "required": [
        "player_id",
        "ship",
        "operator",
        "daemon_id",
        "operation"
      ]
    }
  },
  {
    "name": "bot_assignments_release",
    "description": "Release a ship from its assignment and mark it idle.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to release from assignment (e.g., SHIP-3). Must be currently assigned."
        },
        "reason": {
          "type": "string",
          "description": "Optional: Reason for release (e.g., 'operation_complete', 'manual_stop', 'reassignment'). For logging/debugging."
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_assignments_available",
    "description": "Check whether a ship is idle in the assignment registry and report the operator/daemon if it is busy.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol"
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_assignments_status",
    "description": "Render the full assignment card for one ship: status badge, operator, daemon linkage, timestamps, and live daemon metrics if running.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol"
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_assignments_find",
    "description": "Search for ships that satisfy minimum cargo or fuel thresholds.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "cargo_min": {
          "type": "integer",
          "description": "Optional: Minimum cargo capacity required. Use 40 for trading (standard profitable capacity). Omit for any capacity."
        },
        "fuel_min": {
          "type": "integer",
          "description": "Optional: Minimum fuel capacity required. Use 400+ for long-range operations. Omit for any fuel capacity."
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_assignments_reassign",
    "description": "Bulk release ships from an operation, stopping their daemons first unless `no_stop` is set, then mark them idle for reuse.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        },
        "ships": {
          "type": "string",
          "description": "Comma-separated ship symbols"
        },
        "from_operation": {
          "type": "string",
          "description": "Current operation label"
        },
        "no_stop": {
          "type": "boolean",
          "description": "Skip stopping associated daemons"
        },
        "timeout": {
          "type": "integer",
          "description": "Seconds to wait for daemon shutdown"
        }
      },
      "required": [
        "player_id",
        "ships",
        "from_operation"
      ]
    }
  },
  {
    "name": "bot_assignments_init",
    "description": "Bootstrap the assignment registry by fetching the fleet from the SpaceTraders API and seeding every ship as idle.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID"
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_assignments_sync",
    "description": "Resync assignment records by reconciling daemon state.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        }
      },
      "required": [
        "player_id"
      ]
    }
  },
  {
    "name": "bot_find_fuel",
    "description": "Recommend nearby waypoints where a ship can refuel.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to find fuel for. Tool uses ship's current location to calculate distances."
        }
      },
      "required": [
        "player_id",
        "ship"
      ]
    }
  },
  {
    "name": "bot_calculate_distance",
    "description": "Calculate the distance between two waypoints in a system.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "waypoint1": {
          "type": "string",
          "description": "First waypoint symbol (e.g., X1-HU87-A1)"
        },
        "waypoint2": {
          "type": "string",
          "description": "Second waypoint symbol (e.g., X1-HU87-B9)"
        }
      },
      "required": [
        "player_id",
        "waypoint1",
        "waypoint2"
      ]
    }
  },
  {
    "name": "bot_find_mining_opportunities",
    "description": "Automated mining route optimizer: Scans all asteroids in a system, queries market database for best prices, calculates profit/hour for each asteroid-market pair accounting for ship speed, fuel capacity, cargo capacity, distance, and cycle time. Returns top 10 opportunities ranked by profit/hour. CRITICAL: Always provide ship parameter for accurate calculations based on actual ship specs (speed, fuel, cargo). Without ship parameter, uses default mining drone specs (speed 9, fuel 80, cargo 15).",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "system": {
          "type": "string",
          "description": "System symbol to analyze (e.g., X1-JD30). Scans all asteroids and finds optimal asteroid-market pairs with profit/hour calculations."
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol to optimize for (e.g., IRONKEEP-6). HIGHLY RECOMMENDED: Provides accurate calculations based on actual ship specs (speed, fuel capacity, cargo capacity). Different ships have vastly different optimal routes."
        }
      },
      "required": [
        "player_id",
        "system"
      ]
    }
  },
  {
    "name": "bot_captain_log_init",
    "description": "Initialize captain's log storage for an agent and optional player.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign (e.g., CMDR_AC_2025)"
        },
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional, for fetching agent data)"
        }
      },
      "required": [
        "agent"
      ]
    }
  },
  {
    "name": "bot_captain_log_session_start",
    "description": "Start a captain's log session with objective and operator context.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign"
        },
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional)"
        },
        "objective": {
          "type": "string",
          "description": "Mission objective description"
        },
        "operator": {
          "type": "string",
          "description": "Operator name (default: AI First Mate)"
        }
      },
      "required": [
        "agent",
        "objective"
      ]
    }
  },
  {
    "name": "bot_captain_log_session_end",
    "description": "Close the current captain's log session and persist results.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign"
        },
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional)"
        }
      },
      "required": [
        "agent"
      ]
    }
  },
  {
    "name": "bot_captain_log_entry",
    "description": "Append a structured entry to the captain's log with narrative prose. IMPORTANT: All specialist agents MUST include narrative parameter describing what they did in story-like first-person format, explaining WHY decisions were made and what was accomplished.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign"
        },
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional)"
        },
        "entry_type": {
          "type": "string",
          "description": "Entry type",
          "enum": [
            "OPERATION_STARTED",
            "OPERATION_COMPLETED",
            "CRITICAL_ERROR",
            "PERFORMANCE_SUMMARY"
          ]
        },
        "operator": {
          "type": "string",
          "description": "Operator/specialist name (e.g., 'Mining Operator', 'Trading Operator')"
        },
        "ship": {
          "type": "string",
          "description": "Ship symbol"
        },
        "daemon_id": {
          "type": "string",
          "description": "Daemon ID (for OPERATION_STARTED)"
        },
        "op_type": {
          "type": "string",
          "description": "Operation type (for OPERATION_STARTED)"
        },
        "narrative": {
          "type": "string",
          "description": "REQUIRED: First-person narrative prose describing what was done and why. For OPERATION_STARTED: explain strategy and reasoning. For OPERATION_COMPLETED: tell the story of execution, challenges faced, decisions made. For CRITICAL_ERROR: describe incident and response. Use story-like language with emotional tone (pride, concern, determination)."
        },
        "insights": {
          "type": "string",
          "description": "Strategic insights learned (for OPERATION_COMPLETED). What worked well, what didn't, lessons learned."
        },
        "recommendations": {
          "type": "string",
          "description": "Forward-looking recommendations (for OPERATION_COMPLETED). What to do next, optimizations to try."
        },
        "error": {
          "type": "string",
          "description": "Error description (for CRITICAL_ERROR)"
        },
        "resolution": {
          "type": "string",
          "description": "Resolution applied (for CRITICAL_ERROR)"
        }
      },
      "required": [
        "agent",
        "entry_type"
      ]
    }
  },
  {
    "name": "bot_captain_log_search",
    "description": "Search captain's log entries by tag or timeframe.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign"
        },
        "tag": {
          "type": "string",
          "description": "Tag to search for (e.g., 'mining', 'error')"
        },
        "timeframe": {
          "type": "integer",
          "description": "Hours to look back"
        }
      },
      "required": [
        "agent"
      ]
    }
  },
  {
    "name": "bot_captain_log_report",
    "description": "Generate a summary report of captain's log activity for a duration.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "agent": {
          "type": "string",
          "description": "Agent callsign"
        },
        "player_id": {
          "type": "integer",
          "description": "Player ID (optional)"
        },
        "duration": {
          "type": "integer",
          "description": "Hours to summarize (default: 24)"
        }
      },
      "required": [
        "agent"
      ]
    }
  },
  {
    "name": "bot_scout_coordinator_start",
    "description": "Start multi-ship continuous market scouting operation as a background daemon. Coordinates multiple ships to efficiently scout markets in a system.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "player_id": {
          "type": "integer",
          "description": "Player ID from database"
        },
        "system": {
          "type": "string",
          "description": "System symbol to scout (e.g., X1-HU87)"
        },
        "ships": {
          "type": "string",
          "description": "Comma-separated ship symbols to use for scouting (e.g., 'SHIP-1,SHIP-2,SHIP-3')"
        },
        "algorithm": {
          "type": "string",
          "description": "Optimization algorithm for route planning (default: '2opt'). Options: 'greedy', '2opt'"
        }
      },
      "required": [
        "player_id",
        "system",
        "ships"
      ]
    }
  },
  {
    "name": "bot_scout_coordinator_stop",
    "description": "Stop the multi-ship scouting coordinator and all associated scout ships for a system.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "system": {
          "type": "string",
          "description": "System symbol of the coordinator to stop (e.g., X1-HU87)"
        }
      },
      "required": [
        "system"
      ]
    }
  },
  {
    "name": "bot_scout_coordinator_status",
    "description": "Show the status of the scout coordinator for a system, including all active scouts.",
    "inputSchema": {
      "type": "object",
      "properties": {
        "system": {
          "type": "string",
          "description": "System symbol to check status for (e.g., X1-HU87)"
        }
      },
      "required": [
        "system"
      ]
    }
  }
] as const;
