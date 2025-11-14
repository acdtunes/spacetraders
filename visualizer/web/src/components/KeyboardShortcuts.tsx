import { useEffect, useState } from 'react';

interface KeyboardShortcutsProps {
  onZoomIn: () => void;
  onZoomOut: () => void;
  onReset: () => void;
  onFitView: () => void;
  onToggleSidebar: () => void;
  onSwitchTab: (tab: 'ships' | 'details' | 'search') => void;
  buttonClassName?: string;
}

const KeyboardShortcuts = ({
  onZoomIn,
  onZoomOut,
  onReset,
  onFitView,
  onToggleSidebar,
  onSwitchTab,
  buttonClassName,
}: KeyboardShortcutsProps) => {
  const [showHelp, setShowHelp] = useState(false);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Ignore if typing in an input
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        // Allow ? to open help even in input
        if (e.key === '?' && (e.shiftKey || e.key === '?')) {
          e.preventDefault();
          setShowHelp(!showHelp);
        }
        return;
      }

      // Help modal
      if (e.key === '?' && (e.shiftKey || e.key === '?')) {
        e.preventDefault();
        setShowHelp(!showHelp);
        return;
      }

      // Close help modal
      if (showHelp && e.key === 'Escape') {
        e.preventDefault();
        setShowHelp(false);
        return;
      }

      // Tab switching
      if (e.key === '1') {
        e.preventDefault();
        onSwitchTab('ships');
      } else if (e.key === '2') {
        e.preventDefault();
        onSwitchTab('details');
      } else if (e.key === '3') {
        e.preventDefault();
        onSwitchTab('search');
      }

      // Sidebar toggle
      else if (e.key === ' ' && !e.ctrlKey && !e.metaKey) {
        e.preventDefault();
        onToggleSidebar();
      }

      // Zoom controls
      else if (e.key === '+' || e.key === '=') {
        e.preventDefault();
        onZoomIn();
      } else if (e.key === '-' || e.key === '_') {
        e.preventDefault();
        onZoomOut();
      }

      // View controls
      else if (e.key === '0') {
        e.preventDefault();
        onReset();
      } else if (e.key === 'f' || e.key === 'F') {
        e.preventDefault();
        onFitView();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [showHelp, onZoomIn, onZoomOut, onReset, onFitView, onToggleSidebar, onSwitchTab]);

  return (
    <>
      {/* Help Button */}
      <button
        onClick={() => setShowHelp(!showHelp)}
        className={
          buttonClassName ??
          'px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-xs font-semibold text-gray-400 hover:text-white hover:bg-gray-700 transition-colors flex items-center gap-1 z-20'
        }
        title="Keyboard Shortcuts (Shift + ?)"
      >
        <span>⌨️</span>
        <span>?</span>
      </button>

      {/* Help Modal */}
      {showHelp && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={() => setShowHelp(false)}>
          <div className="bg-gray-800 border-2 border-gray-700 rounded-lg shadow-2xl max-w-2xl w-full mx-4" onClick={(e) => e.stopPropagation()}>
            {/* Header */}
            <div className="border-b border-gray-700 px-6 py-4 flex items-center justify-between">
              <h2 className="text-xl font-bold text-white">Keyboard Shortcuts</h2>
              <button
                onClick={() => setShowHelp(false)}
                className="text-gray-400 hover:text-white transition-colors"
              >
                ✕
              </button>
            </div>

            {/* Content */}
            <div className="p-6 max-h-[70vh] overflow-y-auto">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                {/* Navigation */}
                <div>
                  <h3 className="text-sm font-bold text-gray-400 uppercase mb-3">Navigation</h3>
                  <div className="space-y-2 text-sm">
                    <ShortcutItem keys={['+']} description="Zoom in" />
                    <ShortcutItem keys={['-']} description="Zoom out" />
                    <ShortcutItem keys={['0']} description="Reset view" />
                    <ShortcutItem keys={['F']} description="Fit to view" />
                    <ShortcutItem keys={['Drag']} description="Pan map" />
                    <ShortcutItem keys={['Scroll']} description="Zoom in/out" />
                  </div>
                </div>

                {/* Selection */}
                <div>
                  <h3 className="text-sm font-bold text-gray-400 uppercase mb-3">Selection</h3>
                  <div className="space-y-2 text-sm">
                    <ShortcutItem keys={['Click']} description="Select ship/waypoint" />
                    <ShortcutItem keys={['Esc']} description="Clear selection" />
                  </div>
                </div>

                {/* Sidebar */}
                <div>
                  <h3 className="text-sm font-bold text-gray-400 uppercase mb-3">Sidebar</h3>
                  <div className="space-y-2 text-sm">
                    <ShortcutItem keys={['1']} description="Ships tab" />
                    <ShortcutItem keys={['2']} description="Details tab" />
                    <ShortcutItem keys={['3']} description="Search tab" />
                    <ShortcutItem keys={['Space']} description="Toggle sidebar" />
                  </div>
                </div>

                {/* Help */}
                <div>
                  <h3 className="text-sm font-bold text-gray-400 uppercase mb-3">Help</h3>
                  <div className="space-y-2 text-sm">
                    <ShortcutItem keys={['?']} description="Show this help" />
                  </div>
                </div>
              </div>
            </div>

            {/* Footer */}
            <div className="border-t border-gray-700 px-6 py-3 bg-gray-750">
              <p className="text-xs text-gray-500">
                Press <kbd className="px-1.5 py-0.5 bg-gray-700 rounded text-gray-300">Esc</kbd> or click outside to close
              </p>
            </div>
          </div>
        </div>
      )}
    </>
  );
};

const ShortcutItem = ({ keys, description }: { keys: string[]; description: string }) => (
  <div className="flex items-center justify-between">
    <span className="text-gray-400">{description}</span>
    <div className="flex gap-1">
      {keys.map((key, i) => (
        <kbd
          key={i}
          className="px-2 py-1 bg-gray-700 rounded text-gray-300 text-xs font-mono min-w-[1.5rem] text-center"
        >
          {key}
        </kbd>
      ))}
    </div>
  </div>
);

export default KeyboardShortcuts;
