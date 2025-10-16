import { API_CONSTANTS } from '../../constants/api';
import { mockRequest } from './mockClient';

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
    public readonly cause?: unknown
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export interface ApiRequestOptions extends RequestInit {
  /**
   * Controls whether the response body should be parsed as JSON.
   * Defaults to true to match the SpaceTraders API responses.
   */
  parseJson?: boolean;
  /**
   * Maximum number of retry attempts for rate-limited requests (429 errors).
   * Defaults to 3.
   */
  maxRetries?: number;
  /**
   * Base delay in milliseconds for exponential backoff.
   * Defaults to 1000ms.
   */
  baseRetryDelay?: number;
}

class ApiClient {
  constructor(private readonly baseUrl: string) {}

  async request<T>(endpoint: string, options: ApiRequestOptions = {}): Promise<T> {
    const { maxRetries = 3, baseRetryDelay = 1000 } = options;
    return this.requestWithRetry<T>(endpoint, options, 0, maxRetries, baseRetryDelay);
  }

  private async requestWithRetry<T>(
    endpoint: string,
    options: ApiRequestOptions,
    retryCount: number,
    maxRetries: number,
    baseRetryDelay: number
  ): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;
    const { parseJson = true, headers, maxRetries: _, baseRetryDelay: __, ...fetchOptions } = options;

    let response: Response;

    try {
      response = await fetch(url, {
        ...fetchOptions,
        headers: this.buildHeaders(headers, fetchOptions.body),
      });
    } catch (error) {
      // Surface a consistent error for network failures to help the UI handle it gracefully.
      throw new ApiError(
        'Cannot connect to backend server. Make sure the server is running on port 4000. Run: cd server && npm start',
        undefined,
        error
      );
    }

    // Handle 429 rate limit with exponential backoff + jitter
    if (response.status === 429 && retryCount < maxRetries) {
      const delay = this.calculateBackoffWithJitter(baseRetryDelay, retryCount);
      console.warn(
        `Rate limited (429) on ${endpoint}. Retrying in ${delay}ms (attempt ${retryCount + 1}/${maxRetries})...`
      );

      await this.sleep(delay);
      return this.requestWithRetry<T>(endpoint, options, retryCount + 1, maxRetries, baseRetryDelay);
    }

    if (!response.ok) {
      throw await this.createError(response);
    }

    if (!parseJson) {
      return undefined as T;
    }

    if (response.status === 204) {
      return undefined as T;
    }

    const contentLength = response.headers.get('content-length');
    if (contentLength === '0') {
      return undefined as T;
    }

    const contentType = response.headers.get('content-type') ?? '';
    if (!contentType.includes('application/json')) {
      return undefined as T;
    }

    return (await response.json()) as T;
  }

  /**
   * Calculate exponential backoff delay with jitter.
   * Formula: baseDelay * (2^retryCount) + random jitter
   * Jitter helps prevent thundering herd problem.
   */
  private calculateBackoffWithJitter(baseDelay: number, retryCount: number): number {
    const exponentialDelay = baseDelay * Math.pow(2, retryCount);
    const jitter = Math.random() * baseDelay * 0.5; // 0-50% of base delay
    return Math.floor(exponentialDelay + jitter);
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  private buildHeaders(headers: HeadersInit | undefined, body: BodyInit | null | undefined): HeadersInit {
    const finalHeaders = new Headers(headers);

    if (!finalHeaders.has('Content-Type') && this.shouldSendJsonHeader(body)) {
      finalHeaders.set('Content-Type', 'application/json');
    }

    return finalHeaders;
  }

  private shouldSendJsonHeader(body: BodyInit | null | undefined): boolean {
    if (!body) {
      return true;
    }

    if (body instanceof FormData || body instanceof URLSearchParams || body instanceof Blob) {
      return false;
    }

    if (typeof body === 'string') {
      // Assume caller has already set appropriate headers for raw string payloads.
      return false;
    }

    return true;
  }

  private async createError(response: Response): Promise<ApiError> {
    const contentType = response.headers.get('content-type');

    if (contentType?.includes('text/html')) {
      return new ApiError(
        'Backend server not responding. Make sure the server is running on port 4000. Run: cd server && npm start',
        response.status
      );
    }

    if (contentType?.includes('application/json')) {
      const errorBody = await response.json().catch(() => null);
      const message = errorBody?.error || `HTTP ${response.status}`;
      return new ApiError(message, response.status, errorBody);
    }

    return new ApiError(`HTTP ${response.status}`, response.status);
  }
}

export const apiClient = new ApiClient(API_CONSTANTS.BASE_URL);

const env = (import.meta as unknown as { env?: Record<string, string | undefined> }).env ?? {};
const useMockApi = env.VITE_USE_MOCK_API === 'true';

export const fetchApi = useMockApi
  ? (mockRequest as <T>(endpoint: string, options?: ApiRequestOptions) => Promise<T>)
  : (apiClient.request.bind(apiClient) as <T>(endpoint: string, options?: ApiRequestOptions) => Promise<T>);
