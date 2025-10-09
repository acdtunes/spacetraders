import { useCallback, useEffect, useState } from 'react';
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

export function useKonvaStage({
  containerRef,
  layerRef,
  stageRef,
  onAnimationTick,
}: UseKonvaStageParams): StageSize {
  const [stageSize, setStageSize] = useState<StageSize>({ width: 0, height: 0 });

  const layer = layerRef.current;

  useEffect(() => {
    if (!layer) return;

    const animation = new Konva.Animation(() => {
      onAnimationTick();
    }, layer);

    animation.start();

    return () => {
      animation.stop();
    };
  }, [layer, onAnimationTick]);

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
