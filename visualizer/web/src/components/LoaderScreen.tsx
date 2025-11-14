import type { PropsWithChildren } from 'react';

type LoaderScreenVariant = 'fullscreen' | 'overlay';

interface LoaderScreenProps {
  title?: string;
  message?: string;
  variant?: LoaderScreenVariant;
  className?: string;
}

/**
 * Branded loading screen with spinner and optional messaging.
 */
const LoaderScreen = ({
  title = 'Loading…',
  message,
  variant = 'fullscreen',
  className = '',
  children,
}: PropsWithChildren<LoaderScreenProps>) => {
  const baseClasses =
    variant === 'overlay'
      ? 'absolute inset-0 flex items-center justify-center bg-black/80 backdrop-blur-sm'
      : 'w-full h-full flex items-center justify-center bg-gradient-to-b from-gray-950 via-gray-900 to-gray-950';

  return (
    <div
      className={`${baseClasses} text-white ${className}`}
      role="status"
      aria-live="polite"
    >
      <div className="flex flex-col items-center gap-5">
        <div
          className="h-14 w-14 rounded-full border-4 border-blue-500 border-t-transparent animate-spin"
          aria-hidden="true"
        />
        <div className="text-center space-y-2">
          <h2 className="text-xl font-semibold tracking-wide">{title}</h2>
          {message && <p className="text-gray-300 text-sm">{message}</p>}
        </div>
        {children}
      </div>
    </div>
  );
};

export default LoaderScreen;
