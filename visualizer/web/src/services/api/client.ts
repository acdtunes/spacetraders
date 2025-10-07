import { API_CONSTANTS } from '../../constants/api';

export async function fetchApi<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const url = `${API_CONSTANTS.BASE_URL}${endpoint}`;

  try {
    const response = await fetch(url, {
      headers: {
        'Content-Type': 'application/json',
        ...options?.headers,
      },
      ...options,
    });

    if (!response.ok) {
      const contentType = response.headers.get('content-type');

      // Check if we got HTML instead of JSON (backend not running)
      if (contentType?.includes('text/html')) {
        throw new Error(
          'Backend server not responding. Make sure the server is running on port 4000. ' +
          'Run: cd server && npm start'
        );
      }

      const error = await response.json().catch(() => ({ error: 'Request failed' }));
      throw new Error(error.error || `HTTP ${response.status}`);
    }

    return response.json();
  } catch (err: any) {
    // Network error (backend not running)
    if (err.message.includes('Failed to fetch') || err.name === 'TypeError') {
      throw new Error(
        'Cannot connect to backend server. Make sure the server is running on port 4000. ' +
        'Run: cd server && npm start'
      );
    }
    throw err;
  }
}
