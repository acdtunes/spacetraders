interface OverlayToggleProps {
  label: string;
  active: boolean;
  onToggle: () => void;
  activeTone?: 'orange' | 'amber' | 'sky' | 'rose' | 'lime';
}

const toneClasses: Record<NonNullable<OverlayToggleProps['activeTone']>, string> = {
  orange: 'border-orange-400 text-orange-300 hover:border-orange-300 hover:text-orange-200',
  amber: 'border-amber-400 text-amber-300 hover:border-amber-300 hover:text-amber-200',
  sky: 'border-sky-400 text-sky-300 hover:border-sky-300 hover:text-sky-200',
  rose: 'border-rose-400 text-rose-300 hover:border-rose-300 hover:text-rose-200',
  lime: 'border-lime-400 text-lime-300 hover:border-lime-300 hover:text-lime-200',
};

const OverlayToggle = ({ label, active, onToggle, activeTone = 'sky' }: OverlayToggleProps) => {
  const activeClasses = `bg-gray-800 border-2 ${toneClasses[activeTone]}`;
  const inactiveClasses = 'bg-gray-800 border border-gray-700 text-gray-400 hover:border-gray-600 hover:text-gray-200';

  return (
    <button
      type="button"
      onClick={onToggle}
      className={`px-2 py-1 rounded transition-colors text-left ${active ? activeClasses : inactiveClasses}`}
    >
      <span className="text-[11px] font-semibold tracking-wide uppercase">{label}</span>
    </button>
  );
};

export default OverlayToggle;
