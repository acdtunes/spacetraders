import { useCallback, useEffect, useState, useRef } from 'react';
import Konva from 'konva';

interface StageSize {
  width: number;
  height: number;
}

interface UseKonvaStageParams {
  containerRef: React.RefObject<HTMLDivElement | null>;
  layerRef: React.RefObject<Konva.Layer | null>;
  stageRef: React.RefObject<Konva.Stage | null>;
  onAnimationTick: () => void;
}

const isDev = import.meta.env.DEV;

export function useKonvaStage({
  containerRef,
  layerRef,
  stageRef,
  onAnimationTick,
}: UseKonvaStageParams): StageSize {
  const [stageSize, setStageSize] = useState<StageSize>({ width: 0, height: 0 });
  const [layerReady, setLayerReady] = useState(false);
  const animationRunningRef = useRef(false);

  // Monitor layer availability
  useEffect(() => {
    const checkLayer = () => {
      const layer = layerRef.current;
      if (layer && !layerReady) {
        if (isDev) console.log('useKonvaStage: Layer became ready');
        setLayerReady(true);
      } else if (!layer && layerReady) {
        if (isDev) console.warn('useKonvaStage: Layer became unavailable');
        setLayerReady(false);
      }
    };

    checkLayer();

    // Poll for layer availability (fallback for race conditions during mount)
    const interval = setInterval(checkLayer, 100);

    return () => clearInterval(interval);
  }, [layerRef, layerReady]);

  // Animation loop
  useEffect(() => {
    if (!layerReady) {
      if (isDev && !animationRunningRef.current) {
        console.warn('useKonvaStage: Waiting for layer to be ready...');
      }
      return;
    }

    const layer = layerRef.current;
    if (!layer) {
      if (isDev) console.error('useKonvaStage: Layer ready but not available (race condition)');
      return;
    }

    let animation: Konva.Animation | null = null;

    try {
      animation = new Konva.Animation(() => {
        try {
          onAnimationTick();
        } catch (error) {
          console.error('Animation tick error:', error);
          // Don't stop animation on tick errors - just log and continue
        }
      }, layer);

      animation.start();
      animationRunningRef.current = true;
      if (isDev) console.log('✓ Konva animation started successfully');
    } catch (error) {
      console.error('Failed to start Konva animation:', error);
      animationRunningRef.current = false;
      return;
    }

    return () => {
      if (animation) {
        animation.stop();
        animationRunningRef.current = false;
        if (isDev) console.log('Konva animation stopped');
      }
    };
  }, [layerReady, layerRef, onAnimationTick]);

  const updateSize = useCallback((width: number, height: number) => {
    setStageSize((prev) => {
      if (prev.width === width && prev.height === height) {
        return prev;
      }
      return { width, height };
    });
  }, []);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || typeof ResizeObserver === 'undefined') {
      return;
    }

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
  }, [containerRef, updateSize]);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) {
      return;
    }

    if (stageSize.width > 0 && stageSize.height > 0) {
      stage.width(stageSize.width);
      stage.height(stageSize.height);
    }
  }, [stageRef, stageSize.height, stageSize.width]);

  return stageSize;
}
