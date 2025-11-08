/**
 * Node.js client for SpaceTraders daemon Unix socket
 * Talks directly to daemon socket avoiding Python spawn overhead
 */

import net from "node:net";
import path from "node:path";

const SOCKET_PATH = path.join(process.cwd(), "var", "daemon.sock");
const REQUEST_TIMEOUT_MS = 10000; // 10 seconds

interface JsonRpcRequest {
  jsonrpc: string;
  method: string;
  params: Record<string, unknown>;
  id: number;
}

interface JsonRpcResponse {
  jsonrpc: string;
  result?: unknown;
  error?: {
    code: number;
    message: string;
  };
  id: number;
}

export class DaemonClient {
  private requestId = 0;

  /**
   * Send JSON-RPC request to daemon via Unix socket
   */
  private async sendRequest(method: string, params: Record<string, unknown>): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const request: JsonRpcRequest = {
        jsonrpc: "2.0",
        method,
        params,
        id: ++this.requestId,
      };

      const socket = net.createConnection(SOCKET_PATH);
      let responseData = "";
      const timeout = setTimeout(() => {
        socket.destroy();
        reject(new Error(`Request timeout after ${REQUEST_TIMEOUT_MS}ms`));
      }, REQUEST_TIMEOUT_MS);

      socket.on("connect", () => {
        // Send request
        socket.write(JSON.stringify(request));
        // Don't call socket.end() or shutdown(SHUT_WR) to avoid latency
      });

      socket.on("data", (chunk) => {
        responseData += chunk.toString();

        // Try to parse response immediately (server closes socket without waiting for ACK)
        try {
          const response: JsonRpcResponse = JSON.parse(responseData);
          clearTimeout(timeout);
          socket.destroy();

          if (response.error) {
            reject(new Error(response.error.message));
          } else {
            resolve(response.result);
          }
        } catch (error) {
          // Not complete JSON yet, wait for more data
        }
      });

      socket.on("end", () => {
        // Fallback if server properly closes socket
        if (responseData) {
          clearTimeout(timeout);
          try {
            const response: JsonRpcResponse = JSON.parse(responseData);
            if (response.error) {
              reject(new Error(response.error.message));
            } else {
              resolve(response.result);
            }
          } catch (error) {
            reject(new Error(`Invalid JSON response: ${error}`));
          }
        }
      });

      socket.on("error", (error) => {
        clearTimeout(timeout);
        reject(error);
      });
    });
  }

  async listContainers(playerId?: number): Promise<unknown> {
    const params: Record<string, unknown> = {};
    if (playerId !== undefined) {
      params.player_id = playerId;
    }
    return this.sendRequest("list_containers", params);
  }

  async inspectContainer(containerId: string): Promise<unknown> {
    return this.sendRequest("inspect_container", { container_id: containerId });
  }

  async stopContainer(containerId: string): Promise<unknown> {
    return this.sendRequest("stop_container", { container_id: containerId });
  }

  async removeContainer(containerId: string): Promise<unknown> {
    return this.sendRequest("remove_container", { container_id: containerId });
  }

  async getLogs(containerId: string, playerId: number, level?: string, limit?: number): Promise<unknown> {
    const params: Record<string, unknown> = {
      container_id: containerId,
      player_id: playerId,
    };
    if (level !== undefined) {
      params.level = level;
    }
    if (limit !== undefined) {
      params.limit = limit;
    }
    return this.sendRequest("get_logs", params);
  }

  async scoutMarkets(
    shipSymbols: string[],
    playerId: number,
    system: string,
    markets: string[],
    iterations: number = -1
  ): Promise<unknown> {
    // Generate unique container ID (matches Python pattern)
    const randomHex = Math.floor(Math.random() * 0xFFFFFFFF).toString(16).padStart(8, '0');
    const containerId = `scout-markets-vrp-${randomHex}`;

    const params = {
      container_id: containerId,
      player_id: playerId,
      container_type: "command",
      config: {
        command_type: "ScoutMarketsVRPCommand",
        params: {
          ship_symbols: shipSymbols,
          player_id: playerId,
          system,
          markets,
          iterations,
        },
      },
    };
    return this.sendRequest("container.create", params);
  }
}
