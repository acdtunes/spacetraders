import { useEffect, useRef, useState, forwardRef, useImperativeHandle, useMemo, useCallback } from 'react';
import { Stage, Layer, Shape, Group, Circle, Text, Line, Label, Tag, Image as KonvaImage } from 'react-konva';
import Konva from 'konva';
import { useStore } from '../store/useStore';
import { getWaypoints } from '../services/api';
import { getWaypointOpportunities, formatOpportunity } from '../domain/market';
import { Ship, Waypoint, ShipQueries, WaypointQueries, ViewportBounds } from '../domain';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';
import { getCargoIcon, getCargoLabel } from '../utils/cargo';
import { getFuelBarColor } from '../utils/fuel';
import { hashString } from '../utils/hash';
import { RouteVectors } from './RouteVectors';
import ZoomControls from './ZoomControls';
import Minimap from './Minimap';
import type { FlightMode, ShipTrailPoint, Waypoint as WaypointType, TaggedShip, ShipNavStatus } from '../types/spacetraders';

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
    baseWidth: 0.7,
    baseAlpha: 0.28,
    tailAlpha: 0.06,
    glowBlur: 4,
    glowAlpha: 0.22,
    particleDensity: 0.18,
    particleSize: [0.25, 0.5],
    particleAlpha: 0.22,
    colorBoost: 0.18,
  },
  BURN: {
    maxAgeMs: 12000,
    baseWidth: 2.5,
    baseAlpha: 0.55,
    tailAlpha: 0.15,
    glowBlur: 12,
    glowAlpha: 0.65,
    particleDensity: 0.6,
    particleSize: [0.6, 1.3],
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

const SHIP_LABEL_FONT_SIZE = 10;
const SHIP_LABEL_PADDING_X = 6;
const SHIP_LABEL_PADDING_Y = 3;
const SHIP_LABEL_MIN_WIDTH = 56;
const SHIP_LABEL_SCREEN_OFFSET_X = 16;
const SHIP_LABEL_SCREEN_OFFSET_Y = 14;

const SHIP_TOOLTIP_OFFSET_X = 12;
const SHIP_TOOLTIP_OFFSET_Y = 12;
const WAYPOINT_TOOLTIP_OFFSET_X = 12;
const WAYPOINT_TOOLTIP_OFFSET_Y = 12;

const WAYPOINT_ASSET_BASE_PATH = '/assets/waypoints/';
const SHIP_ASSET_BASE_PATH = '/assets/ships/';

const WAYPOINT_ASSET_VARIANTS: Record<string, string[]> = {
  asteroid: ['waypoint-asteroid-1.png', 'waypoint-asteroid-2.png'],
  asteroidBase: ['waypoint-asteroid-base-1.png', 'waypoint-asteroid-base-2.png'],
  engineeredAsteroid: ['waypoint-engineered-asteroid-2.png'],
  orbitalStation: ['waypoint-orbital-station-1.png'],
  frozenMoon: ['waypoint-frozen-moon-1.png', 'waypoint-frozen-moon-2.png'],
  planetTemperate: ['waypoint-planet-temperate-1.png', 'waypoint-planet-temperate-2.png'],
  planetOcean: ['waypoint-planet-ocean-1.png', 'waypoint-planet-ocean-2.png'],
  planetFrozen: ['waypoint-planet-frozen-1.png', 'waypoint-planet-frozen-2.png'],
  planetRocky: ['waypoint-planet-rocky-1.png', 'waypoint-planet-rocky-2.png'],
  planetVolcanic: ['waypoint-planet-volcanic-1.png', 'waypoint-planet-volcanic-2.png'],
  planetRadioactive: [
    'waypoint-planet-radioactive-1.png',
    'waypoint-planet-radioactive-2.png',
    'waypoint-planet-radioactive-3.png',
    'waypoint-planet-radioactive-4.png',
  ],
  planetSwamp: ['waypoint-planet-swamp-2.png'],
  planetJovian: ['waypoint-planet-jovian-1.png', 'waypoint-planet-jovian-2.png'],
  fuelStation: ['waypoint-fuel-station-1.png', 'waypoint-fuel-station-2.png'],
  volcanicMoon: ['waypoint-volcanic-moon-1.png', 'waypoint-volcanic-moon-2.png'],
};

const SHIP_ASSET_VARIANTS: Record<string, string[]> = {
  command: ['ship-command-frigate-2.png'],
  hauler: ['ship-light-hauler-1.png', 'ship-light-hauler-2.png'],
  mining: ['ship-mining-drone-1.png', 'ship-mining-drone-2.png'],
  probe: ['ship-probe-2.png'],
  satellite: ['ship-satellite-1.png', 'ship-satellite-2.png'],
  station: ['ship-space-station-1.png', 'ship-space-station-2.png'],
};

const DEFAULT_WAYPOINT_ASSET = 'waypoint-planet-rocky-1.png';
const DEFAULT_SHIP_ASSET = 'ship-command-frigate-2.png';
const DEFAULT_SHIP_SPRITE_SIZE = 18;
const SHIP_SPRITE_SIZE = DEFAULT_SHIP_SPRITE_SIZE / 10;
const SHIP_POSITION_SMOOTHING_MS = 900;
const SHIP_POSITION_DISTANCE_THRESHOLD = 2;

const MINING_WAYPOINT_TYPES = new Set<WaypointType['type']>([
  'ASTEROID',
  'ASTEROID_FIELD',
  'ENGINEERED_ASTEROID',
  'ASTEROID_BASE',
]);

const imageCache = new Map<string, HTMLImageElement | null>();

const useCachedImage = (src: string | null): HTMLImageElement | null => {
  const [image, setImage] = useState<HTMLImageElement | null>(() => {
    if (!src || typeof window === 'undefined') return null;
    const cached = imageCache.get(src);
    return cached ?? null;
  });

  useEffect(() => {
    if (!src || typeof window === 'undefined') {
      setImage(null);
      return;
    }

    const cached = imageCache.get(src);
    if (cached !== undefined) {
      setImage(cached);
      return;
    }

    let cancelled = false;
    const img = new window.Image();
    img.src = src;
    img.onload = () => {
      if (cancelled) return;
      imageCache.set(src, img);
      setImage(img);
    };
    img.onerror = () => {
      if (cancelled) return;
      imageCache.set(src, null);
      setImage(null);
    };

    return () => {
      cancelled = true;
    };
  }, [src]);

  return image;
};

const WaypointSprite = ({
  assetPath,
  x,
  y,
  radius,
  scale,
}: {
  assetPath: string | null;
  x: number;
  y: number;
  radius: number;
  scale: number;
}) => {
  const image = useCachedImage(assetPath);
  const MIN_WORLD_SIZE = 1.2;
  const size = Math.max(radius * 2, MIN_WORLD_SIZE);
  const half = size / 2;

  if (image && image.width > 0 && image.height > 0) {
    return (
      <KonvaImage
        image={image}
        x={x - half}
        y={y - half}
        width={size}
        height={size}
        listening={false}
      />
    );
  }

  const crossSize = Math.max(size * 0.75, 14 / Math.max(scale, 0.0001));
  const crossHalf = crossSize / 2;
  const strokeWidth = Math.max(2 / Math.max(scale, 0.0001), 0.8);

  return (
    <Group x={x} y={y} listening={false}>
      <Circle
        radius={size / 2}
        fill="#1f2937"
        stroke="#ef4444"
        strokeWidth={strokeWidth * 0.6}
        listening={false}
        opacity={0.4}
      />
      <Line
        points={[-crossHalf, -crossHalf, crossHalf, crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
      <Line
        points={[-crossHalf, crossHalf, crossHalf, -crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
    </Group>
  );
};

const ShipSprite = ({
  assetPath,
  size,
}: {
  assetPath: string | null;
  size: number;
}) => {
  const image = useCachedImage(assetPath);

  if (image && image.width > 0 && image.height > 0) {
    return (
      <KonvaImage
        image={image}
        x={-size / 2}
        y={-size / 2}
        width={size}
        height={size}
        listening={false}
      />
    );
  }

  const crossSize = Math.max(size * 0.65, 6);
  const crossHalf = crossSize / 2;
  const strokeWidth = Math.max(size * 0.08, 0.8);

  return (
    <Group listening={false}>
      <Line
        points={[-crossHalf, -crossHalf, crossHalf, crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
      <Line
        points={[-crossHalf, crossHalf, crossHalf, -crossHalf]}
        stroke="#f87171"
        strokeWidth={strokeWidth}
        listening={false}
        lineCap="round"
      />
    </Group>
  );
};

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
  const shipPositionCacheRef = useRef<Map<string, { x: number; y: number; status: ShipNavStatus; timestamp: number }>>(new Map());

  const { currentSystem, waypoints, ships, markets, showMapOverlays, showWaypointNames, showShipNames, showDestinationRoutes, setWaypoints, trails, addTrailPosition, clearTrail, filterStatus, filterAgents, filterWaypointTypes, setSelectedShip, setSelectedWaypoint } =
    useStore();

  const [hoveredShip, setHoveredShip] = useState<string | null>(null);
  const [waypointTooltipAnchor, setWaypointTooltipAnchor] = useState<{ symbol: string; worldX: number; worldY: number } | null>(null);
  const [selectedObject, setSelectedObject] = useState<{ type: 'waypoint' | 'ship', symbol: string, x: number, y: number } | null>(null);
  const [viewportBounds, setViewportBounds] = useState({ x: 0, y: 0, width: 0, height: 0, scale: 1 });
  const [animationFrame, setAnimationFrame] = useState(0);

  const [stageSize, setStageSize] = useState({ width: 0, height: 0 });

  const currentScale = viewportBounds.scale || 1;
  const frameTimestamp = useMemo(() => Date.now(), [animationFrame]);

  const getShipRenderPosition = useCallback(
    (ship: TaggedShip, target: { x: number; y: number }, timestamp: number): { x: number; y: number } => {
      const cache = shipPositionCacheRef.current;
      const previous = cache.get(ship.symbol);
      const status = ship.nav.status as ShipNavStatus;

      const distanceFromPrevious = previous
        ? Math.hypot(target.x - previous.x, target.y - previous.y)
        : 0;

      let smoothingAllowed = status !== 'IN_TRANSIT';
      if (!smoothingAllowed && previous) {
        const largeJumpThreshold = SHIP_POSITION_DISTANCE_THRESHOLD * 4;
        if (distanceFromPrevious > largeJumpThreshold) {
          smoothingAllowed = true;
        }
      }

      let result = target;

      if (!smoothingAllowed || !previous) {
        cache.set(ship.symbol, { x: target.x, y: target.y, status, timestamp });
        return target;
      }

      const statusChanged = previous.status !== status;
      const shouldSmooth = statusChanged || distanceFromPrevious > SHIP_POSITION_DISTANCE_THRESHOLD;

      if (shouldSmooth) {
        const deltaTime = Math.max(0, timestamp - previous.timestamp);
        const alpha = 1 - Math.exp(-deltaTime / SHIP_POSITION_SMOOTHING_MS);
        result = {
          x: previous.x + (target.x - previous.x) * alpha,
          y: previous.y + (target.y - previous.y) * alpha,
        };
      }

      cache.set(ship.symbol, { x: result.x, y: result.y, status, timestamp });
      return result;
    },
    []
  );

  useEffect(() => {
    const cache = shipPositionCacheRef.current;
    const knownSymbols = new Set(ships.map((ship) => ship.symbol));
    Array.from(cache.keys()).forEach((symbol) => {
      if (!knownSymbols.has(symbol)) {
        cache.delete(symbol);
      }
    });
  }, [ships]);

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
  const animationLayer = layerRef.current;

  useEffect(() => {
    if (!animationLayer) return;

    const anim = new Konva.Animation(() => {
      setAnimationFrame(prev => prev + 1);
    }, animationLayer);

    anim.start();

    return () => {
      anim.stop();
    };
  }, [animationLayer]);

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
    }) as TaggedShip[];
  }, [ships, currentSystem, filterStatus, filterAgents]);

  // Filter waypoints using domain queries
  const filteredWaypoints = useMemo(() => {
    return WaypointQueries.filterByType(
      Array.from(waypoints.values()),
      filterWaypointTypes
    );
  }, [waypoints, filterWaypointTypes]);

  const getWaypointDisplayPosition = useCallback(
    (waypoint: WaypointType): { x: number; y: number } => {
      const overlapIndex = filteredWaypoints.filter((w) =>
        w.x === waypoint.x &&
        w.y === waypoint.y &&
        w.symbol <= waypoint.symbol
      ).length - 1;

      if (overlapIndex <= 0) {
        return { x: waypoint.x, y: waypoint.y };
      }

      const angle = (overlapIndex * Math.PI * 2) / 8;
      const offset = 15 * overlapIndex;
      return {
        x: waypoint.x + Math.cos(angle) * offset,
        y: waypoint.y + Math.sin(angle) * offset,
      };
    },
    [filteredWaypoints]
  );

  const selectWaypointAsset = useCallback((waypoint: WaypointType): string => {
    const traitSymbols = (waypoint.traits ?? []).map((trait) => trait.symbol.toUpperCase());
    const hasTrait = (...keywords: string[]) =>
      traitSymbols.some((trait) => keywords.some((keyword) => trait.includes(keyword)));

    let variantKey: string;

    if (
      waypoint.type === 'ASTEROID' ||
      waypoint.type === 'ASTEROID_FIELD'
    ) {
      variantKey = 'asteroid';
    } else if (waypoint.type === 'ASTEROID_BASE') {
      variantKey = 'asteroidBase';
    } else if (waypoint.type === 'ENGINEERED_ASTEROID') {
      variantKey = 'engineeredAsteroid';
    } else if (
      waypoint.type === 'GAS_GIANT' ||
      hasTrait('GAS_GIANT') ||
      hasTrait('JOVIAN')
    ) {
      variantKey = 'planetJovian';
    } else if (hasTrait('OCEAN', 'WATER')) {
      variantKey = 'planetOcean';
    } else if (hasTrait('TEMPERATE', 'TROPICAL', 'FOREST')) {
      variantKey = 'planetTemperate';
    } else if (hasTrait('FROZEN', 'ICE')) {
      variantKey = waypoint.type === 'MOON' ? 'frozenMoon' : 'planetFrozen';
    } else if (hasTrait('VOLCANIC', 'INFERNO')) {
      variantKey = waypoint.type === 'MOON' ? 'volcanicMoon' : 'planetVolcanic';
    } else if (hasTrait('RADIOACTIVE', 'NUCLEAR')) {
      variantKey = 'planetRadioactive';
    } else if (waypoint.type === 'ORBITAL_STATION' || hasTrait('ORBITAL')) {
      variantKey = 'orbitalStation';
    } else if (waypoint.type.includes('STATION')) {
      variantKey = 'fuelStation';
    } else if (hasTrait('SWAMP', 'JUNGLE', 'BOG')) {
      variantKey = 'planetSwamp';
    } else if (waypoint.type === 'FUEL_STATION' || hasTrait('FUEL')) {
      variantKey = 'fuelStation';
    } else if (waypoint.type === 'MOON') {
      variantKey = 'planetRocky';
    } else {
      variantKey = 'planetRocky';
    }

    const variants = WAYPOINT_ASSET_VARIANTS[variantKey] ?? WAYPOINT_ASSET_VARIANTS.planetRocky;
    const assetIndex = variants.length > 0
      ? hashString(`${waypoint.symbol}:${variantKey}`) % variants.length
      : 0;
    const filename = variants[assetIndex] ?? DEFAULT_WAYPOINT_ASSET;
    return `${WAYPOINT_ASSET_BASE_PATH}${filename}`;
  }, []);

  const selectShipAsset = useCallback((ship: TaggedShip): string | null => {
    const role = ship.registration.role?.toLowerCase() ?? '';

    let variantKey: string;
    if (role.includes('satellite')) {
      variantKey = 'satellite';
    } else if (role.includes('station') || role.includes('platform')) {
      variantKey = 'station';
    } else if (role.includes('probe') || role.includes('scout') || role.includes('explorer')) {
      variantKey = 'probe';
    } else if (
      role.includes('mine') ||
      role.includes('extract') ||
      role.includes('drone') ||
      role.includes('excavator') ||
      role.includes('miner')
    ) {
      variantKey = 'mining';
    } else if (role.includes('haul') || role.includes('freight') || role.includes('cargo') || role.includes('transport')) {
      variantKey = 'hauler';
    } else {
      variantKey = 'command';
    }

    const variants = SHIP_ASSET_VARIANTS[variantKey];
    if (!variants || variants.length === 0) {
      return `${SHIP_ASSET_BASE_PATH}${DEFAULT_SHIP_ASSET}`;
    }

    const filename = variants[hashString(`${ship.symbol}:${variantKey}`) % variants.length] ?? DEFAULT_SHIP_ASSET;
    return `${SHIP_ASSET_BASE_PATH}${filename}`;
  }, []);

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
  const activeWaypointTooltipSymbol = waypointTooltipAnchor?.symbol ?? null;

  const projectToScreen = useCallback((point: { x: number; y: number }) => {
    const layer = layerRef.current;
    const stage = stageRef.current;
    const container = containerRef.current;
    if (!layer || !stage || !container) return null;

    const transform = layer.getAbsoluteTransform().copy();
    const { x, y } = transform.point(point);

    const stageRect = stage.container().getBoundingClientRect();
    const containerRect = container.getBoundingClientRect();

    return {
      x: x + (stageRect.left - containerRect.left),
      y: y + (stageRect.top - containerRect.top),
    };
  }, []);

  const projectToWorld = useCallback((point: { x: number; y: number }) => {
    const layer = layerRef.current;
    const stage = stageRef.current;
    const container = containerRef.current;
    if (!layer || !stage || !container) return null;

    const stageRect = stage.container().getBoundingClientRect();
    const containerRect = container.getBoundingClientRect();
    const transform = layer.getAbsoluteTransform().copy();
    transform.invert();

    return transform.point({
      x: point.x - (stageRect.left - containerRect.left),
      y: point.y - (stageRect.top - containerRect.top),
    });
  }, []);

  const shipTooltip = activeShipTooltipSymbol ? (() => {
    const ship = ships.find((s) => s.symbol === activeShipTooltipSymbol);
    if (!ship) return null;

    const statusText = ship.nav.status === 'DOCKED'
      ? `Docked at ${ship.nav.waypointSymbol}`
      : ship.nav.status.replace(/_/g, ' ');
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

    const cargoPercent = ship.cargo.capacity > 0
      ? Math.round((ship.cargo.units / ship.cargo.capacity) * 100)
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
      cargoPercent,
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

    const targetPosition = Ship.getPosition(ship, waypoints);
    const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
    if (position.x === 0 && position.y === 0) return null;

    const screenPos = projectToScreen(position);
    if (!screenPos) return null;

    return {
      left: screenPos.x - SHIP_TOOLTIP_OFFSET_X,
      top: screenPos.y - SHIP_TOOLTIP_OFFSET_Y,
    };
  }, [activeShipTooltipSymbol, ships, waypoints, projectToScreen, viewportBounds, getShipRenderPosition, frameTimestamp]);

  const selectionOverlay = useMemo(() => {
    if (!selectedObject) return null;

    let worldX = selectedObject.x;
    let worldY = selectedObject.y;

    if (selectedObject.type === 'ship') {
      const ship = ships.find((s) => s.symbol === selectedObject.symbol);
      if (!ship) return null;
      const targetPosition = Ship.getPosition(ship, waypoints);
      const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
      if (position.x === 0 && position.y === 0) return null;
      worldX = position.x;
      worldY = position.y;
    } else if (selectedObject.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (!waypoint) return null;
      const displayPosition = getWaypointDisplayPosition(waypoint);
      worldX = displayPosition.x;
      worldY = displayPosition.y;
    }

    const screenPos = projectToScreen({ x: worldX, y: worldY });
    if (!screenPos) return null;
    const size = selectedObject.type === 'waypoint' ? 18 : 14;

    return {
      left: screenPos.x,
      top: screenPos.y,
      size,
      type: selectedObject.type,
    };
  }, [selectedObject, ships, waypoints, projectToScreen, viewportBounds, getWaypointDisplayPosition, getShipRenderPosition, frameTimestamp]);

  useEffect(() => {
    if (selectedObject?.type === 'waypoint') {
      const waypoint = waypoints.get(selectedObject.symbol);
      if (waypoint) {
        const { x, y } = getWaypointDisplayPosition(waypoint);
        setWaypointTooltipAnchor({ symbol: waypoint.symbol, worldX: x, worldY: y });
      }
    } else {
      setWaypointTooltipAnchor(null);
    }
  }, [selectedObject, waypoints, getWaypointDisplayPosition]);

  const waypointTooltip = activeWaypointTooltipSymbol ? (() => {
    const waypoint = waypoints.get(activeWaypointTooltipSymbol);
    if (!waypoint) return null;

    const market = markets.get(activeWaypointTooltipSymbol);
    const hasMarketplace = waypoint.traits.some((t) => t.symbol === 'MARKETPLACE');

    let marketData = null;
    if (market && hasMarketplace) {
      const opportunities = getWaypointOpportunities(activeWaypointTooltipSymbol, markets, 2);
      marketData = {
        importsCount: market.imports.length,
        exportsCount: market.exports.length,
        opportunities: opportunities.map(formatOpportunity),
      };
    }

    return {
      symbol: activeWaypointTooltipSymbol,
      type: waypoint.type,
      x: waypoint.x,
      y: waypoint.y,
      traits: waypoint.traits,
      faction: waypoint.faction,
      hasMarketplace,
      marketData,
    };
  })() : null;

  const waypointTooltipPosition = useMemo(() => {
    if (!waypointTooltipAnchor) return null;

    const screenPos = projectToScreen({ x: waypointTooltipAnchor.worldX, y: waypointTooltipAnchor.worldY });
    if (!screenPos) return null;

    return {
      left: screenPos.x + WAYPOINT_TOOLTIP_OFFSET_X,
      top: screenPos.y - WAYPOINT_TOOLTIP_OFFSET_Y,
    };
  }, [waypointTooltipAnchor, projectToScreen, viewportBounds]);

  return (
    <div ref={containerRef} className="relative w-full h-full">
      {stageSize.width > 0 && stageSize.height > 0 && (
        <Stage
          ref={stageRef}
          width={stageSize.width}
          height={stageSize.height}
          draggable
          onWheel={handleWheel}
          onMouseLeave={() => {
            setHoveredShip(null);
            if (!selectedObject || selectedObject.type !== 'waypoint') {
              setWaypointTooltipAnchor(null);
            }
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
          {filteredWaypoints.map((waypoint) => {
            const radius = Waypoint.getRadius(waypoint);
            const hasMarketplace = waypoint.traits.some((t) => t.symbol === 'MARKETPLACE');

            const assetPath = selectWaypointAsset(waypoint);
            const { x, y } = getWaypointDisplayPosition(waypoint);
            const hitRadius = Math.max(radius + 3, 8 / currentScale);

            return (
              <Group key={waypoint.symbol}>
                <Circle
                  x={x}
                  y={y}
                  radius={hitRadius}
                  fill="rgba(255,255,255,0.01)"
                  listening
                  onMouseEnter={(e) => {
                    const container = e.target.getStage()?.container();
                    if (container) container.style.cursor = 'pointer';
                  }}
                  onMouseLeave={(e) => {
                    const container = e.target.getStage()?.container();
                    if (container) container.style.cursor = 'default';
                  }}
                  onClick={() => {
                    setSelectedObject({ type: 'waypoint', symbol: waypoint.symbol, x: waypoint.x, y: waypoint.y });
                    setSelectedWaypoint(waypoint);
                    setSelectedShip(null);
                    setWaypointTooltipAnchor({ symbol: waypoint.symbol, worldX: x, worldY: y });
                  }}
                />

                {/* Marketplace ring */}
                <WaypointSprite
                  assetPath={assetPath}
                  x={x}
                  y={y}
                  radius={radius}
                  scale={currentScale}
                />

                {hasMarketplace && showMapOverlays && (
                  <Text
                    text="🏪"
                    x={x - radius - 8 / currentScale}
                    y={y - 6 / currentScale}
                    fontSize={12 / currentScale}
                    fill="#facc15"
                    stroke="rgba(17, 24, 39, 0.65)"
                    strokeWidth={0.4 / currentScale}
                    listening={false}
                  />
                )}

                {showWaypointNames && (
                  <Text
                    text={waypoint.symbol.split('-').pop() || waypoint.symbol}
                    x={x + radius + 4}
                    y={y - 5}
                    fontSize={10 / currentScale}
                    fill="#e2e8f0"
                    listening={false}
                  />
                )}

                {/* Fuel icon for stations */}
                {showMapOverlays && assetPath.includes('fuel') && (
                  <Text
                    text="⛽"
                    x={x + radius + 8 / currentScale}
                    y={y - 6 / currentScale}
                    fontSize={14 / currentScale}
                    listening={false}
                  />
                )}
              </Group>
            );
          })}

          {/* Ship trails */}
          {filteredShips.map((ship: TaggedShip) => {
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

                  if (config.particleDensity > 0 && ship.nav.status === 'IN_TRANSIT') {
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

          {/* Active route indicators */}
          {showDestinationRoutes && (
            <RouteVectors
              ships={filteredShips}
              waypoints={waypoints}
              currentScale={currentScale}
              animationFrame={animationFrame}
              frameTimestamp={frameTimestamp}
              getShipRenderPosition={getShipRenderPosition}
            />
          )}

          {/* Ships */}
          {filteredShips.map((ship: TaggedShip) => {
            const targetPosition = Ship.getPosition(ship, waypoints);
            if (targetPosition.x === 0 && targetPosition.y === 0) return null;
            const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);

            const shipAssetPath = selectShipAsset(ship);

            // Calculate rotation
            let rotation = 0;
            let travelAngleRad: number | null = null;

            const shipTrail = trails.get(ship.symbol) as ShipTrailPoint[] | undefined;
            if (shipTrail && shipTrail.length >= 2) {
              const previous = shipTrail[shipTrail.length - 2];
              const dxTrail = position.x - previous.x;
              const dyTrail = position.y - previous.y;
              if (Math.hypot(dxTrail, dyTrail) > 0.01) {
                travelAngleRad = Math.atan2(dyTrail, dxTrail);
              }
            }

            if (travelAngleRad === null) {
              if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination) {
                const dest = ship.nav.route.destination;
                if (typeof dest.x === 'number' && typeof dest.y === 'number') {
                  travelAngleRad = Math.atan2(dest.y - position.y, dest.x - position.x);
                }
              } else if (ship.nav.status === 'IN_ORBIT') {
                const waypointSymbol = ship.nav.waypointSymbol;
                const waypoint = waypoints.get(waypointSymbol);
                if (waypoint) {
                  const dx = position.x - waypoint.x;
                  const dy = position.y - waypoint.y;
                  const orbitalAngle = Math.atan2(dy, dx);
                  travelAngleRad = orbitalAngle + Math.PI / 2;
                }
              }
            }

            if (travelAngleRad !== null) {
              rotation = (travelAngleRad + Math.PI / 2) * (180 / Math.PI);
            }

            const shipNumber = ship.symbol.split('-').pop() ?? ship.symbol;
            const shipTypeRaw = ship.registration.role || 'UNKNOWN';
            const shipType = shipTypeRaw
              .split('_')
              .map((part: string) => part.charAt(0) + part.slice(1).toLowerCase())
              .join(' ');
            const labelText = `${shipType} ${shipNumber}`;
            const labelHeight = SHIP_LABEL_FONT_SIZE + SHIP_LABEL_PADDING_Y * 2;
            const estimatedTextWidth = labelText.length * (SHIP_LABEL_FONT_SIZE * 0.6);
            const labelWidth = Math.max(
              SHIP_LABEL_MIN_WIDTH,
              estimatedTextWidth + SHIP_LABEL_PADDING_X * 2
            );
            const labelScale = 1 / currentScale;

            const screenPos = projectToScreen(position);
            if (!screenPos) {
              return null;
            }

            const labelTargetScreen = {
              x: screenPos.x + SHIP_LABEL_SCREEN_OFFSET_X,
              y: screenPos.y - SHIP_LABEL_SCREEN_OFFSET_Y,
            };

            const labelWorldPos = projectToWorld(labelTargetScreen);
            if (!labelWorldPos) {
              return null;
            }

            const labelOffsetX = labelWorldPos.x - position.x;
            const labelOffsetY = labelWorldPos.y - position.y;

            return (
              <Group key={ship.symbol} x={position.x} y={position.y}>
                <Group rotation={rotation}>
                  {/* Hit area - invisible circle for easier clicking */}
                  <Circle
                    radius={4}
                    fill="transparent"
                    onClick={() => {
                      setSelectedObject({ type: 'ship', symbol: ship.symbol, x: position.x, y: position.y });
                      setSelectedShip(ship);
                      setSelectedWaypoint(null);
                    }}
                    onMouseEnter={(e) => {
                      setHoveredShip(ship.symbol);

                      const container = e.target.getStage()?.container();
                      if (container) container.style.cursor = 'pointer';
                    }}
                    onMouseLeave={(e) => {
                      setHoveredShip(null);

                      const container = e.target.getStage()?.container();
                      if (container) container.style.cursor = 'default';
                    }}
                  />
                  <ShipSprite assetPath={shipAssetPath} size={SHIP_SPRITE_SIZE} />
                </Group>

               {showShipNames && (
                 <Group
                   listening={false}
                   x={labelOffsetX}
                   y={labelOffsetY}
                 >
                   <Group scale={{ x: labelScale, y: labelScale }} listening={false}>
                     <Label>
                       <Tag
                          width={labelWidth + 12}
                          height={labelHeight}
                          fill="rgba(0, 0, 0, 0.82)"
                          stroke="#ff4d4f"
                          strokeWidth={1}
                          cornerRadius={3}
                        />
                        <Text
                          x={SHIP_LABEL_PADDING_X}
                          y={SHIP_LABEL_PADDING_Y / 1.5}
                          width={labelWidth + 12 - SHIP_LABEL_PADDING_X * 2}
                          height={labelHeight - SHIP_LABEL_PADDING_Y}
                          fontSize={SHIP_LABEL_FONT_SIZE}
                          fontStyle="bold"
                          fill="#ffd7d7"
                          align="center"
                          text={labelText}
                        />
                      </Label>
                    </Group>
                  </Group>
                )}
              </Group>
            );
          })}

          {/* Mining lasers */}
          {filteredShips.map((ship: TaggedShip) => {
            if (!ship.cooldown || ship.cooldown.remainingSeconds <= 0) return null;

            const waypoint = waypoints.get(ship.nav.waypointSymbol);
            if (!waypoint) return null;

            if (ship.nav.status !== 'IN_ORBIT') return null;
            if (!MINING_WAYPOINT_TYPES.has(waypoint.type)) return null;

            const targetPosition = Ship.getPosition(ship, waypoints);
            const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
            const time = animationFrame / 60; // Convert to seconds

            return (
              <Group key={`laser-${ship.symbol}`}>
                {[0, 1].map((i) => {
                  const phase = (time * 3 + i * 0.7) % 1;
                  const alpha = 0.5 + Math.sin(phase * Math.PI * 2) * 0.4;
                  const angle = Math.atan2(waypoint.y - position.y, waypoint.x - position.x);
                  const directionX = Math.cos(angle);
                  const directionY = Math.sin(angle);
                  const angleOffset = i === 0 ? -0.12 : 0.12;
                  const beamAngle = angle + angleOffset;
                  const beamDirX = Math.cos(beamAngle);
                  const beamDirY = Math.sin(beamAngle);
                  const surfaceRadius = Math.max(Waypoint.getRadius(waypoint) - 1, 0);
                  const centerOffsetX = position.x - waypoint.x;
                  const centerOffsetY = position.y - waypoint.y;
                  const b = 2 * (beamDirX * centerOffsetX + beamDirY * centerOffsetY);
                  const c = centerOffsetX * centerOffsetX + centerOffsetY * centerOffsetY - surfaceRadius * surfaceRadius;
                  const discriminant = b * b - 4 * c;
                  let beamEndX = waypoint.x - directionX * surfaceRadius;
                  let beamEndY = waypoint.y - directionY * surfaceRadius;

                  if (discriminant >= 0) {
                    const sqrtDisc = Math.sqrt(discriminant);
                    const t1 = (-b - sqrtDisc) / 2;
                    const t2 = (-b + sqrtDisc) / 2;
                    const t = [t1, t2]
                      .filter((value) => value > 0)
                      .sort((aVal, bVal) => aVal - bVal)[0];
                    if (typeof t === 'number') {
                      beamEndX = position.x + beamDirX * t;
                      beamEndY = position.y + beamDirY * t;
                    }
                  }

                  return (
                    <Line
                      key={i}
                      points={[position.x, position.y, beamEndX, beamEndY]}
                      stroke="#ff0000"
                      strokeWidth={0.08}
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
            <div
              className={
                selectionOverlay.type === 'ship'
                  ? 'absolute inset-0 rounded-lg border border-red-400/80 shadow-[0_0_12px_rgba(248,113,113,0.8)]'
                  : 'absolute inset-0 rounded-lg border border-sky-300/80 shadow-[0_0_12px_rgba(125,211,252,0.8)]'
              }
            />
            <div
              className={
                selectionOverlay.type === 'ship'
                  ? 'absolute inset-[3px] rounded-lg border border-red-500/50'
                  : 'absolute inset-[3px] rounded-lg border border-sky-500/40'
              }
            />
            {[['top', 'left'], ['top', 'right'], ['bottom', 'left'], ['bottom', 'right']].map(([vertical, horizontal]) => (
              <div
                key={`${vertical}-${horizontal}`}
                className={
                  selectionOverlay.type === 'ship'
                    ? 'absolute h-2 w-2 border-red-200/90'
                    : 'absolute h-2 w-2 border-sky-200/90'
                }
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
      )}

      {/* Ship tooltip */}
      {shipTooltip && shipTooltipPosition && (
        <div
          className="absolute bg-gray-900 bg-opacity-70 border border-red-500/70 rounded-lg p-2.5 text-xs min-w-[220px] max-w-[300px] pointer-events-none z-30 shadow-xl backdrop-blur-sm"
          style={{
            left: `${shipTooltipPosition.left}px`,
            top: `${shipTooltipPosition.top}px`,
            transform: 'translate(-100%, -100%)',
          }}
        >
          <div className="flex flex-col gap-1 mb-3">
            <div className="flex items-center gap-2">
              <span className="text-sm font-bold text-white leading-snug">{shipTooltip.symbol}</span>
              <span className="text-[10px] font-semibold text-red-200 bg-red-500/15 border border-red-500/40 rounded-full px-1.5 py-0.5 whitespace-nowrap">
                {shipTooltip.role}
              </span>
            </div>
            <div className="text-[11px] text-gray-200 flex items-center justify-between gap-2">
              <span className="text-red-200 font-semibold truncate uppercase">{shipTooltip.statusText}</span>
              <span className="text-gray-400 text-[10px] uppercase whitespace-nowrap">{shipTooltip.flightMode}</span>
            </div>
          </div>

          <div className="space-y-3 text-gray-200">
            {shipTooltip.cooldownSeconds !== null && (
              <div>
                <div className="text-[10px] uppercase text-gray-400">Cooldown</div>
                <div className="text-xs">{shipTooltip.cooldownSeconds}s</div>
              </div>
            )}

            {shipTooltip.routeSummary && (
              <div>
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

            <div>
              <div className="flex items-center justify-between text-[10px] uppercase text-gray-400">
                <span>Fuel</span>
                <span className="text-xs text-red-200 font-semibold">
                  {shipTooltip.fuelCurrent} / {shipTooltip.fuelCapacity} ({shipTooltip.fuelPercent}%)
                </span>
              </div>
              <div className="w-full bg-red-900/40 h-1.5 rounded-full mt-1">
                <div
                  className="h-1.5 rounded-full"
                  style={{
                    width: `${Math.min(100, Math.max(0, shipTooltip.fuelPercent))}%`,
                    backgroundColor: getFuelBarColor(shipTooltip.fuelPercent),
                  }}
                />
              </div>
            </div>

            <div>
              <div className="flex items-center justify-between text-[10px] uppercase text-gray-400">
                <span>Cargo</span>
                <span className="text-xs text-red-200 font-semibold">
                  {shipTooltip.cargoUnits} / {shipTooltip.cargoCapacity} ({shipTooltip.cargoPercent}%)
                </span>
              </div>
              <div className="w-full bg-red-900/40 h-1.5 rounded-full mt-1">
                <div
                  className="bg-red-500 h-1.5 rounded-full"
                  style={{ width: `${Math.min(100, Math.max(0, shipTooltip.cargoPercent))}%` }}
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
      {waypointTooltip && waypointTooltipPosition && (
        <div
          className="absolute bg-gray-900 bg-opacity-70 border border-sky-500/60 rounded-lg p-3 text-xs min-w-[220px] max-w-[280px] pointer-events-none z-30 shadow-2xl backdrop-blur"
          style={{
            left: `${waypointTooltipPosition.left}px`,
            top: `${waypointTooltipPosition.top}px`,
            transform: 'translate(-50%, -110%)',
          }}
        >
          <div className="flex items-start justify-between gap-2 mb-2">
            <div>
              <div className="text-sm font-bold text-white leading-snug">{waypointTooltip.symbol}</div>
              <div className="text-[11px] text-sky-200 uppercase tracking-wide">
                {waypointTooltip.type.replace(/_/g, ' ')}
              </div>
            </div>
            {waypointTooltip.faction && (
              <span className="text-[10px] font-semibold text-sky-200 bg-sky-500/10 border border-sky-500/40 rounded-full px-1.5 py-0.5 whitespace-nowrap">
                {waypointTooltip.faction.symbol}
              </span>
            )}
          </div>

          <div className="grid grid-cols-2 gap-1 text-zinc-300 mb-2">
            {waypointTooltip.traits.length === 0 ? (
              <span className="col-span-2 text-[8px] text-zinc-500">No notable traits</span>
            ) : (
              waypointTooltip.traits.map((trait, index) => (
                <span
                  key={`${waypointTooltip.symbol}-trait-${index}`}
                  className="bg-sky-500/10 border border-sky-500/30 text-[8px] text-sky-100 rounded px-1 py-0.5"
                >
                  {trait.symbol.replace(/_/g, ' ')}
                </span>
              ))
            )}
          </div>

          {waypointTooltip.hasMarketplace && (
            <div className="border-t border-sky-500/40 pt-2 mt-2">
              <div className="flex items-center justify-between mb-1">
                <span className="text-[10px] uppercase text-sky-300 tracking-wide">Marketplace</span>
                <span className="text-sm">🏪</span>
              </div>
              {waypointTooltip.marketData ? (
                <div className="space-y-1">
                  <div className="flex justify-between text-[11px] text-sky-100">
                    <span>Imports</span>
                    <span>{waypointTooltip.marketData.importsCount}</span>
                  </div>
                  <div className="flex justify-between text-[11px] text-rose-100">
                    <span>Exports</span>
                    <span>{waypointTooltip.marketData.exportsCount}</span>
                  </div>
                  {waypointTooltip.marketData.opportunities.length > 0 && (
                    <div>
                      <div className="text-[10px] uppercase text-emerald-300 mb-0.5">Opportunities</div>
                      <ul className="list-disc list-inside text-[11px] text-emerald-200 space-y-0.5">
                        {waypointTooltip.marketData.opportunities.map((opp, index) => (
                          <li key={`${waypointTooltip.symbol}-opp-${index}`}>{opp}</li>
                        ))}
                      </ul>
                    </div>
                  )}
                </div>
              ) : (
                <div className="text-[11px] text-zinc-500">
                  Market intel unavailable. Enable Markets overlay for trade insights.
                </div>
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
