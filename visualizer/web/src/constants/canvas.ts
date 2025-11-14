export const CANVAS_CONSTANTS = {
  // Ship rendering
  SHIP_SCALE: 0.1,
  STROKE_WIDTH: 0.1,

  // Orbital mechanics
  ORBIT_PERIOD: 15000, // milliseconds
  ORBIT_DISTANCE_DEFAULT: 3.2,
  ORBIT_DISTANCE_ASTEROID: 1.6,
  ORBIT_DISTANCE_PLANET: 2.4,
  ORBIT_DISTANCE_GAS_GIANT: 4.8,
  ORBIT_DISTANCE_MOON: 0.8,
  ORBIT_DISTANCE_STATION: 1.6,

  // Zoom limits
  MIN_ZOOM_SPACE: 0.1,
  MAX_ZOOM_SPACE: 10,
  MIN_ZOOM_GALAXY: 0.05,
  MAX_ZOOM_GALAXY: 5,

  // Zoom factors
  ZOOM_IN_FACTOR: 1.1,
  ZOOM_OUT_FACTOR: 0.9,

  // Overlap handling
  MAX_OVERLAPS: 8,
  OVERLAP_OFFSET_BASE: 15,
} as const;

export const WAYPOINT_COLORS: Record<string, number> = {
  PLANET: 0x4a90e2,
  GAS_GIANT: 0xe67e22,
  MOON: 0x95a5a6,
  ORBITAL_STATION: 0x3498db,
  ASTEROID_FIELD: 0x7f8c8d,
  ASTEROID: 0x7f8c8d,
  JUMP_GATE: 0x9b59b6,
  NEBULA: 0xe74c3c,
  FUEL_STATION: 0xf39c12,
};

export const DEFAULT_WAYPOINT_COLOR = 0xffffff;

export const ENGINE_GLOW_COLOR = 0x00d4ff;
export const WINDOW_LIGHT_COLOR = 0xffff00;
