import { useStore } from '../store/useStore';

const AgentCredits = () => {
  const { agents, ships, currentSystem } = useStore();

  if (!currentSystem || agents.length === 0) {
    return null;
  }

  const agentIdsInSystem = new Set(
    ships
      .filter((ship) => ship.nav.systemSymbol === currentSystem && ship.agentId)
      .map((ship) => ship.agentId)
  );

  if (agentIdsInSystem.size === 0) {
    return null;
  }

  const agentsOnSystem = agents.filter((agent) => agentIdsInSystem.has(agent.id));

  if (agentsOnSystem.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-3 text-sm">
      {agentsOnSystem.map((agent) => {
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
