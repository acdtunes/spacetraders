import { API_CONSTANTS } from '../constants/api';
import type { ApiResponse } from '../types/spacetraders';

interface PaginatedFetcherOptions {
  limit?: number;
}

/**
 * Fetch all paginated results from an API endpoint
 * @param fetchPage - Function that fetches a single page
 * @param options - Options including limit per page
 * @returns Array of all results
 */
export async function fetchAllPaginated<T>(
  fetchPage: (page: number, limit: number) => Promise<ApiResponse<T[]>>,
  options: PaginatedFetcherOptions = {}
): Promise<T[]> {
  const limit = options.limit || API_CONSTANTS.PAGINATION_LIMIT;
  let allResults: T[] = [];
  let page = 1;
  let hasMore = true;

  while (hasMore) {
    try {
      const response = await fetchPage(page, limit);
      allResults = [...allResults, ...response.data];

      // Check if there are more pages
      if (response.meta?.page && response.meta?.limit && response.meta?.total) {
        if (response.meta.page * response.meta.limit >= response.meta.total) {
          hasMore = false;
        } else {
          page++;
        }
      } else if (response.data.length < limit) {
        hasMore = false;
      } else {
        page++;
      }
    } catch (error: any) {
      // If we get a 404, it means we've reached the end of available pages
      if (error.statusCode === 404 || error.message?.includes('404')) {
        hasMore = false;
      } else {
        // For other errors, rethrow
        throw error;
      }
    }
  }

  return allResults;
}
