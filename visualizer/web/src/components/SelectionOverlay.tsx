import type { CSSProperties } from 'react';
import type { SelectionOverlay as SelectionOverlayData } from '../hooks/useSelectionOverlay';

export interface SelectionOverlayProps {
  overlay: SelectionOverlayData;
}

const CORNER_POSITIONS: Array<['top' | 'bottom', 'left' | 'right']> = [
  ['top', 'left'],
  ['top', 'right'],
  ['bottom', 'left'],
  ['bottom', 'right'],
];

export const SelectionOverlay = ({ overlay }: SelectionOverlayProps) => {
  const containerStyle: CSSProperties = {
    left: `${overlay.left}px`,
    top: `${overlay.top}px`,
    width: `${overlay.size * 2}px`,
    height: `${overlay.size * 2}px`,
    transform: 'translate(-50%, -50%)',
  };

  const primaryBorderClass =
    overlay.type === 'ship'
      ? 'absolute inset-0 rounded-lg border border-red-400/80 shadow-[0_0_12px_rgba(248,113,113,0.8)]'
      : 'absolute inset-0 rounded-lg border border-sky-300/80 shadow-[0_0_12px_rgba(125,211,252,0.8)]';

  const secondaryBorderClass =
    overlay.type === 'ship'
      ? 'absolute inset-[3px] rounded-lg border border-red-500/50'
      : 'absolute inset-[3px] rounded-lg border border-sky-500/40';

  const cornerBorderClass = overlay.type === 'ship' ? 'absolute h-2 w-2 border-red-200/90' : 'absolute h-2 w-2 border-sky-200/90';

  return (
    <div className="absolute pointer-events-none z-20" style={containerStyle}>
      <div className="relative w-full h-full">
        <div className={primaryBorderClass} />
        <div className={secondaryBorderClass} />
        {CORNER_POSITIONS.map(([vertical, horizontal]) => (
          <div
            key={`${vertical}-${horizontal}`}
            className={cornerBorderClass}
            style={{
              [vertical]: '-3px',
              [horizontal]: '-3px',
              borderStyle: 'solid',
              borderTopWidth: vertical === 'top' ? '2px' : '0px',
              borderBottomWidth: vertical === 'bottom' ? '2px' : '0px',
              borderLeftWidth: horizontal === 'left' ? '2px' : '0px',
              borderRightWidth: horizontal === 'right' ? '2px' : '0px',
            }}
          />
        ))}
      </div>
    </div>
  );
};
