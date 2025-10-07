export class SpaceTradersClient {
    baseUrl;
    accountToken;
    constructor(baseUrl, accountToken) {
        this.baseUrl = baseUrl;
        this.accountToken = accountToken;
    }
    getHeaders(agentToken) {
        const headers = {
            "Content-Type": "application/json",
        };
        const token = agentToken || this.accountToken;
        if (token) {
            headers["Authorization"] = `Bearer ${token}`;
        }
        return headers;
    }
    async get(path, agentToken) {
        const response = await fetch(`${this.baseUrl}${path}`, {
            method: "GET",
            headers: this.getHeaders(agentToken),
        });
        if (!response.ok) {
            const error = await response.json().catch(() => ({ error: { message: response.statusText } }));
            throw new Error(`API request failed: ${response.status} - ${error.error?.message || response.statusText}`);
        }
        return response.json();
    }
    async post(path, body, agentToken) {
        const response = await fetch(`${this.baseUrl}${path}`, {
            method: "POST",
            headers: this.getHeaders(agentToken),
            body: JSON.stringify(body),
        });
        if (!response.ok) {
            const error = await response.json().catch(() => ({ error: { message: response.statusText } }));
            throw new Error(`API request failed: ${response.status} - ${error.error?.message || response.statusText}`);
        }
        return response.json();
    }
}
//# sourceMappingURL=client.js.map