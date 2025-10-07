import { useEffect, useRef, useState } from 'react';
import Konva from 'konva';
import { CANVAS_CONSTANTS } from '../constants/canvas';

export interface KonvaStageConfig {
  width: number;
  height: number;
  minScale?: number;
  maxScale?: number;
  zoomInFactor?: number;
  zoomOutFactor?: number;
}

export interface KonvaStageResult {
  stage: Konva.Stage | null;
  layer: Konva.Layer | null;
  isReady: boolean;
}

/**
 * Custom hook for managing Konva Stage with pan and zoom
 * Replaces usePixiCanvas with Konva equivalent
 */
export function useKonvaStage(
  containerRef: React.RefObject<HTMLDivElement>,
  config: KonvaStageConfig
): KonvaStageResult {
  const stageRef = useRef<Konva.Stage | null>(null);
  const layerRef = useRef<Konva.Layer | null>(null);
  const [isReady, setIsReady] = useState(false);

  const {
    width,
    height,
    minScale = CANVAS_CONSTANTS.MIN_ZOOM_SPACE,
    maxScale = CANVAS_CONSTANTS.MAX_ZOOM_SPACE,
    zoomInFactor = CANVAS_CONSTANTS.ZOOM_IN_FACTOR,
    zoomOutFactor = CANVAS_CONSTANTS.ZOOM_OUT_FACTOR,
  } = config;

  useEffect(() => {
    if (!containerRef.current) return;

    // Create Konva stage
    const stage = new Konva.Stage({
      container: containerRef.current,
      width,
      height,
    });

    // Create main layer
    const layer = new Konva.Layer();
    stage.add(layer);

    stageRef.current = stage;
    layerRef.current = layer;

    // Initialize layer at canvas center
    layer.x(width / 2);
    layer.y(height / 2);

    // Pan and zoom state
    let isDragging = false;
    let dragStart = { x: 0, y: 0 };

    // Mouse wheel zoom (centered on screen)
    const handleWheel = (e: WheelEvent) => {
      e.preventDefault();

      const delta = e.deltaY > 0 ? zoomOutFactor : zoomInFactor;
      const currentScale = layer.scaleX();
      const newScale = Math.max(minScale, Math.min(maxScale, currentScale * delta));
      const scaleDelta = newScale / currentScale;

      // Screen center point
      const centerX = width / 2;
      const centerY = height / 2;

      // Calculate position adjustment to keep center fixed
      const dx = centerX - layer.x();
      const dy = centerY - layer.y();

      // Update scale
      layer.scale({ x: newScale, y: newScale });

      // Adjust position
      layer.x(centerX - dx * scaleDelta);
      layer.y(centerY - dy * scaleDelta);

      layer.batchDraw();
    };

    // Mouse drag pan
    const handleMouseDown = (e: MouseEvent) => {
      isDragging = true;
      dragStart = { x: e.clientX - layer.x(), y: e.clientY - layer.y() };
    };

    const handleMouseMove = (e: MouseEvent) => {
      if (!isDragging) return;
      layer.x(e.clientX - dragStart.x);
      layer.y(e.clientY - dragStart.y);
      layer.batchDraw();
    };

    const handleMouseUp = () => {
      isDragging = false;
    };

    // Handle window resize
    const handleResize = () => {
      if (stageRef.current) {
        stageRef.current.width(width);
        stageRef.current.height(height);
        stageRef.current.batchDraw();
      }
    };

    // Add event listeners to the stage container
    const stageContainer = stage.container();
    stageContainer.addEventListener('wheel', handleWheel);
    stageContainer.addEventListener('mousedown', handleMouseDown);
    stageContainer.addEventListener('mousemove', handleMouseMove);
    stageContainer.addEventListener('mouseup', handleMouseUp);
    stageContainer.addEventListener('mouseleave', handleMouseUp);
    window.addEventListener('resize', handleResize);

    setIsReady(true);

    // Cleanup
    return () => {
      stageContainer.removeEventListener('wheel', handleWheel);
      stageContainer.removeEventListener('mousedown', handleMouseDown);
      stageContainer.removeEventListener('mousemove', handleMouseMove);
      stageContainer.removeEventListener('mouseup', handleMouseUp);
      stageContainer.removeEventListener('mouseleave', handleMouseUp);
      window.removeEventListener('resize', handleResize);

      stage.destroy();
      stageRef.current = null;
      layerRef.current = null;
      setIsReady(false);
    };
  }, [containerRef, width, height, minScale, maxScale, zoomInFactor, zoomOutFactor]);

  return {
    stage: stageRef.current,
    layer: layerRef.current,
    isReady,
  };
}
