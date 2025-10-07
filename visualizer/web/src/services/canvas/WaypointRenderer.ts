import type { Waypoint } from '../../types/spacetraders';

// Helper to convert hex number to CSS color string
function hexToCSS(hex: number, alpha: number = 1): string {
  const r = (hex >> 16) & 0xFF;
  const g = (hex >> 8) & 0xFF;
  const b = hex & 0xFF;
  return alpha < 1 ? `rgba(${r}, ${g}, ${b}, ${alpha})` : `rgb(${r}, ${g}, ${b})`;
}

// Helper to draw polygon from points array
function drawPoly(context: CanvasRenderingContext2D, points: number[]): void {
  if (points.length < 2) return;
  context.beginPath();
  context.moveTo(points[0], points[1]);
  for (let i = 2; i < points.length; i += 2) {
    context.lineTo(points[i], points[i + 1]);
  }
  context.closePath();
}

/**
 * Draw waypoint shape based on type using Canvas 2D API
 * Converted from PixiJS Graphics to preserve all visual details
 */
export function drawWaypoint(
  context: CanvasRenderingContext2D,
  waypoint: Waypoint,
  x: number,
  y: number,
  radius: number
): void {
  const type = waypoint.type;

  // Save context state
  context.save();

  switch (type) {
    case 'PLANET':
      // Determine planet type based on position hash for consistency
      const hash = (x * 73856093) ^ (y * 19349663);
      const planetType = Math.abs(hash) % 6;

      if (planetType === 0) {
        // Earth-like planet with oceans and continents
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x2980b9, 1.0);
        context.fill();

        // Multiple continents
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.2, radius * 0.4, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x27ae60, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.2, y + radius * 0.35, radius * 0.35, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x16a085, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.45, y - radius * 0.35, radius * 0.25, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x229954, 0.8);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.5, y + radius * 0.4, radius * 0.22, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x1e8449, 0.8);
        context.fill();

        // Ice caps
        context.beginPath();
        context.arc(x, y - radius * 0.7, radius * 0.22, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xecf0f1, 0.9);
        context.fill();

        context.beginPath();
        context.arc(x, y + radius * 0.7, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xbdc3c7, 0.9);
        context.fill();

        // Cloud swirls
        context.beginPath();
        context.arc(x - radius * 0.15, y - radius * 0.5, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xffffff, 0.25);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.35, y + radius * 0.15, radius * 0.15, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xffffff, 0.2);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x1a5490, 0.9);
        context.lineWidth = 1.5;
        context.stroke();

      } else if (planetType === 1) {
        // Desert/arid planet
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xd35400, 1.0);
        context.fill();

        // Lighter sand regions
        context.beginPath();
        context.arc(x - radius * 0.25, y - radius * 0.3, radius * 0.35, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xe67e22, 0.7);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.3, y + radius * 0.25, radius * 0.4, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xe67e22, 0.65);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.4, y + radius * 0.35, radius * 0.25, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xe67e22, 0.6);
        context.fill();

        // Dark rocky regions
        context.beginPath();
        context.arc(x + radius * 0.35, y - radius * 0.3, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x8b4513, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.35, y + radius * 0.15, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x654321, 0.8);
        context.fill();

        // Dust storms
        context.beginPath();
        context.arc(x + radius * 0.15, y - radius * 0.5, radius * 0.15, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xffffff, 0.3);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.2, y + radius * 0.6, radius * 0.12, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xffffff, 0.25);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0xc0392b, 0.9);
        context.lineWidth = 1.5;
        context.stroke();

      } else if (planetType === 2) {
        // Ice planet
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xd5f4e6, 0.95);
        context.fill();

        // Frozen surfaces with irregular ice shelves
        const ice1Points: number[] = [];
        for (let i = 0; i < 10; i++) {
          const angle = (i * Math.PI * 2) / 10;
          const r = radius * (0.3 + Math.sin(i * 1.8) * 0.08);
          ice1Points.push(x - radius * 0.35 + Math.cos(angle) * r, y - radius * 0.25 + Math.sin(angle) * r);
        }
        drawPoly(context, ice1Points);
        context.fillStyle = hexToCSS(0xa9dfbf, 0.7);
        context.fill();

        const ice2Points: number[] = [];
        for (let i = 0; i < 12; i++) {
          const angle = (i * Math.PI * 2) / 12;
          const r = radius * (0.35 + Math.cos(i * 2) * 0.1);
          ice2Points.push(x + radius * 0.3 + Math.cos(angle) * r, y + radius * 0.3 + Math.sin(angle) * r);
        }
        drawPoly(context, ice2Points);
        context.fillStyle = hexToCSS(0x85c1e2, 0.7);
        context.fill();

        const ice3Points: number[] = [];
        for (let i = 0; i < 8; i++) {
          const angle = (i * Math.PI * 2) / 8;
          const r = radius * (0.22 + Math.sin(i * 2.3) * 0.06);
          ice3Points.push(x + radius * 0.4 + Math.cos(angle) * r, y - radius * 0.4 + Math.sin(angle) * r);
        }
        drawPoly(context, ice3Points);
        context.fillStyle = hexToCSS(0xaed6f1, 0.75);
        context.fill();

        const ice4Points: number[] = [];
        for (let i = 0; i < 9; i++) {
          const angle = (i * Math.PI * 2) / 9;
          const r = radius * (0.25 + Math.cos(i * 1.7) * 0.07);
          ice4Points.push(x - radius * 0.45 + Math.cos(angle) * r, y + radius * 0.35 + Math.sin(angle) * r);
        }
        drawPoly(context, ice4Points);
        context.fillStyle = hexToCSS(0x7fb3d5, 0.7);
        context.fill();

        // Cracks/crevasses
        const crack1Points: number[] = [];
        for (let i = 0; i < 6; i++) {
          const angle = (i * Math.PI * 2) / 6;
          const r = radius * (0.13 + Math.sin(i * 3) * 0.04);
          crack1Points.push(x + radius * 0.1 + Math.cos(angle) * r, y + Math.sin(angle) * r);
        }
        drawPoly(context, crack1Points);
        context.fillStyle = hexToCSS(0x5499c7, 0.6);
        context.fill();

        const crack2Points: number[] = [];
        for (let i = 0; i < 5; i++) {
          const angle = (i * Math.PI * 2) / 5;
          const r = radius * (0.1 + Math.cos(i * 2.2) * 0.03);
          crack2Points.push(x - radius * 0.2 + Math.cos(angle) * r, y - radius * 0.4 + Math.sin(angle) * r);
        }
        drawPoly(context, crack2Points);
        context.fillStyle = hexToCSS(0x5dade2, 0.6);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x3498db, 0.8);
        context.lineWidth = 1;
        context.stroke();

      } else if (planetType === 3) {
        // Volcanic/lava planet
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x1c1c1c, 0.95);
        context.fill();

        // Lava flows
        const lava1Points: number[] = [];
        for (let i = 0; i < 10; i++) {
          const angle = (i * Math.PI * 2) / 10;
          const r = radius * (0.3 + Math.sin(i * 2.1) * 0.09);
          lava1Points.push(x - radius * 0.3 + Math.cos(angle) * r, y - radius * 0.2 + Math.sin(angle) * r);
        }
        drawPoly(context, lava1Points);
        context.fillStyle = hexToCSS(0xe74c3c, 0.8);
        context.fill();

        const lava2Points: number[] = [];
        for (let i = 0; i < 8; i++) {
          const angle = (i * Math.PI * 2) / 8;
          const r = radius * (0.26 + Math.cos(i * 1.8) * 0.08);
          lava2Points.push(x + radius * 0.25 + Math.cos(angle) * r, y + radius * 0.3 + Math.sin(angle) * r);
        }
        drawPoly(context, lava2Points);
        context.fillStyle = hexToCSS(0xc0392b, 0.8);
        context.fill();

        const lava3Points: number[] = [];
        for (let i = 0; i < 9; i++) {
          const angle = (i * Math.PI * 2) / 9;
          const r = radius * (0.22 + Math.sin(i * 2.5) * 0.07);
          lava3Points.push(x + radius * 0.4 + Math.cos(angle) * r, y - radius * 0.35 + Math.sin(angle) * r);
        }
        drawPoly(context, lava3Points);
        context.fillStyle = hexToCSS(0xff6b6b, 0.75);
        context.fill();

        const lava4Points: number[] = [];
        for (let i = 0; i < 7; i++) {
          const angle = (i * Math.PI * 2) / 7;
          const r = radius * (0.19 + Math.cos(i * 2.2) * 0.06);
          lava4Points.push(x - radius * 0.4 + Math.cos(angle) * r, y + radius * 0.4 + Math.sin(angle) * r);
        }
        drawPoly(context, lava4Points);
        context.fillStyle = hexToCSS(0xe67e22, 0.8);
        context.fill();

        // Volcanic vents
        const vent1Points: number[] = [];
        for (let i = 0; i < 6; i++) {
          const angle = (i * Math.PI * 2) / 6;
          const r = radius * (0.13 + Math.sin(i * 2.8) * 0.03);
          vent1Points.push(x + Math.cos(angle) * r, y - radius * 0.5 + Math.sin(angle) * r);
        }
        drawPoly(context, vent1Points);
        context.fillStyle = hexToCSS(0xf39c12, 0.9);
        context.fill();

        const vent2Points: number[] = [];
        for (let i = 0; i < 5; i++) {
          const angle = (i * Math.PI * 2) / 5;
          const r = radius * (0.1 + Math.cos(i * 2) * 0.03);
          vent2Points.push(x + radius * 0.5 + Math.cos(angle) * r, y + radius * 0.1 + Math.sin(angle) * r);
        }
        drawPoly(context, vent2Points);
        context.fillStyle = hexToCSS(0xf1c40f, 0.9);
        context.fill();

        const vent3Points: number[] = [];
        for (let i = 0; i < 5; i++) {
          const angle = (i * Math.PI * 2) / 5;
          const r = radius * (0.08 + Math.sin(i * 1.5) * 0.02);
          vent3Points.push(x - radius * 0.3 + Math.cos(angle) * r, y + radius * 0.5 + Math.sin(angle) * r);
        }
        drawPoly(context, vent3Points);
        context.fillStyle = hexToCSS(0xffa500, 0.9);
        context.fill();

      } else if (planetType === 4) {
        // Ocean world with archipelagos
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x1a5490, 0.95);
        context.fill();

        // Small scattered island chains
        for (let i = 0; i < 10; i++) {
          const angle = (i * Math.PI * 2) / 10;
          const dist = radius * (0.4 + (i % 3) * 0.15);
          const islandX = x + Math.cos(angle) * dist;
          const islandY = y + Math.sin(angle) * dist;

          const islandPoints: number[] = [];
          for (let j = 0; j < 6; j++) {
            const islandAngle = (j * Math.PI * 2) / 6;
            const r = radius * (0.08 + Math.sin(j * 2) * 0.03);
            islandPoints.push(islandX + Math.cos(islandAngle) * r, islandY + Math.sin(islandAngle) * r);
          }
          drawPoly(context, islandPoints);
          context.fillStyle = hexToCSS(0x27ae60, 0.8);
          context.fill();
        }

        // Polar ice
        context.beginPath();
        context.arc(x, y - radius * 0.75, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xecf0f1, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x, y + radius * 0.75, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xbdc3c7, 0.85);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x2980b9, 0.8);
        context.lineWidth = 1;
        context.stroke();

      } else {
        // Tropical/jungle world
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x16a085, 0.9);
        context.fill();

        // Dense vegetation coverage
        const jungle1Points: number[] = [];
        for (let i = 0; i < 14; i++) {
          const angle = (i * Math.PI * 2) / 14;
          const r = radius * (0.55 + Math.sin(i * 1.3) * 0.12);
          jungle1Points.push(x - radius * 0.2 + Math.cos(angle) * r, y - radius * 0.15 + Math.sin(angle) * r);
        }
        drawPoly(context, jungle1Points);
        context.fillStyle = hexToCSS(0x1e8449, 0.8);
        context.fill();

        const jungle2Points: number[] = [];
        for (let i = 0; i < 12; i++) {
          const angle = (i * Math.PI * 2) / 12;
          const r = radius * (0.5 + Math.cos(i * 2.2) * 0.13);
          jungle2Points.push(x + radius * 0.25 + Math.cos(angle) * r, y + radius * 0.2 + Math.sin(angle) * r);
        }
        drawPoly(context, jungle2Points);
        context.fillStyle = hexToCSS(0x229954, 0.8);
        context.fill();

        const jungle3Points: number[] = [];
        for (let i = 0; i < 10; i++) {
          const angle = (i * Math.PI * 2) / 10;
          const r = radius * (0.42 + Math.sin(i * 1.8) * 0.1);
          jungle3Points.push(x + radius * 0.35 + Math.cos(angle) * r, y - radius * 0.4 + Math.sin(angle) * r);
        }
        drawPoly(context, jungle3Points);
        context.fillStyle = hexToCSS(0x27ae60, 0.75);
        context.fill();

        // Small water bodies
        context.beginPath();
        context.arc(x - radius * 0.5, y - radius * 0.3, radius * 0.15, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x3498db, 0.7);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.15, y + radius * 0.5, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x2980b9, 0.7);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.3, y + radius * 0.45, radius * 0.12, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x5dade2, 0.7);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x117a65, 0.8);
        context.lineWidth = 1;
        context.stroke();
      }
      break;

    case 'GAS_GIANT':
      context.beginPath();
      context.arc(x, y, radius, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xe67e22, 0.9);
      context.fill();

      // Atmospheric bands
      for (let i = -radius * 0.6; i < radius * 0.6; i += radius * 0.3) {
        context.fillRect(x - radius, y + i, radius * 2, radius * 0.15);
        context.fillStyle = hexToCSS(0xd35400, 0.6);
        context.fill();
      }

      // Great Red Spot
      context.beginPath();
      context.arc(x + radius * 0.3, y, radius * 0.4, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xc0392b, 0.7);
      context.fill();

      // Outer glow
      context.beginPath();
      context.arc(x, y, radius * 1.2, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0xf39c12, 0.4);
      context.lineWidth = 1.5;
      context.stroke();
      break;

    case 'MOON':
      // Vary moon appearance based on position
      const moonHash = (x * 73856093) ^ (y * 19349663);
      const moonType = Math.abs(moonHash) % 3;

      if (moonType === 0) {
        // Gray rocky moon
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x95a5a6, 1.0);
        context.fill();

        // Craters
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.2, radius * 0.3, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.9);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.4, y + radius * 0.1, radius * 0.22, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x, y + radius * 0.5, radius * 0.26, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.88);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.5, y + radius * 0.3, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.82);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.2, y - radius * 0.5, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.86);
        context.fill();

        // Bright ray crater
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.3, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xbdc3c7, 0.7);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x7f8c8d, 0.9);
        context.lineWidth = 1.2;
        context.stroke();

      } else if (moonType === 1) {
        // Brownish/tan moon
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xa0826d, 1.0);
        context.fill();

        // Dark maria
        context.beginPath();
        context.arc(x - radius * 0.25, y - radius * 0.25, radius * 0.35, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x6d5d4b, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.3, y + radius * 0.2, radius * 0.38, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x5c4d3d, 0.8);
        context.fill();

        // Impact craters
        context.beginPath();
        context.arc(x + radius * 0.45, y - radius * 0.35, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x8b7355, 0.9);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.4, y + radius * 0.4, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x786450, 0.88);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.1, y - radius * 0.5, radius * 0.15, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x6d5d4b, 0.85);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x6d5d4b, 0.9);
        context.lineWidth = 1.2;
        context.stroke();

      } else {
        // Icy/white moon
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xd5d8dc, 1.0);
        context.fill();

        // Darker ice regions
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.2, radius * 0.32, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xaeb6bf, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.35, y + radius * 0.25, radius * 0.35, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xbdc3c7, 0.8);
        context.fill();

        // Bright craters
        context.beginPath();
        context.arc(x + radius * 0.4, y - radius * 0.4, radius * 0.22, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xecf0f1, 0.9);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.45, y + radius * 0.35, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xe8daef, 0.85);
        context.fill();

        context.beginPath();
        context.arc(x - radius * 0.1, y + radius * 0.5, radius * 0.18, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xf4ecf7, 0.88);
        context.fill();

        // Outline
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0xaeb6bf, 0.9);
        context.lineWidth = 1.2;
        context.stroke();
      }
      break;

    case 'ORBITAL_STATION':
    case 'FUEL_STATION':
      // Core
      context.beginPath();
      context.arc(x, y, radius * 0.6, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0x34495e, 0.95);
      context.fill();

      // Ring
      context.beginPath();
      context.arc(x, y, radius * 0.9, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0x3498db, 0.8);
      context.lineWidth = 1.5;
      context.stroke();

      // Docking ports
      const dockSize = radius * 0.3;
      context.fillStyle = hexToCSS(0x2c3e50, 0.9);
      context.fillRect(x - radius - dockSize, y - dockSize / 2, dockSize, dockSize);
      context.fillRect(x + radius, y - dockSize / 2, dockSize, dockSize);
      context.fillRect(x - dockSize / 2, y - radius - dockSize, dockSize, dockSize);
      context.fillRect(x - dockSize / 2, y + radius, dockSize, dockSize);

      // Center indicator
      context.beginPath();
      context.arc(x, y, radius * 0.3, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(type === 'FUEL_STATION' ? 0xf39c12 : 0x3498db, 0.9);
      context.fill();

      // Antenna
      context.beginPath();
      context.moveTo(x, y - radius * 0.6);
      context.lineTo(x, y - radius * 1.5);
      context.strokeStyle = hexToCSS(0xecf0f1, 0.8);
      context.lineWidth = 1;
      context.stroke();

      context.beginPath();
      context.arc(x, y - radius * 1.5, radius * 0.15, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xff0000, 0.9);
      context.fill();
      break;

    case 'JUMP_GATE':
      // Outer ring
      context.beginPath();
      context.arc(x, y, radius, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0x9b59b6, 0.9);
      context.lineWidth = 2;
      context.stroke();

      // Inner ring
      context.beginPath();
      context.arc(x, y, radius * 0.7, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0x8e44ad, 0.9);
      context.lineWidth = 1.5;
      context.stroke();

      // Energy spokes
      for (let i = 0; i < 5; i++) {
        const angle = (i * Math.PI * 2) / 5;
        const r = radius * 0.5;
        context.beginPath();
        context.moveTo(x, y);
        context.lineTo(x + Math.cos(angle) * r, y + Math.sin(angle) * r);
        context.strokeStyle = hexToCSS(0xe74c3c, 0.6);
        context.lineWidth = 1;
        context.stroke();
      }

      // Center
      context.beginPath();
      context.arc(x, y, radius * 0.3, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xffffff, 0.8);
      context.fill();
      break;

    case 'ASTEROID':
    case 'ENGINEERED_ASTEROID':
    case 'ASTEROID_BASE':
      // Irregular shape (using deterministic randomness based on position)
      const asteroidHash = (x * 73856093) ^ (y * 19349663);
      const points: number[] = [];
      for (let i = 0; i < 24; i++) {
        const angle = (i * Math.PI * 2) / 24;
        // Use hash-based pseudo-random value for consistent shape
        const pseudoRandom = (Math.abs((asteroidHash ^ (i * 2654435761)) % 1000) / 1000);
        const r = radius * (0.7 + pseudoRandom * 0.3);
        points.push(x + Math.cos(angle) * r, y + Math.sin(angle) * r);
      }
      drawPoly(context, points);
      context.fillStyle = hexToCSS(type === 'ENGINEERED_ASTEROID' ? 0x16a085 : 0x7f8c8d, 0.9);
      context.fill();

      // Craters
      context.beginPath();
      context.arc(x - radius * 0.2, y, radius * 0.2, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0x5d6d7e, 0.8);
      context.fill();

      context.beginPath();
      context.arc(x + radius * 0.3, y - radius * 0.2, radius * 0.15, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0x5d6d7e, 0.8);
      context.fill();

      context.beginPath();
      context.arc(x + radius * 0.1, y + radius * 0.3, radius * 0.12, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0x5d6d7e, 0.8);
      context.fill();

      context.beginPath();
      context.arc(x - radius * 0.4, y - radius * 0.2, radius * 0.18, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0x5d6d7e, 0.8);
      context.fill();

      // Base structures
      if (type === 'ASTEROID_BASE') {
        context.fillStyle = hexToCSS(0x2980b9, 0.9);
        context.fillRect(x - radius * 0.4, y + radius * 0.3, radius * 0.8, radius * 0.3);

        context.beginPath();
        context.arc(x, y + radius * 0.5, radius * 0.2, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x3498db, 0.9);
        context.fill();
      }

      // Engineering lights
      if (type === 'ENGINEERED_ASTEROID') {
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.3, radius * 0.1, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x00ff00, 0.9);
        context.fill();

        context.beginPath();
        context.arc(x + radius * 0.3, y + radius * 0.2, radius * 0.1, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x00ff00, 0.9);
        context.fill();
      }
      break;

    case 'ASTEROID_FIELD':
      // Scattered small asteroids (using deterministic randomness)
      const fieldHash = (x * 73856093) ^ (y * 19349663);
      for (let i = 0; i < 5; i++) {
        const pseudoRandom = (Math.abs((fieldHash ^ (i * 2654435761)) % 1000) / 1000);
        const angle = (i * Math.PI * 2) / 5 + pseudoRandom * 0.5;
        const distance = radius * 0.6;
        const ax = x + Math.cos(angle) * distance;
        const ay = y + Math.sin(angle) * distance;
        const ar = radius * 0.2;
        context.beginPath();
        context.arc(ax, ay, ar, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0x7f8c8d, 0.8);
        context.fill();
      }

      // Center marker
      context.beginPath();
      context.arc(x, y, radius * 0.15, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xecf0f1, 0.6);
      context.fill();
      break;

    case 'DEBRIS_FIELD':
      // Random debris (using deterministic randomness)
      const debrisHash = (x * 73856093) ^ (y * 19349663);
      for (let i = 0; i < 8; i++) {
        const pseudoRandom1 = (Math.abs((debrisHash ^ (i * 2654435761)) % 1000) / 1000);
        const pseudoRandom2 = (Math.abs((debrisHash ^ ((i + 100) * 2654435761)) % 1000) / 1000);
        const angle = pseudoRandom1 * Math.PI * 2;
        const distance = pseudoRandom2 * radius;
        const dx = x + Math.cos(angle) * distance;
        const dy = y + Math.sin(angle) * distance;
        const size = radius * 0.1;
        context.fillStyle = hexToCSS(0x95a5a6, 0.7);
        context.fillRect(dx - size / 2, dy - size / 2, size, size);
      }

      // Warning marker
      context.beginPath();
      context.arc(x, y, radius * 0.2, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0xe74c3c, 0.8);
      context.lineWidth = 1.5;
      context.stroke();
      break;

    case 'NEBULA':
      // Overlapping clouds
      for (let i = 0; i < 3; i++) {
        const offset = radius * 0.3 * (i - 1);
        context.beginPath();
        context.arc(x + offset, y, radius * 0.8, 0, Math.PI * 2);
        context.fillStyle = hexToCSS(0xe74c3c, 0.3);
        context.fill();
      }

      // Core
      context.beginPath();
      context.arc(x, y, radius * 0.5, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xff6b9d, 0.5);
      context.fill();
      break;

    case 'GRAVITY_WELL':
    case 'ARTIFICIAL_GRAVITY_WELL':
      // Concentric rings
      for (let i = 1; i <= 3; i++) {
        context.beginPath();
        context.arc(x, y, radius * i * 0.4, 0, Math.PI * 2);
        context.strokeStyle = hexToCSS(0x8e44ad, 0.6 / i);
        context.lineWidth = 1;
        context.stroke();
      }

      // Center
      context.beginPath();
      context.arc(x, y, radius * 0.3, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(type === 'ARTIFICIAL_GRAVITY_WELL' ? 0x3498db : 0x000000, 0.95);
      context.fill();

      // Energy lines
      for (let i = 0; i < 6; i++) {
        const angle = (i * Math.PI * 2) / 6;
        context.beginPath();
        context.moveTo(x, y);
        context.lineTo(x + Math.cos(angle) * radius * 0.25, y + Math.sin(angle) * radius * 0.25);
        context.strokeStyle = hexToCSS(0xe74c3c, 0.7);
        context.lineWidth = 0.5;
        context.stroke();
      }
      break;

    default:
      // Generic waypoint
      context.beginPath();
      context.arc(x, y, radius, 0, Math.PI * 2);
      context.fillStyle = hexToCSS(0xffffff, 0.7);
      context.fill();

      context.beginPath();
      context.arc(x, y, radius, 0, Math.PI * 2);
      context.strokeStyle = hexToCSS(0xecf0f1, 0.8);
      context.lineWidth = 1;
      context.stroke();
  }

  // Restore context state
  context.restore();
}
