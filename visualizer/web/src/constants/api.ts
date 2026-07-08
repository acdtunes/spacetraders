export const API_CONSTANTS = {
  BASE_URL: '/api',
  POLL_INTERVAL: 15000, // milliseconds
  REQUEST_DELAY: 2000, // milliseconds between requests
  PAGINATION_LIMIT: 20,
} as const;

/**
 * The waypoint whose construction bill the Operational Pulse tracks as the
 * mission's jump-gate spine (`GET /api/bot/construction/:wp`).
 *
 * ERA-COUPLED: this is the CURRENT-era jump-gate construction site. It changes
 * whenever the fleet advances to a new home system / era — update this single
 * source of truth when that happens; nothing else should hard-code the symbol.
 * Current era: system X1-PZ28.
 */
export const GATE_WAYPOINT = 'X1-PZ28-I67';
