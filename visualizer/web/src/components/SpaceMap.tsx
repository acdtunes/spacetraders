import { useEffect, useRef, useState, forwardRef, useImperativeHandle, useMemo, useCallback } from 'react';
import { Stage, Layer, Group, Circle, Text, Line } from 'react-konva';
import Konva from 'konva';
import { useStore } from '../store/useStore';
import { getWaypoints } from '../services/api';
import { getWaypointOpportunities, formatOpportunity } from '../domain/market';
import { Ship, Waypoint, ShipQueries, WaypointQueries, ViewportBounds } from '../domain';
import type { ShipPositionOptions } from '../domain';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';
import { hashString } from '../utils/hash';
import { getTourId } from '../utils/tourHelpers';
import { WaypointSprite } from './WaypointSprite';
import { ShipLayer } from './ShipLayer';
import { MiningLaserLayer } from './MiningLaserLayer';
import { ShipTrailLayer } from './ShipTrailLayer';
import { MarketFreshnessRing } from './MarketFreshnessRing';
import { ScoutTourLayer } from './ScoutTourLayer';
import { TradeRouteLayer } from './TradeRouteLayer';
import { MiningLoopLayer } from './MiningLoopLayer';
import { useWaypointTooltipAnchor } from '../hooks/useWaypointTooltipAnchor';
import { useGridLines } from '../hooks/useGridLines';
import { useShipTrailSampler } from '../hooks/useShipTrailSampler';
import { useSpaceMapOverlays, type SelectedMapObject } from '../hooks/useSpaceMapOverlays';
import { useKonvaStage } from '../hooks/useKonvaStage';
import { SelectionOverlayLazy, ShipTooltipOverlayLazy, WaypointTooltipOverlayLazy } from './SpaceMapLazyOverlays';
import Minimap from './Minimap';
import type { Waypoint as WaypointType, TaggedShip, ShipNavStatus } from '../types/spacetraders';
import type { RouteVectorsProps } from './RouteVectors';
import LoaderScreen from './LoaderScreen';

type RouteVectorsComponentType = (props: RouteVectorsProps) => JSX.Element | null;

const TRAIL_SAMPLE_RATE = 4;

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
  jumpGate: ['waytpoint-jumpgate.png'],
};

const SHIP_ASSET_VARIANTS: Record<string, string[]> = {
  command: ['ship-command-frigate-2.png'],
  hauler: ['ship-light-hauler-1.png'],
  mining: ['ship-mining-drone-1.png', 'ship-mining-drone-2.png'],
  probe: ['ship-probe-2.png'],
  satellite: ['ship-satellite-1.png', 'ship-satellite-2.png'],
  station: ['ship-space-station-1.png', 'ship-space-station-2.png'],
};

const DEFAULT_WAYPOINT_ASSET = 'waypoint-planet-rocky-1.png';
const DEFAULT_SHIP_ASSET = 'ship-command-frigate-2.png';
const DEFAULT_SHIP_SPRITE_SIZE = 18;
const BASE_SHIP_SIZE = DEFAULT_SHIP_SPRITE_SIZE / 10;

const getShipSize = (role: string | undefined): number => {
  if (role === 'EXPLORER') {
    return BASE_SHIP_SIZE / 2;  // 2x smaller
  }
  if (role === 'SURVEYOR') {
    return BASE_SHIP_SIZE / 4;  // 4x smaller
  }
  if (role === 'PATROL') {
    return BASE_SHIP_SIZE / 2;  // 2x smaller
  }
  if (role === 'EXCAVATOR') {
    return BASE_SHIP_SIZE * 0.75;  // 1.33x smaller
  }
  if (role === 'REFINERY') {
    return BASE_SHIP_SIZE / 3;  // 3x smaller
  }
  return BASE_SHIP_SIZE;
};
const SHIP_POSITION_SMOOTHING_MS = 900;
const SHIP_POSITION_DISTANCE_THRESHOLD = 2;
const TARGET_FRAME_RATE = 60;
const FRAMES_PER_MS = TARGET_FRAME_RATE / 1000;
const getHighResTimestamp = (): number => {
  if (typeof performance !== 'undefined' && typeof performance.now === 'function') {
    return performance.now();
  }
  return Date.now();
};

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
  const animationStartRef = useRef<number>(getHighResTimestamp());

  const {
    currentSystem,
    waypoints,
    ships,
    markets,
    marketIntel,
    showMapOverlays,
    showWaypointNames,
    showShipNames,
    showDestinationRoutes,
    setWaypoints,
    trails,
    addTrailPosition,
    clearTrail,
    filterStatus,
    filterAgents,
    filterShipRoles,
    filterWaypointTypes,
    selectedShip,
    selectedWaypoint,
    setSelectedShip,
    setSelectedWaypoint,
    assignments,
    marketFreshness,
    showMarketFreshness,
    toggleMarketFreshness,
    scoutTours,
    showScoutTours,
    tradeOpportunities,
    showTradeRoutes,
    showMiningRoutes,
    visibleTours,
    toggleTourVisibility,
    showAllTours,
    hideAllTours,
    shipFocusRequest,
    clearShipFocusRequest,
  } = useStore();

  const [hoveredShip, setHoveredShip] = useState<string | null>(null);
  const [selectedObject, setSelectedObject] = useState<SelectedMapObject | null>(null);
  const [viewportBounds, setViewportBounds] = useState({ x: 0, y: 0, width: 0, height: 0, scale: 1 });
  const [animationFrame, setAnimationFrame] = useState(0);
  const [RouteVectorsComponent, setRouteVectorsComponent] = useState<RouteVectorsComponentType | null>(null);
  const [backgroundPosition, setBackgroundPosition] = useState({ x: 0, y: 0 });
  const [isLoadingWaypoints, setIsLoadingWaypoints] = useState(false);

  const currentScale = viewportBounds.scale || 1;
  const frameTimestamp = useMemo(() => Date.now(), [animationFrame]);

  const handleAnimationTick = useCallback(() => {
    const timestamp = getHighResTimestamp();
    const elapsed = timestamp - animationStartRef.current;
    const nextFrame = Math.max(0, Math.round(elapsed * FRAMES_PER_MS));
    setAnimationFrame((prev) => (nextFrame <= prev ? prev : nextFrame));
  }, []);

  const stageSize = useKonvaStage({
    containerRef,
    layerRef,
    stageRef,
    onAnimationTick: handleAnimationTick,
  });

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

  useEffect(() => {
    let isActive = true;

    if (showDestinationRoutes && !RouteVectorsComponent) {
      import('./RouteVectors')
        .then((module) => {
          if (isActive) {
            setRouteVectorsComponent(() => module.RouteVectors);
          }
        })
        .catch((error) => {
          const isDevEnvironment = Boolean((import.meta as any)?.env?.DEV);
          if (isDevEnvironment) {
            console.error('Failed to load route overlay module', error);
          }
        });
    }

    return () => {
      isActive = false;
    };
  }, [showDestinationRoutes, RouteVectorsComponent]);

  // Update viewport bounds for minimap
  const updateViewportBounds = useCallback(() => {
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

    // Update background position to pan with the map
    // Divide by a factor to slow down the parallax effect
    const parallaxFactor = 0.5;
    setBackgroundPosition({
      x: layer.x() * parallaxFactor,
      y: layer.y() * parallaxFactor,
    });
  }, []);

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

  const handleFocusOn = useCallback((x: number, y: number, scale?: number) => {
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
  }, [updateViewportBounds]);

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
      if (e.key === 'Escape' && (selectedObject || selectedShip || selectedWaypoint)) {
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
  }, [selectedObject, selectedShip, selectedWaypoint, setSelectedShip, setSelectedWaypoint]);

  // Load waypoints when system changes
  useEffect(() => {
    let isCancelled = false;

    if (!currentSystem) {
      setIsLoadingWaypoints(false);
      return;
    }

    setIsLoadingWaypoints(true);

    getWaypoints(currentSystem)
      .then((data) => {
        if (!isCancelled) {
          setWaypoints(data);
        }
      })
      .catch((error) => {
        if (!isCancelled) {
          console.error('Failed to load waypoints:', error);
        }
      })
      .finally(() => {
        if (!isCancelled) {
          setIsLoadingWaypoints(false);
        }
      });
    return () => {
      isCancelled = true;
    };
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

  // Filter ships using domain queries
  const filteredShips = useMemo(() => {
    let result = ShipQueries.filter(ships, {
      systemSymbol: currentSystem ?? undefined,
      statuses: filterStatus,
    }) as TaggedShip[];

    if (filterAgents.size > 0) {
      result = result.filter((ship) => !ship.agentId || filterAgents.has(ship.agentId));
    }

    if (filterShipRoles.size > 0) {
      result = result.filter((ship) => {
        const role = ship.registration?.role?.toUpperCase() ?? 'UNKNOWN';
        return filterShipRoles.has(role);
      });
    }

    return result;
  }, [ships, currentSystem, filterStatus, filterAgents, filterShipRoles]);

  // Filter waypoints using domain queries
  const filteredWaypoints = useMemo(() => {
    return WaypointQueries.filterByType(
      Array.from(waypoints.values()),
      filterWaypointTypes
    );
  }, [waypoints, filterWaypointTypes]);

  const orbitalClusters = useMemo(() => {
    const coordinateGroups = new Map<string, WaypointType[]>();
    waypoints.forEach((waypoint) => {
      const key = `${waypoint.x},${waypoint.y}`;
      const existing = coordinateGroups.get(key);
      if (existing) {
        existing.push(waypoint);
      } else {
        coordinateGroups.set(key, [waypoint]);
      }
    });

    const clusters = new Map<
      string,
      {
        id: string;
        center: { x: number; y: number };
        members: string[];
      }
    >();

    coordinateGroups.forEach((group, key) => {
      if (group.length <= 1) {
        return;
      }

      const [first] = group;
      clusters.set(`cluster-${key}`, {
        id: `cluster-${key}`,
        center: { x: first.x, y: first.y },
        members: group.map((waypoint) => waypoint.symbol),
      });
    });

    return clusters;
  }, [waypoints]);

  const orbitalDisplayOffsets = useMemo(() => {
    const offsets = new Map<string, { x: number; y: number }>();

    orbitalClusters.forEach((cluster) => {
      if (cluster.members.length <= 1) {
        return;
      }

      const resolvedMembers = cluster.members
        .map((symbol) => waypoints.get(symbol))
        .filter((waypoint): waypoint is WaypointType => waypoint !== undefined);

      if (resolvedMembers.length <= 1) {
        return;
      }

      const getTypePriority = (type: string) => {
        switch (type) {
          case 'PLANET':
          case 'GAS_GIANT':
          case 'STAR':
            return 0;
          case 'MOON':
          case 'ASTEROID':
          case 'ASTEROID_FIELD':
            return 1;
          case 'ORBITAL_STATION':
          case 'ASTEROID_BASE':
          case 'ARTIFICIAL_GRAVITY_WELL':
            return 2;
          default:
            return 3;
        }
      };

      resolvedMembers.sort((a, b) => {
        const priorityDiff = getTypePriority(a.type) - getTypePriority(b.type);
        if (priorityDiff !== 0) return priorityDiff;
        return a.symbol.localeCompare(b.symbol);
      });

      const primary = resolvedMembers[0];
      offsets.set(primary.symbol, { x: 0, y: 0 });

      const buckets: Record<
        string,
        {
          radius: number;
          startAngle: number;
          endAngle: number;
          waypoints: WaypointType[];
        }
      > = {
        MOON: {
          radius: 24, // More realistic - like Jupiter's outer moons (was 11)
          startAngle: -Math.PI / 3,
          endAngle: Math.PI / 3,
          waypoints: [],
        },
        ORBITAL_STATION: {
          radius: 16, // Closer to parent planet (was 15)
          startAngle: (3 * Math.PI) / 4,
          endAngle: (5 * Math.PI) / 4,
          waypoints: [],
        },
        DEFAULT: {
          radius: 18, // Medium distance (was 12)
          startAngle: Math.PI / 6,
          endAngle: (11 * Math.PI) / 6,
          waypoints: [],
        },
      };

      resolvedMembers.slice(1).forEach((waypoint) => {
        if (waypoint.type === 'MOON') {
          buckets.MOON.waypoints.push(waypoint);
        } else if (waypoint.type === 'ORBITAL_STATION') {
          buckets.ORBITAL_STATION.waypoints.push(waypoint);
        } else {
          buckets.DEFAULT.waypoints.push(waypoint);
        }
      });

      const placeBucket = ({
        radius,
        startAngle,
        endAngle,
        waypoints: bucketMembers,
      }: {
        radius: number;
        startAngle: number;
        endAngle: number;
        waypoints: WaypointType[];
      }) => {
        if (bucketMembers.length === 0) return;

        if (bucketMembers.length === 1) {
          const midAngle = (startAngle + endAngle) / 2;
          const waypoint = bucketMembers[0];
          offsets.set(waypoint.symbol, {
            x: Math.cos(midAngle) * radius,
            y: Math.sin(midAngle) * radius,
          });
          return;
        }

        const span = endAngle - startAngle;
        const step = span / (bucketMembers.length - 1);

        bucketMembers.forEach((waypoint, index) => {
          const angle = startAngle + step * index;

          // Vary radius for each waypoint - create realistic orbital distances
          // Inner moons closer, outer moons farther (like Io, Europa, Ganymede, Callisto)
          const radiusVariation = (index / Math.max(1, bucketMembers.length - 1)) * 0.6; // 0 to 60% variation
          const waypointRadius = radius * (0.7 + radiusVariation); // Range: 70% to 130% of base radius

          offsets.set(waypoint.symbol, {
            x: Math.cos(angle) * waypointRadius,
            y: Math.sin(angle) * waypointRadius,
          });
        });
      };

      placeBucket(buckets.MOON);
      placeBucket(buckets.ORBITAL_STATION);
      placeBucket(buckets.DEFAULT);
    });

    return offsets;
  }, [orbitalClusters, waypoints]);

  // Get set of all market waypoints in visible tours
  const visibleTourMarkets = useMemo(() => {
    const markets = new Set<string>();
    scoutTours.forEach((tour) => {
      const tourId = getTourId(tour);
      if (visibleTours.has(tourId)) {
        tour.markets.forEach((market) => markets.add(market));
      }
    });
    return markets;
  }, [scoutTours, visibleTours]);

  const getWaypointDisplayPosition = useCallback(
    (waypoint: WaypointType): { x: number; y: number } => {
      const offset = orbitalDisplayOffsets.get(waypoint.symbol);
      if (!offset) {
        return { x: waypoint.x, y: waypoint.y };
      }

      return {
        x: waypoint.x + offset.x,
        y: waypoint.y + offset.y,
      };
    },
    [orbitalDisplayOffsets]
  );

  // Start animation loop for ships
  useShipTrailSampler({
    animationFrame,
    sampleRate: TRAIL_SAMPLE_RATE,
    ships,
    waypoints,
    currentSystem,
    addTrailPoint: addTrailPosition,
    clearTrail,
    resolveWaypointPosition: getWaypointDisplayPosition,
  });

  const selectWaypointAsset = useCallback((waypoint: WaypointType): string => {
    const traitSymbols = (waypoint.traits ?? []).map((trait) => trait.symbol.toUpperCase());
    const hasTrait = (...keywords: string[]) =>
      traitSymbols.some((trait) => keywords.some((keyword) => trait.includes(keyword)));

    let variantKey: string;

    if (waypoint.type === 'JUMP_GATE') {
      variantKey = 'jumpGate';
    } else if (
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

  const shipPositionOptions = useMemo<ShipPositionOptions>(
    () => ({
      waypointPositionResolver: getWaypointDisplayPosition,
    }),
    [getWaypointDisplayPosition]
  );

  useEffect(() => {
    if (!shipFocusRequest) {
      return;
    }

    if (!stageSize.width || !stageSize.height) {
      return;
    }

    if (!layerRef.current || !stageRef.current) {
      return;
    }

    const ship = ships.find((candidate) => candidate.symbol === shipFocusRequest.symbol);
    if (!ship) {
      clearShipFocusRequest();
      return;
    }

    const targetPosition = Ship.getPosition(ship, waypoints, shipPositionOptions);
    const renderPosition = getShipRenderPosition(ship, targetPosition, Date.now());
    const zoom = shipFocusRequest.zoom ?? VIEWPORT_CONSTANTS.SHIP_FOCUS_ZOOM;

    handleFocusOn(renderPosition.x, renderPosition.y, zoom);
    clearShipFocusRequest();
  }, [
    shipFocusRequest,
    ships,
    waypoints,
    getShipRenderPosition,
    handleFocusOn,
    clearShipFocusRequest,
    stageSize.width,
    stageSize.height,
    shipPositionOptions,
  ]);

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

  const gridLines = useGridLines(waypoints, viewportBounds.scale);

  const { anchor: waypointTooltipAnchor, showForWaypoint, clearAnchor } = useWaypointTooltipAnchor({
    selectedObject,
    selectedWaypoint,
    waypoints,
    getWaypointPosition: getWaypointDisplayPosition,
  });

  // Get waypoint tooltip data
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

  const {
    selectionOverlays,
    shipTooltip,
    shipTooltipPosition,
    waypointTooltip,
    waypointTooltipPosition,
  } = useSpaceMapOverlays({
    hoveredShip,
    selectedObject,
    selectedShip,
    selectedWaypoint,
    ships,
    waypoints,
    markets,
    marketIntel,
    projectToScreen,
    getWaypointPosition: getWaypointDisplayPosition,
    getShipRenderPosition,
    frameTimestamp,
    waypointTooltipAnchor,
    shipTooltipOffset: { x: SHIP_TOOLTIP_OFFSET_X, y: SHIP_TOOLTIP_OFFSET_Y },
    waypointTooltipOffset: { x: WAYPOINT_TOOLTIP_OFFSET_X, y: WAYPOINT_TOOLTIP_OFFSET_Y },
    getWaypointOpportunities,
    formatOpportunity,
  });

  return (
    <div
      ref={containerRef}
      className="relative w-full h-full cursor-grab active:cursor-grabbing"
      style={{
        backgroundImage: 'url(/assets/space.png)',
        backgroundSize: '1024px 1024px',
        backgroundPosition: `${backgroundPosition.x}px ${backgroundPosition.y}px`,
        backgroundRepeat: 'repeat',
      }}
    >
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
              clearAnchor();
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
                    if (container) container.style.cursor = 'grab';
                  }}
                  onClick={() => {
                    setSelectedObject({ type: 'waypoint', symbol: waypoint.symbol, x: waypoint.x, y: waypoint.y });
                    setSelectedWaypoint(waypoint);
                    showForWaypoint(waypoint);
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

                {/* Market freshness ring - shows for all markets with freshness data */}
                {hasMarketplace && showMarketFreshness && marketFreshness.has(waypoint.symbol) && (
                  <MarketFreshnessRing
                    x={x}
                    y={y}
                    radius={radius}
                    lastUpdated={marketFreshness.get(waypoint.symbol)?.last_updated || null}
                    currentScale={currentScale}
                    animationFrame={animationFrame}
                  />
                )}

                {showWaypointNames && (
                  <Text
                    text={waypoint.symbol.split('-').pop() || waypoint.symbol}
                    x={x + radius + 4 / currentScale}
                    y={y - 5 / currentScale}
                    fontSize={11 / currentScale}
                    fontFamily="'JetBrains Mono', 'Fira Code', 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', Consolas, 'Courier New', monospace"
                    fontStyle="bold"
                    fill="#6dd4d4"
                    shadowColor="#4aa8a8"
                    shadowBlur={6 / currentScale}
                    shadowOpacity={0.5}
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

          {/* Scout tour routes - animated dashed lines with colored markers */}
          {showScoutTours && (
            <ScoutTourLayer
              tours={scoutTours}
              waypoints={waypoints}
              currentScale={currentScale}
              animationFrame={animationFrame}
              visibleTours={visibleTours}
              getWaypointPosition={getWaypointDisplayPosition}
              orbitalClusters={orbitalClusters}
            />
          )}

          {/* Trade routes */}
          {showTradeRoutes && (
            <TradeRouteLayer
              opportunities={tradeOpportunities}
              waypoints={waypoints}
              currentScale={currentScale}
              animationFrame={animationFrame}
            />
          )}

          {/* Mining loops */}
          {showMiningRoutes && (
            <MiningLoopLayer
              assignments={assignments}
              waypoints={waypoints}
              currentScale={currentScale}
              animationFrame={animationFrame}
            />
          )}

          <ShipTrailLayer ships={filteredShips} trails={trails} animationFrame={animationFrame} />

          {/* Active route indicators */}
          {showDestinationRoutes && RouteVectorsComponent && (
            <RouteVectorsComponent
              ships={filteredShips}
              waypoints={waypoints}
              currentScale={currentScale}
              animationFrame={animationFrame}
              frameTimestamp={frameTimestamp}
              getShipRenderPosition={getShipRenderPosition}
              getWaypointPosition={getWaypointDisplayPosition}
            />
          )}

          <ShipLayer
            ships={filteredShips}
            trails={trails}
            waypoints={waypoints}
            frameTimestamp={frameTimestamp}
            currentScale={currentScale}
            showShipNames={showShipNames}
            getShipSize={getShipSize}
            getShipRenderPosition={getShipRenderPosition}
            selectShipAsset={selectShipAsset}
            projectToScreen={projectToScreen}
            projectToWorld={projectToWorld}
            onSelectShip={(ship, position) => {
              setSelectedObject({ type: 'ship', symbol: ship.symbol, x: position.x, y: position.y });
              setSelectedShip(ship);
            }}
            onHoverShip={setHoveredShip}
            assignments={assignments}
            shipPositionOptions={shipPositionOptions}
            getWaypointPosition={getWaypointDisplayPosition}
          />

          <MiningLaserLayer
            ships={filteredShips}
            waypoints={waypoints}
            animationFrame={animationFrame}
            frameTimestamp={frameTimestamp}
            getShipRenderPosition={getShipRenderPosition}
            getWaypointPosition={getWaypointDisplayPosition}
          />

        </Layer>
      </Stage>
      )}

      {selectionOverlays.map((overlay, index) => (
        <SelectionOverlayLazy key={`${overlay.type}-${index}`} overlay={overlay} />
      ))}

      {shipTooltip && shipTooltipPosition && (
        <ShipTooltipOverlayLazy tooltip={shipTooltip} position={shipTooltipPosition} />
      )}

      {waypointTooltip && waypointTooltipPosition && (
        <WaypointTooltipOverlayLazy tooltip={waypointTooltip} position={waypointTooltipPosition} />
      )}

      {isLoadingWaypoints && waypoints.size === 0 && (
        <LoaderScreen
          variant="overlay"
          className="z-50"
          title="Loading System Map"
          message={
            currentSystem
              ? `Syncing ${currentSystem} waypoints and ship telemetry`
              : 'Syncing system data'
          }
        />
      )}

      {!currentSystem && (
        <div className="absolute inset-0 flex items-center justify-center">
          <div className="bg-gray-800 bg-opacity-90 rounded-lg p-8 text-center">
            <h2 className="text-xl font-bold mb-2">No System Selected</h2>
            <p className="text-gray-400">Add an agent and select a system to begin</p>
          </div>
        </div>
      )}

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
