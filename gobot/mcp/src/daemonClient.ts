/**
 * Node.js gRPC client for SpaceTraders daemon
 * Communicates with daemon via gRPC over Unix socket
 */

import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const SOCKET_PATH = process.env.SPACETRADERS_DAEMON_SOCKET
  ? process.env.SPACETRADERS_DAEMON_SOCKET
  : "unix:///tmp/spacetraders-daemon.sock";

// Proto file path (relative to this file)
const PROTO_PATH = path.join(__dirname, "../../pkg/proto/daemon/daemon.proto");

// gRPC options
const packageDefinition = protoLoader.loadSync(PROTO_PATH, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});

const protoDescriptor = grpc.loadPackageDefinition(packageDefinition) as any;
const daemonProto = protoDescriptor.daemon;

export class DaemonClient {
  private client: any;

  constructor() {
    // Create gRPC client connected to Unix socket
    this.client = new daemonProto.DaemonService(
      SOCKET_PATH,
      grpc.credentials.createInsecure()
    );
  }

  /**
   * List containers
   */
  async listContainers(playerId?: number): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {};
      if (playerId !== undefined) {
        request.player_id = playerId;
      }

      this.client.ListContainers(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Get container details
   */
  async inspectContainer(containerId: string): Promise<unknown> {
    return new Promise((resolve, reject) => {
      this.client.GetContainer({ container_id: containerId }, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Stop container
   */
  async stopContainer(containerId: string): Promise<unknown> {
    return new Promise((resolve, reject) => {
      this.client.StopContainer({ container_id: containerId }, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Remove container (placeholder - not in proto yet)
   */
  async removeContainer(containerId: string): Promise<unknown> {
    // Container removal might be through StopContainer
    return this.stopContainer(containerId);
  }

  /**
   * Get container logs
   */
  async getLogs(containerId: string, playerId: number, level?: string, limit?: number): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {
        container_id: containerId,
      };
      if (level !== undefined) {
        request.level = level;
      }
      if (limit !== undefined) {
        request.limit = limit;
      }

      this.client.GetContainerLogs(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Scout markets (VRP fleet distribution)
   */
  async scoutMarkets(
    shipSymbols: string[],
    playerId: number,
    system: string,
    markets: string[],
    iterations: number = -1
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request = {
        ship_symbols: shipSymbols,
        system_symbol: system,
        markets,
        iterations,
        player_id: playerId,
      };

      this.client.ScoutMarkets(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Navigate ship to destination
   */
  async navigateShip(
    shipSymbol: string,
    destination: string,
    playerId: number
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request = {
        ship_symbol: shipSymbol,
        destination,
        player_id: playerId,
      };

      this.client.NavigateShip(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Dock ship
   */
  async dockShip(shipSymbol: string, playerId: number): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request = {
        ship_symbol: shipSymbol,
        player_id: playerId,
      };

      this.client.DockShip(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Put ship in orbit
   */
  async orbitShip(shipSymbol: string, playerId: number): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request = {
        ship_symbol: shipSymbol,
        player_id: playerId,
      };

      this.client.OrbitShip(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Refuel ship
   */
  async refuelShip(
    shipSymbol: string,
    playerId: number,
    units?: number
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {
        ship_symbol: shipSymbol,
        player_id: playerId,
      };
      if (units !== undefined) {
        request.units = units;
      }

      this.client.RefuelShip(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Batch purchase ships
   */
  async batchPurchaseShips(
    purchasingShipSymbol: string,
    shipType: string,
    quantity: number,
    maxBudget: number,
    playerId: number | undefined,
    shipyardWaypoint?: string,
    agent?: string
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {
        purchasing_ship_symbol: purchasingShipSymbol,
        ship_type: shipType,
        quantity,
        max_budget: maxBudget,
      };

      if (playerId !== undefined) {
        request.player_id = playerId;
      } else if (agent) {
        request.agent_symbol = agent;
      } else {
        reject(new Error("Either playerId or agent must be provided"));
        return;
      }

      if (shipyardWaypoint) {
        request.shipyard_waypoint = shipyardWaypoint;
      }

      // Note: This RPC doesn't exist yet in daemon.proto
      // Using placeholder until BatchPurchaseShips RPC is added
      reject(new Error("BatchPurchaseShips RPC not implemented in daemon yet"));
    });
  }

  /**
   * Execute batch contract workflow
   */
  async batchContractWorkflow(
    shipSymbol: string,
    playerId: number | undefined,
    count: number = 1,
    agent?: string
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {
        ship_symbol: shipSymbol,
        iterations: count,
      };

      if (playerId !== undefined) {
        request.player_id = playerId;
      } else if (agent) {
        request.agent_symbol = agent;
      } else {
        reject(new Error("Either playerId or agent must be provided"));
        return;
      }

      this.client.BatchContractWorkflow(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * List all ships for a player
   */
  async listShips(playerId?: number, agentSymbol?: string): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {};
      if (playerId !== undefined) {
        request.player_id = playerId;
      }
      if (agentSymbol !== undefined) {
        request.agent_symbol = agentSymbol;
      }

      this.client.ListShips(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }

  /**
   * Get detailed ship information
   */
  async getShip(shipSymbol: string, playerId?: number, agentSymbol?: string): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: any = {
        ship_symbol: shipSymbol,
      };
      if (playerId !== undefined) {
        request.player_id = playerId;
      }
      if (agentSymbol !== undefined) {
        request.agent_symbol = agentSymbol;
      }

      this.client.GetShip(request, (error: any, response: any) => {
        if (error) {
          reject(error);
        } else {
          resolve(response);
        }
      });
    });
  }
}
