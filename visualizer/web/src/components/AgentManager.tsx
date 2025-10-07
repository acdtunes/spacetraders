import { useState } from 'react';
import { useStore } from '../store/useStore';
import { addAgent as apiAddAgent, deleteAgent as apiDeleteAgent } from '../services/api';

const AgentManager = () => {
  const { agents, addAgent, removeAgent } = useStore();
  const [isOpen, setIsOpen] = useState(false);
  const [newToken, setNewToken] = useState('');
  const [isAdding, setIsAdding] = useState(false);
  const [error, setError] = useState('');

  const handleAddAgent = async () => {
    if (!newToken.trim()) {
      setError('Token is required');
      return;
    }

    setIsAdding(true);
    setError('');

    try {
      const agent = await apiAddAgent(newToken.trim());
      addAgent(agent);
      setNewToken('');
      setError('');
    } catch (err: any) {
      setError(err.message || 'Failed to add agent');
    } finally {
      setIsAdding(false);
    }
  };

  const handleDeleteAgent = async (id: string) => {
    if (!confirm('Are you sure you want to remove this agent?')) return;

    try {
      await apiDeleteAgent(id);
      removeAgent(id);
    } catch (err: any) {
      alert(`Failed to delete agent: ${err.message}`);
    }
  };

  return (
    <div className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="px-4 py-2 bg-purple-600 hover:bg-purple-700 rounded text-white font-medium flex items-center gap-2"
      >
        <span>👥</span>
        <span>Agents ({agents.length})</span>
        <span>{isOpen ? '▲' : '▼'}</span>
      </button>

      {isOpen && (
        <div className="absolute top-full mt-2 right-0 bg-gray-800 border border-gray-700 rounded shadow-lg w-96 z-10 max-h-[80vh] overflow-y-auto">
          <div className="p-4 border-b border-gray-700">
            <h3 className="font-bold mb-3">Add Another Agent</h3>
            <input
              type="text"
              placeholder="Paste agent token here..."
              value={newToken}
              onChange={(e) => setNewToken(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleAddAgent()}
              className="w-full px-3 py-2 bg-gray-900 border border-gray-600 rounded text-white mb-2 font-mono text-sm"
            />
            {error && <div className="text-red-400 text-sm mb-2">{error}</div>}
            <button
              onClick={handleAddAgent}
              disabled={isAdding}
              className="w-full px-4 py-2 bg-green-600 hover:bg-green-700 disabled:bg-gray-600 rounded text-white font-medium"
            >
              {isAdding ? 'Adding...' : 'Add Agent'}
            </button>
          </div>

          <div className="max-h-64 overflow-y-auto">
            {agents.length === 0 ? (
              <div className="p-4 text-gray-400 text-center">No agents added</div>
            ) : (
              <div className="py-2">
                {agents.map((agent) => (
                  <div
                    key={agent.id}
                    className="px-4 py-3 border-b border-gray-700 hover:bg-gray-750 flex items-center justify-between"
                  >
                    <div className="flex items-center gap-3 flex-1">
                      <div
                        className="w-4 h-4 rounded-full"
                        style={{ backgroundColor: agent.color }}
                      />
                      <div className="flex-1">
                        <div className="font-medium">{agent.symbol}</div>
                        <div className="text-xs text-gray-400 flex items-center gap-2">
                          <span>{new Date(agent.createdAt).toLocaleDateString()}</span>
                          {agent.credits !== undefined && (
                            <>
                              <span>•</span>
                              <span className="text-green-400 font-mono">
                                {agent.credits.toLocaleString()}₡
                              </span>
                            </>
                          )}
                        </div>
                      </div>
                    </div>
                    <button
                      onClick={() => handleDeleteAgent(agent.id)}
                      className="px-2 py-1 bg-red-600 hover:bg-red-700 rounded text-xs"
                    >
                      Remove
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default AgentManager;
