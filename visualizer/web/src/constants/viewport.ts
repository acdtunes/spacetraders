/**
 * Viewport and camera control constants
 */

export const VIEWPORT_CONSTANTS = {
  // Zoom limits
  MIN_ZOOM: 0.1,
  MAX_ZOOM: 50,
  DEFAULT_ZOOM: 1,
  SHIP_FOCUS_ZOOM: 4, // Comfortable zoom level when centering on a ship

  // Zoom factors for controls
  ZOOM_IN_FACTOR: 1.4,
  ZOOM_OUT_FACTOR: 0.7,

  // Wheel zoom sensitivity
  WHEEL_ZOOM_IN: 1.1,
  WHEEL_ZOOM_OUT: 0.9,

  // Pan clamping
  PAN_CLAMP_PADDING: 100, // World units of padding when clamping

  // Grid
  GRID_TARGET_SPACING: 50, // Target pixels between grid lines on screen
  GRID_LABEL_MULTIPLIER: 2, // Label every N grid lines

  // Animation
  ZOOM_ANIMATION_DURATION: 300, // ms
  PAN_ANIMATION_DURATION: 500, // ms

  // Cluster detection
  CLUSTER_RADIUS: 50, // World units for density calculation
} as const;
