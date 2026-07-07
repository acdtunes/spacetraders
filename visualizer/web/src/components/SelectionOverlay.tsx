import type { CSSProperties } from 'react';
import type { SelectionOverlay as SelectionOverlayData } from '../hooks/useSelectionOverlay';
import { NOIR, noirAlpha } from '../theme/noir';

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

  // Noir accent for both; ships stay the brighter primary blue, waypoints the softer one.
  const accent = overlay.type === 'ship' ? NOIR.accent : NOIR.accentSoft;

  const primaryBorderStyle: CSSProperties = {
    borderColor: accent,
    boxShadow: `0 0 12px ${noirAlpha(accent, 0.7)}`,
  };

  const secondaryBorderStyle: CSSProperties = {
    borderColor: noirAlpha(accent, 0.5),
  };

  return (
    <div className="absolute pointer-events-none z-20" style={containerStyle}>
      <div className="relative w-full h-full">
        <div className="absolute inset-0 rounded-lg border" style={primaryBorderStyle} />
        <div className="absolute inset-[3px] rounded-lg border" style={secondaryBorderStyle} />
        {CORNER_POSITIONS.map(([vertical, horizontal]) => (
          <div
            key={`${vertical}-${horizontal}`}
            className="absolute h-2 w-2"
            style={{
              [vertical]: '-3px',
              [horizontal]: '-3px',
              borderColor: noirAlpha(accent, 0.9),
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
