import React, { Component, ReactNode } from 'react';

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
    console.error('Error caught by boundary:', error, errorInfo);
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="h-screen w-screen flex items-center justify-center bg-gray-900 text-white p-8">
          <div className="max-w-2xl">
            <h1 className="text-3xl font-bold mb-4 text-red-500">Something went wrong</h1>
            <div className="bg-gray-800 p-4 rounded-lg mb-4">
              <p className="text-sm font-mono text-red-400">
                {this.state.error?.toString()}
              </p>
              <pre className="text-xs text-gray-400 mt-2 overflow-auto">
                {this.state.error?.stack}
              </pre>
            </div>
            <button
              onClick={() => window.location.reload()}
              className="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded"
            >
              Reload Page
            </button>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
}

export default ErrorBoundary;
