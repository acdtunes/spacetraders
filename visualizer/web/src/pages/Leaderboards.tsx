import { useCallback, useEffect, useState } from 'react';
import { getLeaderboard, type LeaderboardResponse } from '../services/api/leaderboard';
import { getAgents } from '../services/api/agents';

interface RankRow {
  agentSymbol: string;
  value: number;
  rank: number;
}

function medalClass(rank: number): string {
  if (rank === 1) return 'text-yellow-300';
  if (rank === 2) return 'text-gray-300';
  if (rank === 3) return 'text-amber-500';
  return 'text-gray-500';
}

function RankTable({
  title,
  rows,
  valueLabel,
  formatValue,
  ourSymbols,
  emptyMessage,
}: {
  title: string;
  rows: RankRow[];
  valueLabel: string;
  formatValue: (v: number) => string;
  ourSymbols: Set<string>;
  emptyMessage: string;
}) {
  return (
    <div className="flex-1 min-w-0 bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
      <div className="px-4 py-3 border-b border-gray-700">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-gray-300">{title}</h2>
      </div>
      {rows.length === 0 ? (
        <div className="px-4 py-10 text-center text-sm text-gray-500">{emptyMessage}</div>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase tracking-wider text-gray-500">
              <th className="px-4 py-2 w-12">#</th>
              <th className="px-4 py-2">Agent</th>
              <th className="px-4 py-2 text-right">{valueLabel}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const ours = ourSymbols.has(row.agentSymbol);
              return (
                <tr
                  key={row.agentSymbol}
                  className={`border-t border-gray-700/60 ${
                    ours ? 'bg-blue-600/20 border-l-2 border-l-blue-400' : 'hover:bg-gray-700/40'
                  }`}
                >
                  <td className={`px-4 py-2 font-bold tabular-nums ${medalClass(row.rank)}`}>{row.rank}</td>
                  <td className="px-4 py-2">
                    <span className={ours ? 'font-semibold text-blue-300' : 'text-gray-200'}>
                      {row.agentSymbol}
                    </span>
                    {ours && (
                      <span className="ml-2 rounded bg-blue-500/30 px-1.5 py-0.5 text-[10px] font-medium text-blue-200">
                        YOU
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums text-gray-300">{formatValue(row.value)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function Leaderboards() {
  const [data, setData] = useState<LeaderboardResponse | null>(null);
  const [ourSymbols, setOurSymbols] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [board, agents] = await Promise.all([
        getLeaderboard(),
        // Tracked agents are how we know which rows are "ours". If this fails
        // (e.g. none configured), the board still renders — just without highlights.
        getAgents().catch(() => []),
      ]);
      setData(board);
      setOurSymbols(new Set(agents.map((a) => a.symbol)));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load leaderboard');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const creditRows: RankRow[] = (data?.leaderboards.mostCredits ?? []).map((r, i) => ({
    agentSymbol: r.agentSymbol,
    value: r.credits,
    rank: i + 1,
  }));
  const chartRows: RankRow[] = (data?.leaderboards.mostSubmittedCharts ?? []).map((r, i) => ({
    agentSymbol: r.agentSymbol,
    value: r.chartCount,
    rank: i + 1,
  }));

  // Best standing among our tracked agents on the credits board (for the summary).
  const ourBest = creditRows.find((r) => ourSymbols.has(r.agentSymbol)) ?? null;

  return (
    <div className="h-full overflow-auto bg-gray-900 text-gray-100">
      <div className="mx-auto max-w-5xl px-6 py-6">
        <div className="mb-4 flex items-center justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold">Leaderboards</h1>
            <p className="text-sm text-gray-500">
              Global standings from the SpaceTraders status endpoint
              {data?.resetDate ? ` · reset ${data.resetDate}` : ''}
            </p>
          </div>
          <button
            onClick={() => void load()}
            disabled={loading}
            className="rounded bg-gray-700 px-3 py-1.5 text-sm text-gray-200 transition-colors hover:bg-gray-600 disabled:opacity-50"
          >
            {loading ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>

        {ourBest && (
          <div className="mb-5 rounded-lg border border-blue-500/40 bg-blue-600/10 px-4 py-3 text-sm">
            <span className="font-semibold text-blue-300">{ourBest.agentSymbol}</span> is ranked{' '}
            <span className="font-bold text-blue-200">#{ourBest.rank}</span> of {creditRows.length} by credits ·{' '}
            <span className="tabular-nums">{ourBest.value.toLocaleString()}</span> credits
          </div>
        )}

        {error && (
          <div className="mb-5 rounded-lg border border-red-500/40 bg-red-600/10 px-4 py-3 text-sm text-red-300">
            {error}
          </div>
        )}

        {loading && !data ? (
          <div className="py-20 text-center text-gray-500">Loading leaderboard…</div>
        ) : (
          <div className="flex flex-col gap-6 lg:flex-row">
            <RankTable
              title="Most Credits"
              rows={creditRows}
              valueLabel="Credits"
              formatValue={(v) => v.toLocaleString()}
              ourSymbols={ourSymbols}
              emptyMessage="No credit rankings yet this reset."
            />
            <RankTable
              title="Most Submitted Charts"
              rows={chartRows}
              valueLabel="Charts"
              formatValue={(v) => v.toLocaleString()}
              ourSymbols={ourSymbols}
              emptyMessage="No charts submitted yet this reset."
            />
          </div>
        )}
      </div>
    </div>
  );
}
