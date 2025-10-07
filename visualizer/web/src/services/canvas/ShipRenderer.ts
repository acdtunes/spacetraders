import { lightenColor, darkenColor } from '../../utils/colors';
import { CANVAS_CONSTANTS, ENGINE_GLOW_COLOR, WINDOW_LIGHT_COLOR } from '../../constants/canvas';

// Helper to convert hex to CSS color
function hexToCSS(hex: number, alpha: number = 1): string {
  const r = (hex >> 16) & 0xFF;
  const g = (hex >> 8) & 0xFF;
  const b = hex & 0xFF;
  return alpha < 1 ? `rgba(${r}, ${g}, ${b}, ${alpha})` : `rgb(${r}, ${g}, ${b})`;
}

// Get CSS color from color utils
function colorToCSS(color: string | number, alpha: number = 1): string {
  if (typeof color === 'string') {
    // Already CSS format
    if (alpha < 1) {
      // Convert to rgba
      const hex = color.replace('#', '');
      const r = parseInt(hex.substr(0, 2), 16);
      const g = parseInt(hex.substr(2, 2), 16);
      const b = parseInt(hex.substr(4, 2), 16);
      return `rgba(${r}, ${g}, ${b}, ${alpha})`;
    }
    return color;
  }
  return hexToCSS(color, alpha);
}

/**
 * Draw ship shape based on role using Canvas 2D API
 * Converted from PixiJS Graphics to preserve all ship designs
 */
export function drawShipShape(
  context: CanvasRenderingContext2D,
  role: string,
  color: number
): void {
  const alpha = 0.95;
  const scale = CANVAS_CONSTANTS.SHIP_SCALE;
  const strokeWidth = CANVAS_CONSTANTS.STROKE_WIDTH;

  context.save();
  context.globalAlpha = alpha;

  switch (role.toUpperCase()) {
    case 'COMMAND':
      drawCommandShip(context, color, scale, strokeWidth);
      break;
    case 'HAULER':
    case 'FREIGHTER':
      drawHaulerShip(context, color, scale, strokeWidth);
      break;
    case 'EXCAVATOR':
    case 'REFINERY':
      drawMiningShip(context, color, scale, strokeWidth);
      break;
    case 'EXPLORER':
    case 'SURVEYOR':
      drawExplorerShip(context, color, scale, strokeWidth);
      break;
    case 'PATROL':
    case 'INTERCEPTOR':
      drawFighterShip(context, color, scale, strokeWidth);
      break;
    case 'SATELLITE':
    case 'PROBE':
      drawProbeShip(context, color, scale, strokeWidth);
      break;
    case 'REPAIR':
      drawRepairShip(context, color, scale, strokeWidth);
      break;
    default:
      drawGenericShip(context, color, scale, strokeWidth);
  }

  // Add outline for all shapes
  context.strokeStyle = hexToCSS(0xffffff, 0.7);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.restore();
}

function drawCommandShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Main hull
  context.beginPath();
  context.moveTo(0, -8 * scale);
  context.lineTo(-2 * scale, -6 * scale);
  context.lineTo(-4 * scale, 2 * scale);
  context.lineTo(-3 * scale, 6 * scale);
  context.lineTo(3 * scale, 6 * scale);
  context.lineTo(4 * scale, 2 * scale);
  context.lineTo(2 * scale, -6 * scale);
  context.closePath();
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fill();

  // Bridge tower
  context.fillStyle = lightenColor(hexToCSS(color), 30);
  context.fillRect(-1.5 * scale, -7 * scale, 3 * scale, 4 * scale);

  // Superstructure
  context.fillStyle = lightenColor(hexToCSS(color), 50);
  context.fillRect(-0.8 * scale, -8 * scale, 1.6 * scale, 2 * scale);

  // Hull panels
  context.beginPath();
  context.moveTo(-3 * scale, 0);
  context.lineTo(3 * scale, 0);
  context.strokeStyle = darkenColor(hexToCSS(color, 0.7), 60);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.beginPath();
  context.moveTo(-3.5 * scale, 3 * scale);
  context.lineTo(3.5 * scale, 3 * scale);
  context.stroke();

  // Engine arrays
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fillRect(-3.5 * scale, 5 * scale, 2 * scale, 2 * scale);
  context.fillRect(1.5 * scale, 5 * scale, 2 * scale, 2 * scale);

  // Engine glow
  context.beginPath();
  context.arc(-2.5 * scale, 6.5 * scale, 1.2 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(ENGINE_GLOW_COLOR, 0.9);
  context.fill();

  context.beginPath();
  context.arc(2.5 * scale, 6.5 * scale, 1.2 * scale, 0, Math.PI * 2);
  context.fill();

  // Windows
  context.fillStyle = hexToCSS(WINDOW_LIGHT_COLOR, 0.8);
  context.fillRect(-0.5 * scale, -6 * scale, 1 * scale, 0.5 * scale);

  // Outline
  context.beginPath();
  context.moveTo(0, -8 * scale);
  context.lineTo(-2 * scale, -6 * scale);
  context.lineTo(-4 * scale, 2 * scale);
  context.lineTo(-3 * scale, 6 * scale);
  context.lineTo(3 * scale, 6 * scale);
  context.lineTo(4 * scale, 2 * scale);
  context.lineTo(2 * scale, -6 * scale);
  context.closePath();
  context.strokeStyle = lightenColor(hexToCSS(color, 0.8), 80);
  context.lineWidth = strokeWidth;
  context.stroke();
}

function drawHaulerShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Front cockpit
  context.beginPath();
  context.moveTo(0, -6 * scale);
  context.lineTo(-2 * scale, -4 * scale);
  context.lineTo(-2 * scale, -2 * scale);
  context.lineTo(2 * scale, -2 * scale);
  context.lineTo(2 * scale, -4 * scale);
  context.closePath();
  context.fillStyle = lightenColor(hexToCSS(color), 40);
  context.fill();

  // Cargo bay
  context.fillStyle = hexToCSS(color);
  context.fillRect(-4 * scale, -2 * scale, 8 * scale, 7 * scale);

  // Cargo containers
  context.fillStyle = darkenColor(hexToCSS(color), 40);
  context.fillRect(-3.5 * scale, -1 * scale, 3 * scale, 2.5 * scale);
  context.fillRect(0.5 * scale, -1 * scale, 3 * scale, 2.5 * scale);

  // Container details
  context.beginPath();
  context.moveTo(-2 * scale, -1 * scale);
  context.lineTo(-2 * scale, 1.5 * scale);
  context.strokeStyle = darkenColor(hexToCSS(color, 0.8), 70);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.beginPath();
  context.moveTo(2 * scale, -1 * scale);
  context.lineTo(2 * scale, 1.5 * scale);
  context.stroke();

  // Side hatches
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fillRect(-4.5 * scale, 1 * scale, 0.8 * scale, 2 * scale);
  context.fillRect(3.7 * scale, 1 * scale, 0.8 * scale, 2 * scale);

  // Engine nacelles
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fillRect(-4 * scale, 4.5 * scale, 2.5 * scale, 2 * scale);
  context.fillRect(1.5 * scale, 4.5 * scale, 2.5 * scale, 2 * scale);

  // Engine exhaust
  context.beginPath();
  context.arc(-2.8 * scale, 6 * scale, 1.1 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff8800, 0.95);
  context.fill();

  context.beginPath();
  context.arc(2.8 * scale, 6 * scale, 1.1 * scale, 0, Math.PI * 2);
  context.fill();

  // Cockpit windows
  context.fillStyle = hexToCSS(0x88ddff, 0.9);
  context.fillRect(-1.2 * scale, -5 * scale, 2.4 * scale, 1 * scale);

  // Comms array
  context.beginPath();
  context.moveTo(0, -2 * scale);
  context.lineTo(0, -3.5 * scale);
  context.strokeStyle = hexToCSS(0xcccccc, 0.8);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.beginPath();
  context.arc(0, -3.5 * scale, 0.5 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff4444, 0.9);
  context.fill();
}

function drawMiningShip(context: CanvasRenderingContext2D, color: number, scale: number, _strokeWidth: number): void {
  // Central hull
  context.fillStyle = hexToCSS(color);
  context.fillRect(-3 * scale, -3 * scale, 6 * scale, 6 * scale);

  // Front drill housing
  context.beginPath();
  context.moveTo(0, -5 * scale);
  context.lineTo(-2 * scale, -3 * scale);
  context.lineTo(2 * scale, -3 * scale);
  context.closePath();
  context.fillStyle = lightenColor(hexToCSS(color), 30);
  context.fill();

  // Mining arm joints
  context.beginPath();
  context.arc(-3 * scale, 0, 1.2 * scale, 0, Math.PI * 2);
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fill();

  context.beginPath();
  context.arc(3 * scale, 0, 1.2 * scale, 0, Math.PI * 2);
  context.fill();

  // Extended drill arms
  context.fillStyle = darkenColor(hexToCSS(color), 40);
  context.fillRect(-6.5 * scale, -1 * scale, 3.5 * scale, 2 * scale);
  context.fillRect(3 * scale, -1 * scale, 3.5 * scale, 2 * scale);

  // Drill heads
  context.beginPath();
  context.arc(-6.5 * scale, 0, 1.3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff9900, 0.95);
  context.fill();

  context.beginPath();
  context.arc(6.5 * scale, 0, 1.3 * scale, 0, Math.PI * 2);
  context.fill();

  // Drill bits
  context.beginPath();
  context.arc(-6.5 * scale, 0, 0.6 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xffdd00, 0.95);
  context.fill();

  context.beginPath();
  context.arc(6.5 * scale, 0, 0.6 * scale, 0, Math.PI * 2);
  context.fill();

  // Processing unit
  context.beginPath();
  context.arc(0, 0, 2 * scale, 0, Math.PI * 2);
  context.fillStyle = lightenColor(hexToCSS(color), 40);
  context.fill();

  context.beginPath();
  context.arc(0, 0, 1.2 * scale, 0, Math.PI * 2);
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fill();

  // Cargo holds
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fillRect(-2.5 * scale, 2 * scale, 2 * scale, 2 * scale);
  context.fillRect(0.5 * scale, 2 * scale, 2 * scale, 2 * scale);

  // Thrusters
  context.fillStyle = hexToCSS(0xff6600, 0.9);
  context.fillRect(-2 * scale, 4 * scale, 1.5 * scale, 1.5 * scale);
  context.fillRect(0.5 * scale, 4 * scale, 1.5 * scale, 1.5 * scale);

  // Industrial lights
  context.beginPath();
  context.arc(-1 * scale, -2 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(WINDOW_LIGHT_COLOR, 0.9);
  context.fill();

  context.beginPath();
  context.arc(1 * scale, -2 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fill();
}

function drawExplorerShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Saucer section
  context.beginPath();
  context.arc(0, -2 * scale, 3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(color);
  context.fill();

  // Bridge dome
  context.beginPath();
  context.arc(0, -3 * scale, 1.2 * scale, 0, Math.PI * 2);
  context.fillStyle = lightenColor(hexToCSS(color), 40);
  context.fill();

  context.beginPath();
  context.arc(0, -3 * scale, 0.6 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x88ddff, 0.95);
  context.fill();

  // Engineering hull
  context.beginPath();
  context.moveTo(-1 * scale, 0);
  context.lineTo(-1.5 * scale, 6 * scale);
  context.lineTo(1.5 * scale, 6 * scale);
  context.lineTo(1 * scale, 0);
  context.closePath();
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fill();

  // Nacelle struts
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fillRect(-4 * scale, 1 * scale, 1 * scale, 3 * scale);
  context.fillRect(3 * scale, 1 * scale, 1 * scale, 3 * scale);

  // Warp nacelles
  context.fillStyle = lightenColor(hexToCSS(color), 20);
  context.fillRect(-5 * scale, 2 * scale, 2 * scale, 5 * scale);
  context.fillRect(3 * scale, 2 * scale, 2 * scale, 5 * scale);

  // Nacelle glow (warp coils)
  context.fillStyle = hexToCSS(0x00aaff, 0.9);
  context.fillRect(-4.8 * scale, 2.5 * scale, 1.6 * scale, 1 * scale);
  context.fillRect(-4.8 * scale, 4.5 * scale, 1.6 * scale, 1 * scale);
  context.fillRect(3.2 * scale, 2.5 * scale, 1.6 * scale, 1 * scale);
  context.fillRect(3.2 * scale, 4.5 * scale, 1.6 * scale, 1 * scale);

  // Deflector dish
  context.beginPath();
  context.arc(0, 3 * scale, 1 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ffaa, 0.9);
  context.fill();

  context.beginPath();
  context.arc(0, 3 * scale, 1.5 * scale, 0, Math.PI * 2);
  context.strokeStyle = hexToCSS(0x00ffaa, 0.7);
  context.lineWidth = strokeWidth;
  context.stroke();

  // Sensor array
  context.beginPath();
  context.arc(-2 * scale, -2 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ff88, 0.9);
  context.fill();

  context.beginPath();
  context.arc(2 * scale, -2 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fill();

  // Impulse engines
  context.fillStyle = hexToCSS(0xff6600, 0.9);
  context.fillRect(-1.2 * scale, -0.5 * scale, 2.4 * scale, 1 * scale);
}

function drawFighterShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Fuselage
  context.beginPath();
  context.moveTo(0, -7 * scale);
  context.lineTo(-1.5 * scale, -5 * scale);
  context.lineTo(-1.2 * scale, 4 * scale);
  context.lineTo(1.2 * scale, 4 * scale);
  context.lineTo(1.5 * scale, -5 * scale);
  context.closePath();
  context.fillStyle = hexToCSS(color);
  context.fill();

  // Cockpit canopy
  context.beginPath();
  context.arc(0, -4 * scale, 1.4 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x44aaff, 0.9);
  context.fill();

  context.beginPath();
  context.arc(0, -4 * scale, 1.8 * scale, 0, Math.PI * 2);
  context.strokeStyle = lightenColor(hexToCSS(color, 0.8), 60);
  context.lineWidth = strokeWidth;
  context.stroke();

  // S-foils/wings
  context.beginPath();
  context.moveTo(-1.5 * scale, -2 * scale);
  context.lineTo(-5 * scale, -3 * scale);
  context.lineTo(-5.5 * scale, 1 * scale);
  context.lineTo(-2 * scale, 2 * scale);
  context.closePath();
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fill();

  context.beginPath();
  context.moveTo(1.5 * scale, -2 * scale);
  context.lineTo(5 * scale, -3 * scale);
  context.lineTo(5.5 * scale, 1 * scale);
  context.lineTo(2 * scale, 2 * scale);
  context.closePath();
  context.fill();

  // Weapon mounts
  context.beginPath();
  context.arc(-5 * scale, -1 * scale, 0.7 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff3300, 0.95);
  context.fill();

  context.beginPath();
  context.arc(5 * scale, -1 * scale, 0.7 * scale, 0, Math.PI * 2);
  context.fill();

  // Engine pods
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fillRect(-2.2 * scale, 3 * scale, 1.5 * scale, 2.5 * scale);
  context.fillRect(0.7 * scale, 3 * scale, 1.5 * scale, 2.5 * scale);

  // Engine exhaust glow
  context.beginPath();
  context.arc(-1.5 * scale, 5.2 * scale, 1 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff4400, 0.95);
  context.fill();

  context.beginPath();
  context.arc(1.5 * scale, 5.2 * scale, 1 * scale, 0, Math.PI * 2);
  context.fill();

  // Reactor vent
  context.fillStyle = hexToCSS(0x00ffff, 0.7);
  context.fillRect(-0.5 * scale, 1 * scale, 1 * scale, 1.5 * scale);

  // Panel lines
  context.beginPath();
  context.moveTo(0, -7 * scale);
  context.lineTo(0, 4 * scale);
  context.strokeStyle = darkenColor(hexToCSS(color, 0.7), 50);
  context.lineWidth = strokeWidth;
  context.stroke();
}

function drawProbeShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Central body
  context.beginPath();
  context.arc(0, 0, 2.2 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(color);
  context.fill();

  // Instrument core
  context.beginPath();
  context.arc(0, 0, 1.3 * scale, 0, Math.PI * 2);
  context.fillStyle = darkenColor(hexToCSS(color), 30);
  context.fill();

  // Central sensor
  context.beginPath();
  context.arc(0, 0, 0.7 * scale, 0, Math.PI * 2);
  context.fillStyle = lightenColor(hexToCSS(color), 40);
  context.fill();

  // Solar panels
  context.fillStyle = hexToCSS(0x0055cc, 0.85);
  context.fillRect(-6 * scale, -0.7 * scale, 3.5 * scale, 1.4 * scale);
  context.fillRect(2.5 * scale, -0.7 * scale, 3.5 * scale, 1.4 * scale);

  // Grid lines
  for (let i = -5.5; i < -2; i += 0.8) {
    context.beginPath();
    context.moveTo(i * scale, -0.5 * scale);
    context.lineTo(i * scale, 0.5 * scale);
    context.strokeStyle = hexToCSS(0x003388, 0.6);
    context.lineWidth = strokeWidth * 0.75;
    context.stroke();
  }
  for (let i = 3; i < 6; i += 0.8) {
    context.beginPath();
    context.moveTo(i * scale, -0.5 * scale);
    context.lineTo(i * scale, 0.5 * scale);
    context.stroke();
  }

  // Comms dish
  context.beginPath();
  context.moveTo(0, -2.2 * scale);
  context.lineTo(0, -4 * scale);
  context.strokeStyle = hexToCSS(0xcccccc, 0.8);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.beginPath();
  context.arc(0, -4.5 * scale, 1.2 * scale, 0, Math.PI * 2);
  context.fillStyle = lightenColor(hexToCSS(color, 0.8), 30);
  context.fill();

  context.beginPath();
  context.arc(0, -4.5 * scale, 0.5 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff2222, 0.95);
  context.fill();

  // Sensor booms
  context.beginPath();
  context.moveTo(0, 2.2 * scale);
  context.lineTo(0, 4 * scale);
  context.strokeStyle = hexToCSS(0xcccccc, 0.8);
  context.lineWidth = strokeWidth;
  context.stroke();

  context.beginPath();
  context.arc(0, 4.2 * scale, 0.6 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ff88, 0.9);
  context.fill();

  // Status lights
  context.beginPath();
  context.arc(-1.5 * scale, 0, 0.3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ff00, 0.9);
  context.fill();

  context.beginPath();
  context.arc(1.5 * scale, 0, 0.3 * scale, 0, Math.PI * 2);
  context.fill();

  // Outer ring
  context.beginPath();
  context.arc(0, 0, 3.2 * scale, 0, Math.PI * 2);
  context.strokeStyle = lightenColor(hexToCSS(color, 0.6), 50);
  context.lineWidth = strokeWidth;
  context.stroke();
}

function drawRepairShip(context: CanvasRenderingContext2D, color: number, scale: number, _strokeWidth: number): void {
  // Central workshop
  context.beginPath();
  context.arc(0, 0, 2.5 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(color);
  context.fill();

  // Workshop core
  context.beginPath();
  context.arc(0, 0, 1.8 * scale, 0, Math.PI * 2);
  context.fillStyle = darkenColor(hexToCSS(color), 20);
  context.fill();

  // Command pod
  context.beginPath();
  context.arc(0, -1 * scale, 1 * scale, 0, Math.PI * 2);
  context.fillStyle = lightenColor(hexToCSS(color), 40);
  context.fill();

  context.beginPath();
  context.arc(0, -1 * scale, 0.5 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x88ddff, 0.9);
  context.fill();

  // Manipulator arms (simplified for brevity)
  const armColor = darkenColor(hexToCSS(color), 30);
  const gripperColor = lightenColor(hexToCSS(color), 20);

  // Top arm
  context.fillStyle = armColor;
  context.fillRect(-0.5 * scale, -2.5 * scale, 1 * scale, 2 * scale);
  context.fillRect(-0.5 * scale, -5.5 * scale, 1 * scale, 3 * scale);
  context.fillStyle = gripperColor;
  context.fillRect(-1 * scale, -6 * scale, 2 * scale, 1.5 * scale);
  context.beginPath();
  context.arc(0, -5.5 * scale, 0.8 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xffbb00, 0.9);
  context.fill();

  // Bottom arm
  context.fillStyle = armColor;
  context.fillRect(-0.5 * scale, 0.5 * scale, 1 * scale, 2 * scale);
  context.fillRect(-0.5 * scale, 2.5 * scale, 1 * scale, 3 * scale);
  context.fillStyle = gripperColor;
  context.fillRect(-1 * scale, 4.5 * scale, 2 * scale, 1.5 * scale);
  context.beginPath();
  context.arc(0, 5.5 * scale, 0.8 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xffbb00, 0.9);
  context.fill();

  // Left arm
  context.fillStyle = armColor;
  context.fillRect(-2.5 * scale, -0.5 * scale, 2 * scale, 1 * scale);
  context.fillRect(-5.5 * scale, -0.5 * scale, 3 * scale, 1 * scale);
  context.fillStyle = gripperColor;
  context.fillRect(-6 * scale, -1 * scale, 1.5 * scale, 2 * scale);
  context.beginPath();
  context.arc(-5.5 * scale, 0, 0.8 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xffbb00, 0.9);
  context.fill();

  // Right arm
  context.fillStyle = armColor;
  context.fillRect(0.5 * scale, -0.5 * scale, 2 * scale, 1 * scale);
  context.fillRect(2.5 * scale, -0.5 * scale, 3 * scale, 1 * scale);
  context.fillStyle = gripperColor;
  context.fillRect(4.5 * scale, -1 * scale, 1.5 * scale, 2 * scale);
  context.beginPath();
  context.arc(5.5 * scale, 0, 0.8 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xffbb00, 0.9);
  context.fill();

  // Welding torch indicators
  context.fillStyle = hexToCSS(0xff4400, 0.95);
  context.beginPath();
  context.arc(0, -5.5 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fill();
  context.beginPath();
  context.arc(0, 5.5 * scale, 0.4 * scale, 0, Math.PI * 2);
  context.fill();
  context.beginPath();
  context.arc(-5.5 * scale, 0, 0.4 * scale, 0, Math.PI * 2);
  context.fill();
  context.beginPath();
  context.arc(5.5 * scale, 0, 0.4 * scale, 0, Math.PI * 2);
  context.fill();

  // Status lights
  context.beginPath();
  context.arc(-1.2 * scale, 0.8 * scale, 0.3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ff00, 0.9);
  context.fill();
  context.beginPath();
  context.arc(1.2 * scale, 0.8 * scale, 0.3 * scale, 0, Math.PI * 2);
  context.fill();
}

function drawGenericShip(context: CanvasRenderingContext2D, color: number, scale: number, strokeWidth: number): void {
  // Nose cone
  context.beginPath();
  context.moveTo(0, -6 * scale);
  context.lineTo(-1.5 * scale, -3 * scale);
  context.lineTo(-2 * scale, -1 * scale);
  context.lineTo(2 * scale, -1 * scale);
  context.lineTo(1.5 * scale, -3 * scale);
  context.closePath();
  context.fillStyle = lightenColor(hexToCSS(color), 30);
  context.fill();

  // Main fuselage
  context.fillStyle = hexToCSS(color);
  context.fillRect(-2 * scale, -1 * scale, 4 * scale, 6 * scale);

  // Cockpit windows
  context.fillStyle = hexToCSS(0x66aaff, 0.9);
  context.fillRect(-1.3 * scale, -4 * scale, 2.6 * scale, 1.5 * scale);

  // Wings
  context.beginPath();
  context.moveTo(-2 * scale, 1 * scale);
  context.lineTo(-4 * scale, 2 * scale);
  context.lineTo(-4 * scale, 4 * scale);
  context.lineTo(-2 * scale, 3 * scale);
  context.closePath();
  context.fillStyle = darkenColor(hexToCSS(color), 25);
  context.fill();

  context.beginPath();
  context.moveTo(2 * scale, 1 * scale);
  context.lineTo(4 * scale, 2 * scale);
  context.lineTo(4 * scale, 4 * scale);
  context.lineTo(2 * scale, 3 * scale);
  context.closePath();
  context.fill();

  // Engine nacelles
  context.fillStyle = darkenColor(hexToCSS(color), 35);
  context.fillRect(-2.5 * scale, 4 * scale, 1.5 * scale, 2 * scale);
  context.fillRect(1 * scale, 4 * scale, 1.5 * scale, 2 * scale);

  // Engine glow
  context.beginPath();
  context.arc(-1.8 * scale, 5.8 * scale, 0.9 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00bbff, 0.95);
  context.fill();

  context.beginPath();
  context.arc(1.8 * scale, 5.8 * scale, 0.9 * scale, 0, Math.PI * 2);
  context.fill();

  // Navigation lights
  context.beginPath();
  context.arc(-2.2 * scale, 0, 0.3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0xff0000, 0.9);
  context.fill();

  context.beginPath();
  context.arc(2.2 * scale, 0, 0.3 * scale, 0, Math.PI * 2);
  context.fillStyle = hexToCSS(0x00ff00, 0.9);
  context.fill();

  // Center line
  context.beginPath();
  context.moveTo(0, -6 * scale);
  context.lineTo(0, 5 * scale);
  context.strokeStyle = darkenColor(hexToCSS(color, 0.6), 60);
  context.lineWidth = strokeWidth;
  context.stroke();
}
