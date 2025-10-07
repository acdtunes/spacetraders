import type { Waypoint } from '../../types/spacetraders';

// Helper to convert hex number to CSS color string
function hexToCSS(hex: number, alpha: number = 1): string {
  const r = (hex >> 16) & 0xFF;
  const g = (hex >> 8) & 0xFF;
  const b = hex & 0xFF;
  return alpha < 1 ? `rgba(${r}, ${g}, ${b}, ${alpha})` : `rgb(${r}, ${g}, ${b})`;
}

function adjustHex(hex: number, factor: number): number {
  const clamp = (value: number) => Math.max(0, Math.min(255, value));
  const r = (hex >> 16) & 0xff;
  const g = (hex >> 8) & 0xff;
  const b = hex & 0xff;
  const apply = (channel: number) => {
    if (factor >= 0) {
      return clamp(channel + (255 - channel) * factor);
    }
    return clamp(channel + channel * factor);
  };
  return (apply(r) << 16) | (apply(g) << 8) | apply(b);
}

function createSeededRandom(seed: number): () => number {
  let value = seed % 2147483647;
  if (value <= 0) {
    value += 2147483646;
  }
  return () => {
    value = (value * 16807) % 2147483647;
    return (value - 1) / 2147483646;
  };
}

interface IrregularDiskOptions {
  jaggedness?: number;
  points?: number;
  rotation?: number;
  squash?: number;
}

function drawIrregularDisk(
  context: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  radius: number,
  random: () => number,
  options: IrregularDiskOptions = {}
): void {
  const { jaggedness = 0.25, points = 16, rotation = 0, squash = 1 } = options;
  const cosR = Math.cos(rotation);
  const sinR = Math.sin(rotation);

  context.beginPath();
  for (let i = 0; i < points; i++) {
    const angle = (i / points) * Math.PI * 2;
    const noise = 1 + (random() * 2 - 1) * jaggedness;
    const r = radius * noise;
    const localX = Math.cos(angle) * r;
    const localY = Math.sin(angle) * r * squash;
    const rotatedX = localX * cosR - localY * sinR;
    const rotatedY = localX * sinR + localY * cosR;
    const px = cx + rotatedX;
    const py = cy + rotatedY;
    if (i === 0) {
      context.moveTo(px, py);
    } else {
      context.lineTo(px, py);
    }
  }
  context.closePath();
}

interface MoonPalette {
  base: number;
  highlight: number;
  shadow: number;
  rim: number;
  craterDark: number;
  craterLight: number;
  dust: number;
}

const MOON_PALETTES: MoonPalette[] = [
  {
    base: 0x9ea6b2,
    highlight: 0xd8dee6,
    shadow: 0x5a636f,
    rim: 0x3d4753,
    craterDark: 0x6b737e,
    craterLight: 0xb0b8c4,
    dust: 0xcbd2dc,
  },
  {
    base: 0x8c7460,
    highlight: 0xcab49a,
    shadow: 0x4c3d33,
    rim: 0x352a22,
    craterDark: 0x5d4b3b,
    craterLight: 0xa18a72,
    dust: 0xdfc5a8,
  },
  {
    base: 0x8893a4,
    highlight: 0xc4d2e4,
    shadow: 0x454f61,
    rim: 0x2f3747,
    craterDark: 0x535d70,
    craterLight: 0xa7b4c7,
    dust: 0xd5deeb,
  },
];

function drawDetailedMoon(
  context: CanvasRenderingContext2D,
  x: number,
  y: number,
  radius: number,
  seed: number,
  palette: MoonPalette
): void {
  const random = createSeededRandom(seed || 1);
  context.save();

  const gradient = context.createRadialGradient(
    x - radius * 0.25,
    y - radius * 0.28,
    radius * 0.25,
    x,
    y,
    radius
  );
  gradient.addColorStop(0, hexToCSS(palette.highlight, 1));
  gradient.addColorStop(0.45, hexToCSS(palette.base, 1));
  gradient.addColorStop(1, hexToCSS(palette.shadow, 1));

  context.beginPath();
  context.arc(x, y, radius, 0, Math.PI * 2);
  context.fillStyle = gradient;
  context.fill();

  // Soft terminator shadow for depth
  context.beginPath();
  context.arc(x + radius * 0.18, y + radius * 0.12, radius * 1.08, Math.PI * 0.1, Math.PI * 1.2);
  context.strokeStyle = hexToCSS(adjustHex(palette.shadow, -0.2), 0.25);
  context.lineWidth = radius * 0.25;
  context.stroke();

  const craterTargets: { cx: number; cy: number; r: number }[] = [];
  const craterCount = 7 + Math.floor(random() * 6);
  let attempts = 0;

  while (craterTargets.length < craterCount && attempts < craterCount * 8) {
    attempts += 1;
    const angle = random() * Math.PI * 2;
    const distance = radius * (0.18 + random() * 0.55);
    const craterRadius = radius * (0.08 + random() * 0.18);
    if (distance + craterRadius > radius * 0.92) continue;

    const cx = x + Math.cos(angle) * distance;
    const cy = y + Math.sin(angle) * distance * (0.92 + random() * 0.12);

    let overlaps = false;
    for (const crater of craterTargets) {
      const separation = Math.hypot(crater.cx - cx, crater.cy - cy);
      if (separation < (crater.r + craterRadius) * 0.85) {
        overlaps = true;
        break;
      }
    }
    if (overlaps) continue;

    craterTargets.push({ cx, cy, r: craterRadius });
  }

  // Draw large craters with irregular rims and inner highlights
  craterTargets.forEach(({ cx, cy, r }) => {
    const rotation = random() * Math.PI;
    const squash = 0.85 + random() * 0.3;
    const rimJaggedness = 0.18 + random() * 0.1;
    const rimPoints = 14 + Math.floor(random() * 6);

    // Outer rim shadow
    drawIrregularDisk(context, cx, cy, r * (1.18 + random() * 0.1), random, {
      jaggedness: rimJaggedness,
      points: rimPoints,
      rotation,
      squash,
    });
    context.fillStyle = hexToCSS(adjustHex(palette.craterDark, -0.15 + random() * 0.1), 0.85);
    context.fill();

    // Crater floor
    drawIrregularDisk(context, cx, cy, r * (0.68 + random() * 0.08), random, {
      jaggedness: rimJaggedness * 0.6,
      points: rimPoints,
      rotation,
      squash: squash * (0.9 + random() * 0.1),
    });
    context.fillStyle = hexToCSS(adjustHex(palette.craterLight, -0.05 + random() * 0.08), 0.9);
    context.fill();

    // Rim highlight
    context.beginPath();
    context.arc(
      cx - r * 0.15,
      cy - r * 0.18,
      r * (0.55 + random() * 0.15),
      Math.PI * 1.1,
      Math.PI * 1.85
    );
    context.strokeStyle = hexToCSS(adjustHex(palette.craterLight, 0.35), 0.35);
    context.lineWidth = Math.max(1, r * 0.2);
    context.stroke();

    // Inner core highlight
    drawIrregularDisk(context, cx - r * 0.05, cy - r * 0.05, r * 0.32, random, {
      jaggedness: rimJaggedness * 0.4,
      points: rimPoints,
      rotation,
      squash: squash * 0.9,
    });
    context.fillStyle = hexToCSS(adjustHex(palette.highlight, 0.15 + random() * 0.1), 0.55);
    context.fill();

    // Rim stroke
    drawIrregularDisk(context, cx, cy, r * (1.05 + random() * 0.08), random, {
      jaggedness: rimJaggedness,
      points: rimPoints,
      rotation,
      squash,
    });
    context.strokeStyle = hexToCSS(adjustHex(palette.rim, -0.05), 0.7);
    context.lineWidth = Math.max(0.8, r * 0.25);
    context.stroke();
  });

  // Smaller pockmarks rendered as jagged depressions
  const microCraterCount = 36 + Math.floor(random() * 28);
  for (let i = 0; i < microCraterCount; i++) {
    const angle = random() * Math.PI * 2;
    const distance = radius * Math.pow(random(), 0.75) * 0.94;
    const size = radius * (0.012 + random() * 0.024);
    const cx = x + Math.cos(angle) * distance;
    const cy = y + Math.sin(angle) * distance;
    const points = 5 + Math.floor(random() * 4);
    const rotation = random() * Math.PI * 2;

    drawIrregularDisk(context, cx, cy, size * (1.4 + random() * 0.4), random, {
      jaggedness: 0.45,
      points,
      rotation,
      squash: 0.6 + random() * 0.5,
    });
    context.fillStyle = hexToCSS(adjustHex(palette.craterDark, -0.08 + random() * 0.12), 0.28 + random() * 0.28);
    context.fill();

    drawIrregularDisk(context, cx + size * 0.15, cy + size * 0.1, size * 0.7, random, {
      jaggedness: 0.25,
      points,
      rotation: rotation * 1.5,
      squash: 0.7 + random() * 0.3,
    });
    context.fillStyle = hexToCSS(adjustHex(palette.craterLight, 0.05 + random() * 0.08), 0.25 + random() * 0.2);
    context.fill();
  }

  // Dust streaks/highlights rendered as short strokes
  const dustCount = 55 + Math.floor(random() * 30);
  context.save();
  context.globalAlpha = 0.18;
  context.lineCap = 'round';
  for (let i = 0; i < dustCount; i++) {
    const angle = random() * Math.PI * 2;
    const distance = radius * Math.pow(random(), 0.85) * 0.96;
    const px = x + Math.cos(angle) * distance;
    const py = y + Math.sin(angle) * distance;
    const length = radius * (0.025 + random() * 0.045);
    const dir = random() * Math.PI * 2;
    const offsetX = Math.cos(dir) * length;
    const offsetY = Math.sin(dir) * length * (0.6 + random() * 0.5);
    context.beginPath();
    context.moveTo(px - offsetX * 0.4, py - offsetY * 0.4);
    context.lineTo(px + offsetX * 0.6, py + offsetY * 0.6);
    context.strokeStyle = hexToCSS(adjustHex(palette.dust, (random() - 0.5) * 0.5), 0.4 + random() * 0.3);
    context.lineWidth = Math.max(0.6, radius * 0.02);
    context.stroke();
  }
  context.restore();

  // Subtle noise overlay to break up uniform shading
  const textureSamples = 140 + Math.floor(radius * 12);
  context.save();
  context.globalAlpha = 0.08;
  for (let i = 0; i < textureSamples; i++) {
    const angle = random() * Math.PI * 2;
    const distance = radius * Math.sqrt(random()) * 0.98;
    const px = x + Math.cos(angle) * distance;
    const py = y + Math.sin(angle) * distance;
    const size = radius * (0.006 + random() * 0.012);
    context.fillStyle = hexToCSS(adjustHex(palette.base, (random() - 0.5) * 0.4), 1);
    context.fillRect(px, py, size, size * (0.6 + random() * 0.8));
  }
  context.restore();

  // Rim with subtle highlight
  context.beginPath();
  context.arc(x, y, radius, 0, Math.PI * 2);
  context.strokeStyle = hexToCSS(palette.rim, 0.85);
  context.lineWidth = Math.max(1, radius * 0.09);
  context.stroke();

  context.beginPath();
  context.arc(x - radius * 0.3, y - radius * 0.32, radius * 0.92, Math.PI * 1.15, Math.PI * 1.9);
  context.strokeStyle = hexToCSS(palette.highlight, 0.28);
  context.lineWidth = radius * 0.12;
  context.stroke();

  context.restore();
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

    case 'MOON': {
      const moonHash = (x * 73856093) ^ (y * 19349663);
      const moonType = Math.abs(moonHash) % MOON_PALETTES.length;
      drawDetailedMoon(context, x, y, radius, moonHash, MOON_PALETTES[moonType]);
      break;
    }

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
