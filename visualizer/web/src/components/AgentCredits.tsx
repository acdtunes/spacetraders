import { useStore } from '../store/useStore';

const AgentCredits = () => {
  const { agents } = useStore();

  if (agents.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-3 text-sm">
      {agents.map((agent) => {
        const creditsLabel =
          agent.credits !== undefined ? `${agent.credits.toLocaleString()}₡` : '--';

        return (
          <div
            key={agent.id}
            className="flex items-center gap-2 rounded border border-gray-700 bg-gray-900/60 px-3 py-1"
          >
            <span
              className="h-2.5 w-2.5 rounded-full"
              style={{ backgroundColor: agent.color }}
              aria-hidden="true"
            />
            <span className="font-medium text-gray-100">{agent.symbol}</span>
            <span className="font-mono text-green-400">{creditsLabel}</span>
          </div>
        );
      })}
    </div>
  );
};

export default AgentCredits;
