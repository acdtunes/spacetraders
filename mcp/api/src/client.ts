import { getTokenForPlayer } from "./database.js";

export class SpaceTradersClient {
  private baseUrl: string;
  private accountToken?: string;

  constructor(baseUrl: string, accountToken?: string) {
    this.baseUrl = baseUrl;
    this.accountToken = accountToken;
  }

  private getHeaders(playerId?: number): Record<string, string> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };

    let token: string | null | undefined;

    // If playerId provided, look up token from database
    if (playerId !== undefined) {
      token = getTokenForPlayer(playerId);
      if (!token) {
        throw new Error(`No token found for player_id ${playerId} in database`);
      }
    } else {
      // Fallback to account token from environment
      token = this.accountToken;
    }

    if (token) {
      headers["Authorization"] = `Bearer ${token}`;
    }

    return headers;
  }

  async get(path: string, playerId?: number): Promise<any> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: "GET",
      headers: this.getHeaders(playerId),
    });

    if (!response.ok) {
      const error: any = await response.json().catch(() => ({ error: { message: response.statusText } }));
      throw new Error(
        `API request failed: ${response.status} - ${error.error?.message || response.statusText}`
      );
    }

    return response.json();
  }

  async post(path: string, body: any, playerId?: number): Promise<any> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: "POST",
      headers: this.getHeaders(playerId),
      body: JSON.stringify(body),
    });

    if (!response.ok) {
      const error: any = await response.json().catch(() => ({ error: { message: response.statusText } }));
      throw new Error(
        `API request failed: ${response.status} - ${error.error?.message || response.statusText}`
      );
    }

    return response.json();
  }
}
