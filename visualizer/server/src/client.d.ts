export declare class SpaceTradersClient {
    private baseUrl;
    private accountToken?;
    constructor(baseUrl: string, accountToken?: string);
    private getHeaders;
    get(path: string, agentToken?: string): Promise<any>;
    post(path: string, body: any, agentToken?: string): Promise<any>;
}
//# sourceMappingURL=client.d.ts.map