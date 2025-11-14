interface ZoomControlsProps {
  onZoomIn: () => void;
  onZoomOut: () => void;
  onReset: () => void;
  onFitView: () => void;
}

const ZoomControls = ({ onZoomIn, onZoomOut, onReset, onFitView }: ZoomControlsProps) => {
  return (
    <div className="absolute bottom-4 right-4 bg-gray-800 border border-gray-700 rounded-lg shadow-lg p-2 flex flex-col gap-2 z-10">
      <button
        onClick={onZoomIn}
        className="w-10 h-10 bg-gray-700 hover:bg-gray-600 rounded flex items-center justify-center transition-colors"
        title="Zoom In (Scroll Up)"
      >
        <span className="text-xl font-bold">+</span>
      </button>
      <button
        onClick={onZoomOut}
        className="w-10 h-10 bg-gray-700 hover:bg-gray-600 rounded flex items-center justify-center transition-colors"
        title="Zoom Out (Scroll Down)"
      >
        <span className="text-xl font-bold">−</span>
      </button>
      <div className="h-px bg-gray-700 my-1" />
      <button
        onClick={onReset}
        className="w-10 h-10 bg-gray-700 hover:bg-gray-600 rounded flex items-center justify-center transition-colors text-xs"
        title="Reset View (0,0)"
      >
        ⌂
      </button>
      <button
        onClick={onFitView}
        className="w-10 h-10 bg-gray-700 hover:bg-gray-600 rounded flex items-center justify-center transition-colors text-xs"
        title="Fit to View"
      >
        ⛶
      </button>
    </div>
  );
};

export default ZoomControls;
