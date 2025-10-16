export class SpaceTradersClient {
  private baseUrl: string;
  private accountToken?: string;
  private readonly maxRetries = 3;
  private readonly baseRetryDelayMs = 500;

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
    return this.request("GET", path, undefined, agentToken);
  }

  async post(path: string, body: any, agentToken?: string): Promise<any> {
    return this.request("POST", path, body, agentToken);
  }

  private async request(method: "GET" | "POST", path: string, body?: any, agentToken?: string): Promise<any> {
    const maxAttempts = this.maxRetries + 1;

    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      const requestInit: RequestInit = {
        method,
        headers: this.getHeaders(agentToken),
      };

      if (body !== undefined) {
        requestInit.body = JSON.stringify(body);
      }

      const response = await fetch(`${this.baseUrl}${path}`, requestInit);

      if (response.status === 429 && attempt < this.maxRetries) {
        await this.delay(this.getRetryDelayMs(response, attempt));
        continue;
      }

      if (!response.ok) {
        const error: any = await response.json().catch(() => ({ error: { message: response.statusText } }));
        throw new Error(
          `API request failed: ${response.status} - ${error.error?.message || response.statusText}`
        );
      }

      return response.json();
    }

    throw new Error("API request failed after maximum retry attempts");
  }

  private async delay(delayMs: number): Promise<void> {
    if (delayMs <= 0) {
      return;
    }

    await new Promise((resolve) => setTimeout(resolve, delayMs));
  }

  private getRetryDelayMs(response: Response, attempt: number): number {
    const retryAfterHeader = response.headers.get("Retry-After");
    const retryAfterMs = this.parseRetryAfterHeader(retryAfterHeader);

    if (retryAfterMs !== null && retryAfterMs > 0) {
      return retryAfterMs;
    }

    return this.baseRetryDelayMs * Math.pow(2, attempt);
  }

  private parseRetryAfterHeader(retryAfter: string | null): number | null {
    if (!retryAfter) {
      return null;
    }

    const asNumber = Number(retryAfter);
    if (!Number.isNaN(asNumber)) {
      return Math.max(asNumber * 1000, 0);
    }

    const asDate = Date.parse(retryAfter);
    if (!Number.isNaN(asDate)) {
      const delayMs = asDate - Date.now();
      return delayMs > 0 ? delayMs : 0;
    }

    return null;
  }
}
