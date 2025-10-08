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
}

class ApiClient {
  constructor(private readonly baseUrl: string) {}

  async request<T>(endpoint: string, options: ApiRequestOptions = {}): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;
    const { parseJson = true, headers, ...fetchOptions } = options;

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

const useMockApi = import.meta.env.VITE_USE_MOCK_API === 'true';

export const fetchApi = useMockApi
  ? (mockRequest as <T>(endpoint: string, options?: ApiRequestOptions) => Promise<T>)
  : (apiClient.request.bind(apiClient) as <T>(endpoint: string, options?: ApiRequestOptions) => Promise<T>);
