import { useAgentForm } from '../hooks/useAgentActions';

const AddAgentCard = () => {
  const { token, setToken, submit, isSubmitting, error } = useAgentForm();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    await submit();
  };

  return (
    <div className="bg-gray-800 rounded-lg p-6 max-w-2xl w-full">
      <h2 className="text-2xl font-bold mb-2">Add SpaceTraders Agent</h2>
      <p className="text-gray-400 mb-6">
        Enter your SpaceTraders agent token to start tracking your fleet
      </p>

      <form onSubmit={handleSubmit}>
        <div className="mb-4">
          <label className="block text-sm font-medium mb-2">
            Agent Token
          </label>
          <input
            type="text"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
            className="w-full px-4 py-3 bg-gray-900 border border-gray-600 rounded-lg text-white focus:outline-none focus:border-blue-500 font-mono text-sm"
            disabled={isSubmitting}
          />
          <p className="text-xs text-gray-500 mt-2">
            Get your token at{' '}
            <a
              href="https://spacetraders.io"
              target="_blank"
              rel="noopener noreferrer"
              className="text-blue-400 hover:text-blue-300"
            >
              spacetraders.io
            </a>
          </p>
        </div>

        {error && (
          <div className="mb-4 p-3 bg-red-900/50 border border-red-600 rounded text-red-200 text-sm">
            {error}
          </div>
        )}

        <button
          type="submit"
          disabled={isSubmitting || !token.trim()}
          className="w-full px-6 py-3 bg-blue-600 hover:bg-blue-700 disabled:bg-gray-600 disabled:cursor-not-allowed rounded-lg font-medium transition-colors"
        >
          {isSubmitting ? 'Adding Agent...' : 'Add Agent'}
        </button>
      </form>

      <div className="mt-6 pt-6 border-t border-gray-700">
        <h3 className="font-semibold mb-2 text-sm">How to get your token:</h3>
        <ol className="text-sm text-gray-400 space-y-1 list-decimal list-inside">
          <li>Visit <a href="https://spacetraders.io" target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:text-blue-300">spacetraders.io</a></li>
          <li>Register a new agent or log in</li>
          <li>Copy your agent token from the dashboard</li>
          <li>Paste it above and click "Add Agent"</li>
        </ol>
      </div>
    </div>
  );
};

export default AddAgentCard;
