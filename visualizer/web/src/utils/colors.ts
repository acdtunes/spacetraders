/**
 * Lighten a color by adding to each RGB component
 * @param color - Hex color value (e.g., 0xFF0000) or CSS color string (e.g., "#ff0000")
 * @param amount - Amount to add to each component (0-255)
 * @returns Lightened color (same type as input)
 */
export function lightenColor(color: number, amount?: number): number;
export function lightenColor(color: string, amount?: number): string;
export function lightenColor(color: number | string, amount: number = 50): number | string {
  if (typeof color === 'string') {
    // Parse CSS color string
    const hex = color.replace('#', '');
    const num = parseInt(hex, 16);
    const r = Math.min(255, ((num >> 16) & 0xFF) + amount);
    const g = Math.min(255, ((num >> 8) & 0xFF) + amount);
    const b = Math.min(255, (num & 0xFF) + amount);
    return '#' + ((r << 16) | (g << 8) | b).toString(16).padStart(6, '0');
  }

  const r = Math.min(255, ((color >> 16) & 0xFF) + amount);
  const g = Math.min(255, ((color >> 8) & 0xFF) + amount);
  const b = Math.min(255, (color & 0xFF) + amount);
  return (r << 16) | (g << 8) | b;
}

/**
 * Darken a color by subtracting from each RGB component
 * @param color - Hex color value (e.g., 0xFF0000) or CSS color string (e.g., "#ff0000")
 * @param amount - Amount to subtract from each component (0-255)
 * @returns Darkened color (same type as input)
 */
export function darkenColor(color: number, amount?: number): number;
export function darkenColor(color: string, amount?: number): string;
export function darkenColor(color: number | string, amount: number = 50): number | string {
  if (typeof color === 'string') {
    // Parse CSS color string
    const hex = color.replace('#', '');
    const num = parseInt(hex, 16);
    const r = Math.max(0, ((num >> 16) & 0xFF) - amount);
    const g = Math.max(0, ((num >> 8) & 0xFF) - amount);
    const b = Math.max(0, (num & 0xFF) - amount);
    return '#' + ((r << 16) | (g << 8) | b).toString(16).padStart(6, '0');
  }

  const r = Math.max(0, ((color >> 16) & 0xFF) - amount);
  const g = Math.max(0, ((color >> 8) & 0xFF) - amount);
  const b = Math.max(0, (color & 0xFF) - amount);
  return (r << 16) | (g << 8) | b;
}
