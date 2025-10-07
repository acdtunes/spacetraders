export class SpaceTradersClient {
  private baseUrl: string;
  private accountToken?: string;

  constructor(baseUrl: string, accountToken?: string) {
    this.baseUrl = baseUrl;
    this.accountToken = accountToken;
  }

  private getHeaders(agentToken?: string): Record<string, string> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };

    const token = agentToken || this.accountToken;
    if (token) {
      headers["Authorization"] = `Bearer ${token}`;
    }

    return headers;
  }

  async get(path: string, agentToken?: string): Promise<any> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: "GET",
      headers: this.getHeaders(agentToken),
    });

    if (!response.ok) {
      const error: any = await response.json().catch(() => ({ error: { message: response.statusText } }));
      throw new Error(
        `API request failed: ${response.status} - ${error.error?.message || response.statusText}`
      );
    }

    return response.json();
  }

  async post(path: string, body: any, agentToken?: string): Promise<any> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: "POST",
      headers: this.getHeaders(agentToken),
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
