import { fetchApi } from './client';

export interface CreditRank {
  agentSymbol: string;
  credits: number;
}

export interface ChartRank {
  agentSymbol: string;
  chartCount: number;
}

export interface Leaderboards {
  mostCredits: CreditRank[];
  mostSubmittedCharts: ChartRank[];
}

export interface LeaderboardResponse {
  resetDate: string | null;
  leaderboards: Leaderboards;
}

// GET /api/leaderboard — global standings from the SpaceTraders status endpoint.
export async function getLeaderboard(): Promise<LeaderboardResponse> {
  return fetchApi<LeaderboardResponse>('/leaderboard');
}
