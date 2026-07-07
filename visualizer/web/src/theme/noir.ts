export const NOIR = {
  bg0: '#04060D',
  bg1: '#0A0F1E',
  nebula: '#16223F',
  nebulaCore: '#2B4470',
  panel: '#0D1220',
  ink: '#EAEEF6',
  muted: '#8B95AB',
  dim: '#5A6478',
  accent: '#7DB1FF',
  accentSoft: '#9CC5FF',
  good: '#3DD68C',
  warn: '#F5C518',
  bad: '#FF6369',
  star: '#F5E9C8',
} as const;

export function noirAlpha(hex: string, alpha: number): string {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}
