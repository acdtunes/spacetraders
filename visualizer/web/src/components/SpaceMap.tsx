import { useEffect, useRef, useState, forwardRef, useImperativeHandle, useMemo } from 'react';
import { Stage, Layer, Shape, Group, Circle, Text, Line } from 'react-konva';
import Konva from 'konva';
import { useStore } from '../store/useStore';
import { getWaypoints } from '../services/api';
import { getWaypointOpportunities, formatOpportunity } from '../services/marketAnalysis';
import { Ship, Waypoint, ShipQueries, WaypointQueries, ViewportBounds } from '../domain';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';
import { drawWaypoint } from '../services/canvas/WaypointRenderer';
import { drawShipShape } from '../services/canvas/ShipRenderer';
import { getCargoIcon, getCargoLabel } from '../utils/cargo';
import ZoomControls from './ZoomControls';
import Minimap from './Minimap';
import type { FlightMode, ShipTrailPoint } from '../types/spacetraders';

type TrailVisualSettings = {
  maxAgeMs: number;
  baseWidth: number;
  baseAlpha: number;
  tailAlpha: number;
  glowBlur: number;
  glowAlpha: number;
  particleDensity: number;
  particleSize: [number, number];
  particleAlpha: number;
  colorBoost: number;
};

const TRAIL_VISUAL_CONFIG: Record<FlightMode, TrailVisualSettings> = {
  DRIFT: {
    maxAgeMs: 0,
    baseWidth: 0,
    baseAlpha: 0,
    tailAlpha: 0,
    glowBlur: 0,
    glowAlpha: 0,
    particleDensity: 0,
    particleSize: [0, 0],
    particleAlpha: 0,
    colorBoost: 0,
  },
  CRUISE: {
    maxAgeMs: 7000,
    baseWidth: 1.4,
    baseAlpha: 0.35,
    tailAlpha: 0.08,
    glowBlur: 6,
    glowAlpha: 0.35,
    particleDensity: 0.25,
    particleSize: [0.7, 1.4],
    particleAlpha: 0.3,
    colorBoost: 0.2,
  },
  BURN: {
    maxAgeMs: 12000,
    baseWidth: 2.5,
    baseAlpha: 0.55,
    tailAlpha: 0.15,
    glowBlur: 12,
    glowAlpha: 0.65,
    particleDensity: 0.6,
    particleSize: [1.2, 2.6],
    particleAlpha: 0.5,
    colorBoost: 0.45,
  },
  STEALTH: {
    maxAgeMs: 0,
    baseWidth: 0,
    baseAlpha: 0,
    tailAlpha: 0,
    glowBlur: 0,
    glowAlpha: 0,
    particleDensity: 0,
    particleSize: [0, 0],
    particleAlpha: 0,
    colorBoost: 0,
  },
};

const TRAIL_SAMPLE_RATE = 4;

type ShipLabelData = {
  key: string;
  text: string;
  left: number;
  top: number;
};

const SHIP_LABEL_FONT_SIZE = 6;
const SHIP_LABEL_PADDING_X = 3;
const SHIP_LABEL_PADDING_Y = 2;
const SHIP_LABEL_HORIZONTAL_OFFSET = 8;
const SHIP_LABEL_VERTICAL_OFFSET = 6;

type RGB = { r: number; g: number; b: number };

const hexToRgb = (hex: string): RGB => {
  const normalized = hex.replace('#', '');
  const parsed = normalized.length === 3
    ? normalized
        .split('')
        .map((char) => char + char)
        .join('')
    : normalized;

  const value = Number.parseInt(parsed, 16);
  if (Number.isNaN(value)) {
    return { r: 255, g: 107, b: 107 };
  }

  return {
    r: (value >> 16) & 255,
    g: (value >> 8) & 255,
    b: value & 255,
  };
};

const boostColor = (rgb: RGB, amount: number): RGB => ({
  r: Math.min(255, rgb.r + (255 - rgb.r) * amount),
  g: Math.min(255, rgb.g + (255 - rgb.g) * amount),
  b: Math.min(255, rgb.b + (255 - rgb.b) * amount),
});

const rgba = (rgb: RGB, alpha: number): string => `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, ${alpha})`;

export interface SpaceMapRef {
  zoomIn: () => void;
  zoomOut: () => void;
  resetView: () => void;
  fitView: () => void;
  focusOn: (x: number, y: number, scale?: number) => void;
}

const SpaceMap = forwardRef<SpaceMapRef>((_props, ref) => {
  const stageRef = useRef<Konva.Stage | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const layerRef = useRef<Konva.Layer | null>(null);
  const waypointsSizeRef = useRef<number>(0);

  const { currentSystem, waypoints, ships, markets, showMarkets, setWaypoints, trails, addTrailPosition, clearTrail, filterStatus, filterAgents, filterWaypointTypes, setSelectedShip, setSelectedWaypoint } =
    useStore();

  const [hoveredWaypoint, setHoveredWaypoint] = useState<string | null>(null);
  const [hoveredShip, setHoveredShip] = useState<string | null>(null);
  const [mousePosition, setMousePosition] = useState<{ x: number; y: number }>({ x: 0, y: 0 });
  const [selectedObject, setSelectedObject] = useState<{ type: 'waypoint' | 'ship', symbol: string, x: number, y: number } | null>(null);
  const [viewportBounds, setViewportBounds] = useState({ x: 0, y: 0, width: 0, height: 0, scale: 1 });
  const [animationFrame, setAnimationFrame] = useState(0);
  const animationRef = useRef<Konva.Animation | null>(null);

  const [stageSize, setStageSize] = useState({ width: 0, height: 0 });

  const currentScale = viewportBounds.scale || 1;

  // Track canvas size with ResizeObserver so it reacts to layout changes (e.g. sidebar toggles)
  useEffect(() => {
    const container = containerRef.current;
    if (!container || typeof ResizeObserver === 'undefined') return;

    const updateSize = (width: number, height: number) => {
      setStageSize((prev) => {
        if (prev.width === width && prev.height === height) return prev;
        return { width, height };
      });
    };

    // Initialise with current size before observing for future changes
    const rect = container.getBoundingClientRect();
    updateSize(rect.width, rect.height);

    const observer = new ResizeObserver((entries) => {
      entries.forEach((entry) => {
        const { width, height } = entry.contentRect;
        updateSize(width, height);
      });
    });

    observer.observe(container);

    return () => observer.disconnect();
  }, []);

  // Update viewport bounds for minimap
  const updateViewportBounds = () => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    const screenCenterX = stage.width() / 2;
    const screenCenterY = stage.height() / 2;
    const worldX = (screenCenterX - layer.x()) / layer.scaleX();
    const worldY = (screenCenterY - layer.y()) / layer.scaleY();

    setViewportBounds({
      x: worldX,
      y: worldY,
      width: stage.width(),
      height: stage.height(),
      scale: layer.scaleX(),
    });
  };

  // Handle drag end with viewport clamping
  const handleDragEnd = () => {
    if (!layerRef.current || !stageRef.current || waypoints.size === 0) {
      updateViewportBounds();
      return;
    }

    const layer = layerRef.current;
    const stage = stageRef.current;

    // Calculate world bounds from waypoints
    const worldBounds = WaypointQueries.calculateBounds(Array.from(waypoints.values()));

    // Get current viewport
    const screenCenterX = stage.width() / 2;
    const screenCenterY = stage.height() / 2;
    const worldX = (screenCenterX - layer.x()) / layer.scaleX();
    const worldY = (screenCenterY - layer.y()) / layer.scaleY();

    const currentViewport = new ViewportBounds(
      worldX,
      worldY,
      stage.width(),
      stage.height(),
      layer.scaleX()
    );

    // Clamp viewport to world bounds
    const clampedViewport = ViewportBounds.clampViewport(currentViewport, worldBounds);

    // Apply clamped viewport if it changed
    if (clampedViewport.x !== currentViewport.x || clampedViewport.y !== currentViewport.y) {
      layer.x(stage.width() / 2 - clampedViewport.x * clampedViewport.scale);
      layer.y(stage.height() / 2 - clampedViewport.y * clampedViewport.scale);
    }

    updateViewportBounds();
  };

  // Handle minimap navigation
  const handleMinimapNavigate = (worldX: number, worldY: number) => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    layer.x(stage.width() / 2 - worldX * layer.scaleX());
    layer.y(stage.height() / 2 - worldY * layer.scaleY());

    updateViewportBounds();
  };

  // Calculate the center of the densest waypoint cluster
  const calculateClusterCenter = (): { x: number; y: number } => {
    if (waypoints.size === 0) return { x: 0, y: 0 };

    const waypointArray = Array.from(waypoints.values());
    const CLUSTER_RADIUS = 50;
    let maxDensity = 0;
    let clusterCenter = { x: 0, y: 0 };

    waypointArray.forEach(waypoint => {
      const neighbors = waypointArray.filter(w => {
        const dx = w.x - waypoint.x;
        const dy = w.y - waypoint.y;
        const distance = Math.sqrt(dx * dx + dy * dy);
        return distance <= CLUSTER_RADIUS;
      });

      if (neighbors.length > maxDensity) {
        maxDensity = neighbors.length;
        const centerX = neighbors.reduce((sum, w) => sum + w.x, 0) / neighbors.length;
        const centerY = neighbors.reduce((sum, w) => sum + w.y, 0) / neighbors.length;
        clusterCenter = { x: centerX, y: centerY };
      }
    });

    return clusterCenter;
  };

  // Zoom control functions with animation
  const handleZoomIn = () => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    // Calculate current viewport center from layer position (not from stale state!)
    const currentScale = layer.scaleX();
    const currentCenterX = (stage.width() / 2 - layer.x()) / currentScale;
    const currentCenterY = (stage.height() / 2 - layer.y()) / currentScale;

    const currentViewport = new ViewportBounds(
      currentCenterX,
      currentCenterY,
      stage.width(),
      stage.height(),
      currentScale
    );

    const newViewport = ViewportBounds.zoomAtCenter(
      currentViewport,
      VIEWPORT_CONSTANTS.ZOOM_IN_FACTOR
    );

    layer.to({
      scaleX: newViewport.scale,
      scaleY: newViewport.scale,
      x: stage.width() / 2 - newViewport.x * newViewport.scale,
      y: stage.height() / 2 - newViewport.y * newViewport.scale,
      duration: VIEWPORT_CONSTANTS.ZOOM_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseOut,
      onFinish: updateViewportBounds,
    });
  };

  const handleZoomOut = () => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    // Calculate current viewport center from layer position (not from stale state!)
    const currentScale = layer.scaleX();
    const currentCenterX = (stage.width() / 2 - layer.x()) / currentScale;
    const currentCenterY = (stage.height() / 2 - layer.y()) / currentScale;

    const currentViewport = new ViewportBounds(
      currentCenterX,
      currentCenterY,
      stage.width(),
      stage.height(),
      currentScale
    );

    const newViewport = ViewportBounds.zoomAtCenter(
      currentViewport,
      VIEWPORT_CONSTANTS.ZOOM_OUT_FACTOR
    );

    layer.to({
      scaleX: newViewport.scale,
      scaleY: newViewport.scale,
      x: stage.width() / 2 - newViewport.x * newViewport.scale,
      y: stage.height() / 2 - newViewport.y * newViewport.scale,
      duration: VIEWPORT_CONSTANTS.ZOOM_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseOut,
      onFinish: updateViewportBounds,
    });
  };

  const handleResetView = () => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    const clusterCenter = calculateClusterCenter();
    layer.to({
      x: stage.width() / 2 - clusterCenter.x,
      y: stage.height() / 2 - clusterCenter.y,
      scaleX: 1,
      scaleY: 1,
      duration: VIEWPORT_CONSTANTS.PAN_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseInOut,
      onFinish: updateViewportBounds,
    });
  };

  const handleFitView = () => {
    if (!layerRef.current || !stageRef.current || waypoints.size === 0) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    waypoints.forEach(waypoint => {
      minX = Math.min(minX, waypoint.x);
      maxX = Math.max(maxX, waypoint.x);
      minY = Math.min(minY, waypoint.y);
      maxY = Math.max(maxY, waypoint.y);
    });

    const centerX = (minX + maxX) / 2;
    const centerY = (minY + maxY) / 2;
    const width = maxX - minX;
    const height = maxY - minY;

    const padding = 50;
    const scaleX = (stage.width() - padding * 2) / width;
    const scaleY = (stage.height() - padding * 2) / height;
    const scale = Math.min(scaleX, scaleY, 2);

    layer.to({
      scaleX: scale,
      scaleY: scale,
      x: stage.width() / 2 - centerX * scale,
      y: stage.height() / 2 - centerY * scale,
      duration: VIEWPORT_CONSTANTS.PAN_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseInOut,
      onFinish: updateViewportBounds,
    });
  };

  const handleFocusOn = (x: number, y: number, scale?: number) => {
    if (!layerRef.current || !stageRef.current) return;
    const layer = layerRef.current;
    const stage = stageRef.current;

    const targetScale = scale ?? layer.scaleX();
    layer.to({
      scaleX: targetScale,
      scaleY: targetScale,
      x: stage.width() / 2 - x * targetScale,
      y: stage.height() / 2 - y * targetScale,
      duration: VIEWPORT_CONSTANTS.PAN_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseInOut,
      onFinish: updateViewportBounds,
    });
  };

  // Expose zoom functions via ref
  useImperativeHandle(ref, () => ({
    zoomIn: handleZoomIn,
    zoomOut: handleZoomOut,
    resetView: handleResetView,
    fitView: handleFitView,
    focusOn: handleFocusOn,
  }));

  // Keyboard handler for navigation and deselection
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Deselection
      if (e.key === 'Escape' && selectedObject) {
        setSelectedObject(null);
        setSelectedShip(null);
        setSelectedWaypoint(null);
        return;
      }

      // Ignore keyboard shortcuts if user is typing in an input
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        return;
      }

      // Zoom shortcuts
      if (e.key === '=' || e.key === '+') {
        e.preventDefault();
        handleZoomIn();
      } else if (e.key === '-' || e.key === '_') {
        e.preventDefault();
        handleZoomOut();
      } else if (e.key === '0') {
        e.preventDefault();
        handleResetView();
      } else if (e.key === 'f' || e.key === 'F') {
        e.preventDefault();
        handleFitView();
      }
      // Pan shortcuts (arrow keys)
      else if (e.key === 'ArrowUp' || e.key === 'ArrowDown' || e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault();
        if (!layerRef.current || !stageRef.current) return;

        const layer = layerRef.current;
        const panDistance = 50; // pixels

        let dx = 0;
        let dy = 0;

        if (e.key === 'ArrowUp') dy = panDistance;
        if (e.key === 'ArrowDown') dy = -panDistance;
        if (e.key === 'ArrowLeft') dx = panDistance;
        if (e.key === 'ArrowRight') dx = -panDistance;

        layer.to({
          x: layer.x() + dx,
          y: layer.y() + dy,
          duration: 0.2,
          easing: Konva.Easings.EaseOut,
          onFinish: updateViewportBounds,
        });
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [selectedObject, setSelectedShip, setSelectedWaypoint]);

  // Load waypoints when system changes
  useEffect(() => {
    if (!currentSystem) return;

    getWaypoints(currentSystem)
      .then((data) => {
        setWaypoints(data);
      })
      .catch((error) => {
        console.error('Failed to load waypoints:', error);
      });
  }, [currentSystem, setWaypoints]);

  // Center view when waypoints load
  useEffect(() => {
    const waypointsSizeChanged = waypointsSizeRef.current !== waypoints.size;
    if (waypointsSizeChanged && waypoints.size > 0 && stageRef.current && layerRef.current) {
      const layer = layerRef.current;
      const stage = stageRef.current;
      const clusterCenter = calculateClusterCenter();

      layer.x(stage.width() / 2 - clusterCenter.x);
      layer.y(stage.height() / 2 - clusterCenter.y);
      layer.scale({ x: 1, y: 1 });

      waypointsSizeRef.current = waypoints.size;
    }
  }, [waypoints]);

  // Handle mouse move for tooltips
  const handleStageMouseMove = (e: any) => {
    const stage = e.target.getStage();
    const pointerPosition = stage.getPointerPosition();
    if (pointerPosition) {
      setMousePosition({ x: pointerPosition.x, y: pointerPosition.y });
    }
  };

  const applyZoomAtWorldPoint = (worldX: number, worldY: number, zoomFactor: number) => {
    if (!layerRef.current || !stageRef.current) return;

    const layer = layerRef.current;
    const stage = stageRef.current;

    const currentScale = layer.scaleX();
    const currentCenterX = (stage.width() / 2 - layer.x()) / currentScale;
    const currentCenterY = (stage.height() / 2 - layer.y()) / currentScale;

    const currentViewport = new ViewportBounds(
      currentCenterX,
      currentCenterY,
      stage.width(),
      stage.height(),
      currentScale
    );

    const newViewport = ViewportBounds.zoomAtPointer(
      currentViewport,
      worldX,
      worldY,
      zoomFactor
    );

    layer.scale({ x: newViewport.scale, y: newViewport.scale });
    layer.x(stage.width() / 2 - newViewport.x * newViewport.scale);
    layer.y(stage.height() / 2 - newViewport.y * newViewport.scale);

    updateViewportBounds();
  };

  // Handle wheel zoom (pointer-relative)
  const handleWheel = (e: any) => {
    e.evt.preventDefault();
    if (!layerRef.current || !stageRef.current) return;

    const layer = layerRef.current;
    const stage = stageRef.current;
    const pointer = stage.getPointerPosition();
    if (!pointer) return;

    const currentScale = layer.scaleX();
    const currentCenterX = (stage.width() / 2 - layer.x()) / currentScale;
    const currentCenterY = (stage.height() / 2 - layer.y()) / currentScale;

    const currentViewport = new ViewportBounds(
      currentCenterX,
      currentCenterY,
      stage.width(),
      stage.height(),
      currentScale
    );

    const worldPos = currentViewport.screenToWorld(pointer.x, pointer.y, stage);
    const zoomFactor = e.evt.deltaY > 0 ? VIEWPORT_CONSTANTS.WHEEL_ZOOM_OUT : VIEWPORT_CONSTANTS.WHEEL_ZOOM_IN;

    applyZoomAtWorldPoint(worldPos.x, worldPos.y, zoomFactor);
  };

  const handleMinimapZoom = (worldX: number, worldY: number, zoomFactor: number) => {
    applyZoomAtWorldPoint(worldX, worldY, zoomFactor);
  };

  // Handle double-click zoom
  const handleDoubleClick = () => {
    if (!layerRef.current || !stageRef.current) return;

    const layer = layerRef.current;
    const stage = stageRef.current;
    const pointer = stage.getPointerPosition();
    if (!pointer) return;

    const currentScale = layer.scaleX();
    const currentCenterX = (stage.width() / 2 - layer.x()) / currentScale;
    const currentCenterY = (stage.height() / 2 - layer.y()) / currentScale;

    const currentViewport = new ViewportBounds(
      currentCenterX,
      currentCenterY,
      stage.width(),
      stage.height(),
      currentScale
    );

    const worldPos = currentViewport.screenToWorld(pointer.x, pointer.y, stage);

    const newViewport = ViewportBounds.zoomAtPointer(
      currentViewport,
      worldPos.x,
      worldPos.y,
      VIEWPORT_CONSTANTS.ZOOM_IN_FACTOR
    );

    // Apply with animation
    layer.to({
      scaleX: newViewport.scale,
      scaleY: newViewport.scale,
      x: stage.width() / 2 - newViewport.x * newViewport.scale,
      y: stage.height() / 2 - newViewport.y * newViewport.scale,
      duration: VIEWPORT_CONSTANTS.ZOOM_ANIMATION_DURATION / 1000,
      easing: Konva.Easings.EaseOut,
      onFinish: updateViewportBounds,
    });
  };

  // Start animation loop for ships
  useEffect(() => {
    if (!layerRef.current) return;

    const layer = layerRef.current;
    const anim = new Konva.Animation(() => {
      setAnimationFrame(prev => prev + 1);
    }, layer);

    anim.start();
    animationRef.current = anim;

    return () => {
      anim.stop();
      animationRef.current = null;
    };
  }, []);

  // Sample ship positions for trails based on flight mode
  useEffect(() => {
    if (ships.length === 0) return;
    if (animationFrame % TRAIL_SAMPLE_RATE !== 0) return;

    const timestamp = Date.now();

    ships.forEach((ship: any) => {
      if (currentSystem && ship.nav.systemSymbol !== currentSystem) {
        if (ship.nav.route?.destination?.systemSymbol !== currentSystem) {
          clearTrail(ship.symbol);
          return;
        }
      }

      if (ship.nav.status !== 'IN_TRANSIT') {
        clearTrail(ship.symbol);
        return;
      }

      const flightMode: FlightMode = ship.nav.flightMode;
      if (flightMode === 'DRIFT' || flightMode === 'STEALTH') {
        clearTrail(ship.symbol);
        return;
      }

      const position = Ship.getPosition(ship, waypoints);
      if (position.x === 0 && position.y === 0) return;

      addTrailPosition(ship.symbol, {
        shipSymbol: ship.symbol,
        x: position.x,
        y: position.y,
        timestamp,
        flightMode,
      });
    });
  }, [animationFrame, ships, waypoints, currentSystem, addTrailPosition, clearTrail]);

  // Filter ships using domain queries
  const filteredShips = useMemo(() => {
    return ShipQueries.filter(ships, {
      systemSymbol: currentSystem ?? undefined,
      statuses: filterStatus,
      hiddenAgentIds: filterAgents,
    });
  }, [ships, currentSystem, filterStatus, filterAgents]);

  // Filter waypoints using domain queries
  const filteredWaypoints = useMemo(() => {
    return WaypointQueries.filterByType(
      Array.from(waypoints.values()),
      filterWaypointTypes
    );
  }, [waypoints, filterWaypointTypes]);

  // Grid rendering with dynamic spacing
  const gridLines = useMemo(() => {
    if (waypoints.size === 0) return { vertical: [], horizontal: [], labels: [] };

    const waypointArray = Array.from(waypoints.values());
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    waypointArray.forEach(wp => {
      minX = Math.min(minX, wp.x);
      maxX = Math.max(maxX, wp.x);
      minY = Math.min(minY, wp.y);
      maxY = Math.max(maxY, wp.y);
    });

    // Calculate dynamic grid spacing based on zoom level
    // Target: ~50px spacing on screen
    const currentScale = viewportBounds.scale || 1;
    const targetSpacing = VIEWPORT_CONSTANTS.GRID_TARGET_SPACING;
    const worldSpacing = targetSpacing / currentScale;

    // Round to nice numbers (powers of 10 or 5)
    const magnitude = Math.pow(10, Math.floor(Math.log10(worldSpacing)));
    let gridSpacing = magnitude;

    if (worldSpacing / magnitude >= 5) {
      gridSpacing = magnitude * 5;
    } else if (worldSpacing / magnitude >= 2) {
      gridSpacing = magnitude * 2;
    }

    // Calculate label multiplier (show labels every N grid lines)
    const labelMultiplier = VIEWPORT_CONSTANTS.GRID_LABEL_MULTIPLIER;
    const labelSpacing = gridSpacing * labelMultiplier;

    const padding = gridSpacing * 2;
    minX = Math.floor((minX - padding) / gridSpacing) * gridSpacing;
    maxX = Math.ceil((maxX + padding) / gridSpacing) * gridSpacing;
    minY = Math.floor((minY - padding) / gridSpacing) * gridSpacing;
    maxY = Math.ceil((maxY + padding) / gridSpacing) * gridSpacing;

    const vertical = [];
    const horizontal = [];
    const labels = [];

    for (let x = minX; x <= maxX; x += gridSpacing) {
      vertical.push({
        points: [x, minY, x, maxY],
        stroke: x === 0 ? '#444444' : '#222222',
        strokeWidth: x === 0 ? 1.5 : 0.5,
        opacity: x === 0 ? 0.5 : 0.2,
      });
      if (x % labelSpacing === 0) {
        labels.push({ text: x.toString(), x: x + 2, y: 5 });
      }
    }

    for (let y = minY; y <= maxY; y += gridSpacing) {
      horizontal.push({
        points: [minX, y, maxX, y],
        stroke: y === 0 ? '#444444' : '#222222',
        strokeWidth: y === 0 ? 1.5 : 0.5,
        opacity: y === 0 ? 0.5 : 0.2,
      });
      if (y % labelSpacing === 0 && y !== 0) {
        labels.push({ text: y.toString(), x: 5, y: y + 2 });
      }
    }

    return { vertical, horizontal, labels };
  }, [waypoints, viewportBounds.scale]);

  // Get waypoint tooltip data
  const activeShipTooltipSymbol = hoveredShip ?? (selectedObject?.type === 'ship' ? selectedObject.symbol : null);

  const shipTooltip = activeShipTooltipSymbol ? (() => {
    const ship = ships.find((s) => s.symbol === activeShipTooltipSymbol);
    if (!ship) return null;

    const statusText = ship.nav.status.replace(/_/g, ' ');
    const flightMode = ship.nav.flightMode;
    const location = ship.nav.waypointSymbol.split('-').pop() ?? ship.nav.waypointSymbol;

    let routeSummary: string | null = null;
    let etaText: string | null = null;
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route) {
      const origin = ship.nav.route.origin.symbol.split('-').pop() ?? ship.nav.route.origin.symbol;
      const destination = ship.nav.route.destination.symbol.split('-').pop() ?? ship.nav.route.destination.symbol;
      routeSummary = `${origin}→${destination}`;

      const arrivalTime = new Date(ship.nav.route.arrival).getTime();
      const now = Date.now();
      const remainingMs = Math.max(0, arrivalTime - now);
      const totalSeconds = Math.floor(remainingMs / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      const pad = (value: number) => value.toString().padStart(2, '0');
      etaText = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
    }

    const fuelPercent = ship.fuel.capacity > 0
      ? Math.round((ship.fuel.current / ship.fuel.capacity) * 100)
      : 0;

    const cargoEntries = ship.cargo.inventory.slice(0, 4).map((item) => ({
      icon: getCargoIcon(item.symbol),
      label: getCargoLabel(item.symbol),
      units: item.units,
    }));
    const extraCargoCount = Math.max(0, ship.cargo.inventory.length - cargoEntries.length);

    const cooldownSeconds = ship.cooldown && ship.cooldown.remainingSeconds > 0
      ? ship.cooldown.remainingSeconds
      : null;

    return {
      symbol: ship.symbol,
      registrationName: ship.registration.name,
      role: ship.registration.role,
      statusText,
      flightMode,
      location,
      routeSummary,
      etaText,
      fuelCurrent: ship.fuel.current,
      fuelCapacity: ship.fuel.capacity,
      fuelPercent,
      cargoUnits: ship.cargo.units,
      cargoCapacity: ship.cargo.capacity,
      cargoEntries,
      extraCargoCount,
      cooldownSeconds,
    };
  })() : null;

  const shipTooltipPosition = useMemo(() => {
    if (!activeShipTooltipSymbol) return null;
    const layer = layerRef.current;
    if (!layer) return null;

    const ship = ships.find((s) => s.symbol === activeShipTooltipSymbol);
    if (!ship) return null;

    const position = Ship.getPosition(ship, waypoints);
    if (position.x === 0 && position.y === 0) return null;

    const transform = layer.getAbsoluteTransform().copy();
    const screenPos = transform.point({ x: position.x, y: position.y });

    return {
      left: screenPos.x - SHIP_LABEL_HORIZONTAL_OFFSET,
      top: screenPos.y - SHIP_LABEL_VERTICAL_OFFSET,
    };
  }, [activeShipTooltipSymbol, ships, waypoints, animationFrame]);

  const selectionOverlay = useMemo(() => {
    if (!selectedObject) return null;
    const layer = layerRef.current;
    if (!layer) return null;

    const transform = layer.getAbsoluteTransform().copy();

    let worldX = selectedObject.x;
    let worldY = selectedObject.y;

    if (selectedObject.type === 'ship') {
      const ship = ships.find((s) => s.symbol === selectedObject.symbol);
      if (!ship) return null;
      const position = Ship.getPosition(ship, waypoints);
      if (position.x === 0 && position.y === 0) return null;
      worldX = position.x;
      worldY = position.y;
    } else if (selectedObject.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (!waypoint) return null;
      worldX = waypoint.x;
      worldY = waypoint.y;
    }

    const screenPos = transform.point({ x: worldX, y: worldY });
    const size = selectedObject.type === 'waypoint' ? 15 : 12;

    return {
      left: screenPos.x,
      top: screenPos.y,
      size,
    };
  }, [selectedObject, ships, waypoints, animationFrame]);

  const shipLabelData = useMemo<ShipLabelData[]>(() => {
    const layer = layerRef.current;
    if (!layer) return [];

    const transform = layer.getAbsoluteTransform().copy();

    return filteredShips.flatMap<ShipLabelData>((ship: any) => {
      const position = Ship.getPosition(ship, waypoints);
      if (position.x === 0 && position.y === 0) return [];

      const screenPos = transform.point({ x: position.x, y: position.y });

      const shipNumber = ship.symbol.split('-').pop() ?? ship.symbol;
      const shipTypeRaw = ship.registration.role || 'UNKNOWN';
      const shipType = shipTypeRaw
        .split('_')
        .map((part: string) => part.charAt(0) + part.slice(1).toLowerCase())
        .join(' ');

      const labelHeight = SHIP_LABEL_FONT_SIZE + SHIP_LABEL_PADDING_Y * 2;

      return [{
        key: ship.symbol,
        text: `${shipType} ${shipNumber}`,
        left: screenPos.x + SHIP_LABEL_HORIZONTAL_OFFSET,
        top: screenPos.y - (labelHeight + SHIP_LABEL_VERTICAL_OFFSET),
      }];
    });
  }, [filteredShips, waypoints, animationFrame]);

  const waypointTooltip = hoveredWaypoint ? (() => {
    const waypoint = waypoints.get(hoveredWaypoint);
    if (!waypoint) return null;

    const market = markets.get(hoveredWaypoint);
    const hasMarketplace = waypoint.traits.some((t) => t.symbol === 'MARKETPLACE');

    let marketData = null;
    if (market && hasMarketplace && showMarkets) {
      const opportunities = getWaypointOpportunities(hoveredWaypoint, markets, 2);
      marketData = {
        importsCount: market.imports.length,
        exportsCount: market.exports.length,
        opportunities: opportunities.map(formatOpportunity),
      };
    }

    return {
      symbol: hoveredWaypoint,
      type: waypoint.type,
      x: waypoint.x,
      y: waypoint.y,
      traits: waypoint.traits,
      faction: waypoint.faction,
      hasMarketplace,
      marketData,
    };
  })() : null;

  return (
    <div ref={containerRef} className="relative w-full h-full">
      {stageSize.width > 0 && stageSize.height > 0 && (
        <Stage
          ref={stageRef}
          width={stageSize.width}
          height={stageSize.height}
          draggable
          onWheel={handleWheel}
          onMouseMove={handleStageMouseMove}
          onMouseLeave={() => {
            setHoveredShip(null);
            setHoveredWaypoint(null);
          }}
          onDragMove={updateViewportBounds}
          onDragEnd={handleDragEnd}
          onDblClick={handleDoubleClick}
        >
          <Layer ref={layerRef}>
            {/* Grid lines */}
            {gridLines.vertical.map((line, i) => (
              <Line
                key={`v-${i}`}
                points={line.points}
                stroke={line.stroke}
                strokeWidth={line.strokeWidth / currentScale}
                opacity={line.opacity}
                listening={false}
              />
            ))}
            {gridLines.horizontal.map((line, i) => (
              <Line
                key={`h-${i}`}
                points={line.points}
                stroke={line.stroke}
                strokeWidth={line.strokeWidth / currentScale}
                opacity={line.opacity}
                listening={false}
              />
            ))}
            {gridLines.labels.map((label, i) => (
              <Text
                key={`label-${i}`}
                text={label.text}
                x={label.x}
                y={label.y}
                fontSize={8 / currentScale}
                fill="#666666"
                opacity={0.6}
                listening={false}
              />
            ))}

          {/* Waypoints */}
          {filteredWaypoints.map(waypoint => {
            const radius = Waypoint.getRadius(waypoint);
            const hasMarketplace = waypoint.traits.some((t) => t.symbol === 'MARKETPLACE');

            // Handle overlapping waypoints
            const overlapIndex = filteredWaypoints.filter(w =>
              w.x === waypoint.x && w.y === waypoint.y && w.symbol <= waypoint.symbol
            ).length - 1;

            let x = waypoint.x;
            let y = waypoint.y;
            if (overlapIndex > 0) {
              const angle = (overlapIndex * Math.PI * 2) / 8;
              const offset = 15 * overlapIndex;
              x += Math.cos(angle) * offset;
              y += Math.sin(angle) * offset;
            }

            return (
              <Group key={waypoint.symbol}>
                {/* Marketplace ring */}
                {hasMarketplace && showMarkets && (
                  <Circle
                    x={x}
                    y={y}
                    radius={radius + 4}
                    stroke="#f39c12"
                    strokeWidth={1 / currentScale}
                    opacity={0.6}
                    listening={false}
                  />
                )}

                {/* Waypoint shape */}
                <Shape
                  sceneFunc={(context, _shape) => {
                    drawWaypoint(context._context as CanvasRenderingContext2D, waypoint, x, y, radius);
                  }}
                  onMouseEnter={() => setHoveredWaypoint(waypoint.symbol)}
                  onMouseLeave={() => setHoveredWaypoint(null)}
                  onClick={() => {
                    setSelectedObject({ type: 'waypoint', symbol: waypoint.symbol, x: waypoint.x, y: waypoint.y });
                    setSelectedWaypoint(waypoint);
                    setSelectedShip(null);
                  }}
                  hitStrokeWidth={radius + 20}
                />

                {/* Marketplace $ symbol */}
                {hasMarketplace && showMarkets && (
                  <Text
                    text="$"
                    x={x + radius * 0.7 - 3}
                    y={y - radius * 0.7 - 4}
                    fontSize={8 / currentScale}
                    fill="#f39c12"
                    listening={false}
                  />
                )}

                {/* Waypoint label */}
                <Text
                  text={waypoint.symbol.split('-').pop() || ''}
                  x={x + radius + 2}
                  y={y - 5}
                  fontSize={10 / currentScale}
                  fill="white"
                  opacity={0.6}
                  listening={false}
                />
              </Group>
            );
          })}

          {/* Ship trails */}
          {filteredShips.map((ship: any) => {
            const trail = trails.get(ship.symbol) as ShipTrailPoint[] | undefined;
            if (!trail || trail.length < 2) return null;

            const now = Date.now();
            const activeTrail = trail.filter((point) => {
              const config = TRAIL_VISUAL_CONFIG[point.flightMode];
              return config.maxAgeMs > 0 && now - point.timestamp <= config.maxAgeMs;
            });

            if (activeTrail.length < 2) return null;

            const latestMode = activeTrail[activeTrail.length - 1].flightMode;
            const config = TRAIL_VISUAL_CONFIG[latestMode] ?? TRAIL_VISUAL_CONFIG.CRUISE;
            if (config.maxAgeMs === 0) return null;

            const baseColor = hexToRgb(Ship.getDisplayColor(ship));
            const boostedColor = boostColor(baseColor, config.colorBoost);
            const sparkColor = boostColor(boostedColor, 0.25);

            return (
              <Shape
                key={`trail-${ship.symbol}`}
                sceneFunc={(context, _shape) => {
                  const ctx = context._context as CanvasRenderingContext2D;
                  ctx.save();
                  ctx.lineCap = 'round';
                  ctx.lineJoin = 'round';

                  for (let i = 0; i < activeTrail.length - 1; i++) {
                    const start = activeTrail[i];
                    const end = activeTrail[i + 1];
                    const progress = (i + 1) / activeTrail.length;
                    const alpha = config.tailAlpha + (config.baseAlpha - config.tailAlpha) * progress;

                    ctx.shadowColor = rgba(boostedColor, config.glowAlpha * progress);
                    ctx.shadowBlur = config.glowBlur * progress;
                    ctx.lineWidth = config.baseWidth * (0.6 + progress * 0.4);
                    ctx.strokeStyle = rgba(boostedColor, alpha);
                    ctx.beginPath();
                    ctx.moveTo(start.x, start.y);
                    ctx.lineTo(end.x, end.y);
                    ctx.stroke();
                  }

                  ctx.shadowBlur = 0;

                  if (config.particleDensity > 0) {
                    const segmentCount = activeTrail.length - 1;
                    const particleCount = Math.max(1, Math.floor(segmentCount * config.particleDensity));
                    for (let p = 0; p < particleCount; p++) {
                      const index = Math.max(1, segmentCount - Math.floor((p / particleCount) * segmentCount));
                      const head = activeTrail[index];
                      const tail = activeTrail[index - 1];
                      const t = ((animationFrame * 0.08 + p * 0.37) % 1 + 1) % 1;
                      const x = head.x + (tail.x - head.x) * t;
                      const y = head.y + (tail.y - head.y) * t;
                      const oscillation = (Math.sin(animationFrame * 0.15 + p) + 1) / 2;
                      const radius =
                        config.particleSize[0] +
                        (config.particleSize[1] - config.particleSize[0]) * oscillation;
                      ctx.fillStyle = rgba(sparkColor, config.particleAlpha * (0.8 + 0.2 * oscillation));
                      ctx.beginPath();
                      ctx.arc(x, y, radius, 0, Math.PI * 2);
                      ctx.fill();
                    }
                  }

                  ctx.restore();
                }}
                listening={false}
              />
            );
          })}

          {/* Ships */}
          {filteredShips.map((ship: any) => {
            const position = Ship.getPosition(ship, waypoints);
            if (position.x === 0 && position.y === 0) return null;

            const shipColor = ship.agentColor ? parseInt(ship.agentColor.replace('#', ''), 16) : 0xff6b6b;

            // Calculate rotation
            let rotation = 0;
            if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination) {
              const dest = ship.nav.route.destination;
              if (dest.x && dest.y) {
                const angle = Math.atan2(dest.y - position.y, dest.x - position.x);
                rotation = (angle + Math.PI / 2) * (180 / Math.PI);
              }
            } else if (ship.nav.status === 'IN_ORBIT') {
              const waypointSymbol = ship.nav.waypointSymbol;
              const waypoint = waypoints.get(waypointSymbol);
              if (waypoint) {
                const dx = position.x - waypoint.x;
                const dy = position.y - waypoint.y;
                const orbitalAngle = Math.atan2(dy, dx);
                rotation = (orbitalAngle + Math.PI) * (180 / Math.PI);
              }
            }

            return (
              <Group key={ship.symbol} x={position.x} y={position.y} rotation={rotation}>
                {/* Hit area - invisible circle for easier clicking */}
                <Circle
                  radius={15}
                  fill="transparent"
                  onClick={() => {
                    setSelectedObject({ type: 'ship', symbol: ship.symbol, x: position.x, y: position.y });
                    setSelectedShip(ship);
                    setSelectedWaypoint(null);
                  }}
                  onMouseEnter={(e) => {
                    setHoveredShip(ship.symbol);
                    setHoveredWaypoint(null);

                    const container = e.target.getStage()?.container();
                    if (container) container.style.cursor = 'pointer';
                  }}
                  onMouseLeave={(e) => {
                    setHoveredShip(null);

                    const container = e.target.getStage()?.container();
                    if (container) container.style.cursor = 'default';
                  }}
                />
                {/* Ship shape */}
                <Shape
                  sceneFunc={(context, _shape) => {
                    drawShipShape(context._context as CanvasRenderingContext2D, ship.registration.role, shipColor);
                  }}
                  listening={false}
                />
              </Group>
            );
          })}

          {/* Mining lasers */}
          {filteredShips.map((ship: any) => {
            if (!ship.cooldown || ship.cooldown.remainingSeconds <= 0) return null;

            const waypoint = waypoints.get(ship.nav.waypointSymbol);
            if (!waypoint) return null;

            const position = Ship.getPosition(ship, waypoints);
            const time = animationFrame / 60; // Convert to seconds

            return (
              <Group key={`laser-${ship.symbol}`}>
                {[0, 1, 2].map(i => {
                  const phase = (time * 3 + i * 0.5) % 1;
                  const alpha = 0.5 + Math.sin(phase * Math.PI * 2) * 0.4;
                  const offset = (i - 1) * 2;
                  const angle = Math.atan2(waypoint.y - position.y, waypoint.x - position.x);
                  const perpX = Math.cos(angle + Math.PI / 2) * offset;
                  const perpY = Math.sin(angle + Math.PI / 2) * offset;

                  return (
                    <Line
                      key={i}
                      points={[position.x, position.y, waypoint.x + perpX, waypoint.y + perpY]}
                      stroke="#ff0000"
                      strokeWidth={0.3}
                      opacity={alpha}
                      listening={false}
                    />
                  );
                })}
              </Group>
            );
          })}

        </Layer>
      </Stage>
      )}

      {shipLabelData.map((label: ShipLabelData) => (
        <div
          key={label.key}
          className="absolute pointer-events-none z-10 inline-flex items-center justify-center rounded-sm border border-red-500 bg-black/80 text-red-200 font-semibold leading-none shadow-[0_0_6px_rgba(255,77,79,0.3)] whitespace-nowrap"
          style={{
            left: `${label.left}px`,
            top: `${label.top}px`,
            fontSize: `${SHIP_LABEL_FONT_SIZE}px`,
            padding: `${SHIP_LABEL_PADDING_Y}px ${SHIP_LABEL_PADDING_X}px`,
          }}
        >
          {label.text}
        </div>
      ))}

      {selectionOverlay && (
        <div
          className="absolute pointer-events-none z-20"
          style={{
            left: `${selectionOverlay.left}px`,
            top: `${selectionOverlay.top}px`,
            width: `${selectionOverlay.size * 2}px`,
            height: `${selectionOverlay.size * 2}px`,
            transform: 'translate(-50%, -50%)',
          }}
        >
          <div className="relative w-full h-full">
            <div className="absolute inset-0 rounded-full border border-green-400/80" />
            {[['top', 'left'], ['top', 'right'], ['bottom', 'left'], ['bottom', 'right']].map(([vertical, horizontal]) => (
              <div
                key={`${vertical}-${horizontal}`}
                className="absolute w-2 h-2 border-green-400/80"
                style={{
                  [vertical]: 0,
                  [horizontal]: 0,
                  borderStyle: 'solid',
                  borderTopWidth: vertical === 'top' ? '1px' : '0px',
                  borderBottomWidth: vertical === 'bottom' ? '1px' : '0px',
                  borderLeftWidth: horizontal === 'left' ? '1px' : '0px',
                  borderRightWidth: horizontal === 'right' ? '1px' : '0px',
                }}
              />
            ))}
          </div>
        </div>
      )}

      {/* Ship tooltip */}
      {shipTooltip && shipTooltipPosition && (
        <div
          className="absolute bg-black bg-opacity-80 border border-red-500 rounded-lg p-3 text-xs min-w-[260px] max-w-[340px] pointer-events-none z-30 shadow-xl backdrop-blur-sm"
          style={{
            left: `${shipTooltipPosition.left}px`,
            top: `${shipTooltipPosition.top}px`,
            transform: 'translate(-100%, -100%)',
          }}
        >
          <div className="flex items-start justify-between gap-3 mb-3">
            <div>
              <div className="text-sm font-bold text-white leading-snug">{shipTooltip.symbol}</div>
              <div className="text-[10px] uppercase tracking-wide text-red-300">{shipTooltip.registrationName}</div>
            </div>
            <span className="text-[10px] font-semibold text-red-300 bg-red-500/10 border border-red-500/40 rounded-full px-2 py-0.5 whitespace-nowrap">
              {shipTooltip.role}
            </span>
          </div>

          <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-gray-200">
            <div>
              <div className="text-[10px] uppercase text-gray-400">Status</div>
              <div className="text-xs font-semibold">{shipTooltip.statusText}</div>
            </div>
            <div>
              <div className="text-[10px] uppercase text-gray-400">Flight Mode</div>
              <div className="text-xs">{shipTooltip.flightMode}</div>
            </div>

            <div>
              <div className="text-[10px] uppercase text-gray-400">Location</div>
              <div className="text-xs">{shipTooltip.location}</div>
            </div>

            <div>
              <div className="text-[10px] uppercase text-gray-400">Cargo Capacity</div>
              <div className="text-xs font-semibold text-red-200">
                {shipTooltip.cargoUnits} / {shipTooltip.cargoCapacity}
              </div>
            </div>

            {shipTooltip.cooldownSeconds !== null && (
              <div>
                <div className="text-[10px] uppercase text-gray-400">Cooldown</div>
                <div className="text-xs">{shipTooltip.cooldownSeconds}s</div>
              </div>
            )}

            {shipTooltip.routeSummary && (
              <div className="col-span-2">
                <div className="text-[10px] uppercase text-gray-400">Route</div>
                <div className="text-xs flex items-center gap-2">
                  <span>{shipTooltip.routeSummary}</span>
                  {shipTooltip.etaText && (
                    <span className="text-[10px] text-red-200 bg-red-500/10 px-1.5 py-0.5 rounded-full">
                      ETA {shipTooltip.etaText}
                    </span>
                  )}
                </div>
              </div>
            )}

            <div className="col-span-2">
              <div className="flex items-center justify-between text-[10px] uppercase text-gray-400">
                <span>Fuel</span>
                <span className="text-xs text-red-200 font-semibold">
                  {shipTooltip.fuelCurrent} / {shipTooltip.fuelCapacity} ({shipTooltip.fuelPercent}%)
                </span>
              </div>
              <div className="w-full bg-red-900/40 h-1.5 rounded-full mt-1">
                <div
                  className="bg-red-500 h-1.5 rounded-full"
                  style={{ width: `${Math.min(100, Math.max(0, shipTooltip.fuelPercent))}%` }}
                />
              </div>
            </div>
          </div>

          {shipTooltip.cargoEntries.length > 0 && (
            <div className="mt-3">
              <div className="text-[10px] uppercase text-gray-400 mb-1">Cargo Hold</div>
              <div className="grid grid-cols-2 gap-2">
                {shipTooltip.cargoEntries.map((item, index) => (
                  <div
                    key={`${item.label}-${index}`}
                    className="flex items-center gap-2 text-xs text-gray-200 bg-white/5 border border-white/10 rounded-md px-2 py-1"
                  >
                    <span className="text-base leading-none">{item.icon}</span>
                    <div className="flex flex-col leading-tight">
                      <span className="text-[11px]">{item.label}</span>
                      <span className="text-[10px] text-gray-400">×{item.units}</span>
                    </div>
                  </div>
                ))}
                {shipTooltip.extraCargoCount > 0 && (
                  <div className="col-span-2 text-[10px] text-gray-500">
                    +{shipTooltip.extraCargoCount} more item{shipTooltip.extraCargoCount > 1 ? 's' : ''}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Waypoint tooltip */}
      {waypointTooltip && (
        <div
          className="fixed bg-gray-800 bg-opacity-95 rounded-lg p-3 text-xs min-w-[200px] border border-gray-600 pointer-events-none z-20 shadow-lg"
          style={{
            left: `${mousePosition.x + 10}px`,
            bottom: `${window.innerHeight - mousePosition.y + 10}px`,
          }}
        >
          <div className="font-bold mb-1 text-sm">{waypointTooltip.symbol}</div>
          <div className="text-gray-300 mb-2">{waypointTooltip.type.replace(/_/g, ' ')}</div>

          <div className="text-gray-400 text-xs mb-2">
            <div>X: {waypointTooltip.x}</div>
            <div>Y: {waypointTooltip.y}</div>
          </div>

          {waypointTooltip.faction && (
            <div className="text-blue-400 mb-2">
              Faction: {waypointTooltip.faction.symbol}
            </div>
          )}

          {waypointTooltip.traits.length > 0 && (
            <div className="mb-2">
              <div className="text-gray-400 mb-1">Traits:</div>
              <div className="flex flex-wrap gap-1">
                {waypointTooltip.traits.slice(0, 3).map((trait, i) => (
                  <span key={i} className="bg-gray-700 px-2 py-0.5 rounded text-xs">
                    {trait.symbol.replace(/_/g, ' ')}
                  </span>
                ))}
                {waypointTooltip.traits.length > 3 && (
                  <span className="text-gray-500">+{waypointTooltip.traits.length - 3} more</span>
                )}
              </div>
            </div>
          )}

          {waypointTooltip.marketData && (
            <div className="border-t border-gray-600 pt-2 mt-2">
              <div className="text-yellow-400 flex items-center gap-2 mb-1">
                <span>🏪</span> Market
              </div>
              <div className="text-xs">↓ {waypointTooltip.marketData.importsCount} Imports</div>
              <div className="text-xs mb-2">↑ {waypointTooltip.marketData.exportsCount} Exports</div>

              {waypointTooltip.marketData.opportunities.length > 0 && (
                <>
                  <div className="text-gray-400 mb-1">Opportunities:</div>
                  <ul className="text-green-400 text-xs">
                    {waypointTooltip.marketData.opportunities.map((opp, i) => (
                      <li key={i} className="mb-1">• {opp}</li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          )}
        </div>
      )}

      {!currentSystem && (
        <div className="absolute inset-0 flex items-center justify-center">
          <div className="bg-gray-800 bg-opacity-90 rounded-lg p-8 text-center">
            <h2 className="text-xl font-bold mb-2">No System Selected</h2>
            <p className="text-gray-400">Add an agent and select a system to begin</p>
          </div>
        </div>
      )}

      {/* Zoom Controls */}
      <ZoomControls
        onZoomIn={handleZoomIn}
        onZoomOut={handleZoomOut}
        onReset={handleResetView}
        onFitView={handleFitView}
      />

      {/* Minimap */}
      <Minimap
        waypoints={waypoints}
        ships={filteredShips}
        viewportBounds={viewportBounds}
        onNavigate={handleMinimapNavigate}
        onZoom={handleMinimapZoom}
        animationFrame={animationFrame}
      />
    </div>
  );
});

SpaceMap.displayName = 'SpaceMap';

export default SpaceMap;
