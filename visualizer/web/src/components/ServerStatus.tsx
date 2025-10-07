import { useEffect, useState } from 'react';

const ServerStatus = () => {
  const [isConnected, setIsConnected] = useState<boolean | null>(null);

  useEffect(() => {
    const checkServer = async () => {
      try {
        const response = await fetch('/api/agents');
        setIsConnected(response.ok);
      } catch {
        setIsConnected(false);
      }
    };

    checkServer();
    const interval = setInterval(checkServer, 5000);

    return () => clearInterval(interval);
  }, []);

  if (isConnected === null) return null;

  if (!isConnected) {
    return (
      <div className="fixed top-4 left-1/2 -translate-x-1/2 z-50 bg-red-600 text-white px-6 py-3 rounded-lg shadow-lg max-w-2xl">
        <div className="flex items-center gap-3">
          <span className="text-2xl">⚠️</span>
          <div>
            <div className="font-bold">Backend Server Not Running</div>
            <div className="text-sm mt-1">
              Open a terminal and run: <code className="bg-red-700 px-2 py-1 rounded">cd server && npm start</code>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return null;
};

export default ServerStatus;
