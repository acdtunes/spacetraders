import type { Position } from './ship';
import type Konva from 'konva';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';

/**
 * Minimap data structure
 */
export interface MinimapData {
  minX: number;
  minY: number;
  scale: number;
  padding: number;
  canvasSize: number;
}

/**
 * World bounds structure
 */
export interface WorldBounds {
  minX: number;
  maxX: number;
  minY: number;
  maxY: number;
}

/**
 * Viewport bounds value object
 * Encapsulates viewport calculations and transformations
 */
export class ViewportBounds {
  constructor(
    public readonly x: number,
    public readonly y: number,
    public readonly width: number,
    public readonly height: number,
    public readonly scale: number
  ) {}

  /**
   * Check if a point is within the viewport
   */
  contains(point: Position): boolean {
    const halfWidth = (this.width / this.scale) / 2;
    const halfHeight = (this.height / this.scale) / 2;

    return (
      point.x >= this.x - halfWidth &&
      point.x <= this.x + halfWidth &&
      point.y >= this.y - halfHeight &&
      point.y <= this.y + halfHeight
    );
  }

  /**
   * Convert world coordinates to minimap coordinates
   */
  toMinimapCoords(worldX: number, worldY: number, minimap: MinimapData): Position {
    return {
      x: minimap.padding + (worldX - minimap.minX) * minimap.scale,
      y: minimap.padding + (worldY - minimap.minY) * minimap.scale,
    };
  }

  /**
   * Convert minimap coordinates to world coordinates
   */
  fromMinimapCoords(minimapX: number, minimapY: number, minimap: MinimapData): Position {
    return {
      x: minimap.minX + (minimapX - minimap.padding) / minimap.scale,
      y: minimap.minY + (minimapY - minimap.padding) / minimap.scale,
    };
  }

  /**
   * Get viewport bounds as a rectangle
   */
  toRect(): { x: number; y: number; width: number; height: number } {
    const halfWidth = (this.width / this.scale) / 2;
    const halfHeight = (this.height / this.scale) / 2;

    return {
      x: this.x - halfWidth,
      y: this.y - halfHeight,
      width: this.width / this.scale,
      height: this.height / this.scale,
    };
  }

  /**
   * Get the center point of the viewport
   */
  getCenter(): Position {
    return { x: this.x, y: this.y };
  }

  /**
   * Convert screen coordinates to world coordinates
   */
  screenToWorld(screenX: number, screenY: number, stage: Konva.Stage): Position {
    const layer = stage.children[0] as Konva.Layer;
    if (!layer) return { x: 0, y: 0 };

    const transform = layer.getAbsoluteTransform().copy();
    transform.invert();
    const worldPos = transform.point({ x: screenX, y: screenY });

    return { x: worldPos.x, y: worldPos.y };
  }

  /**
   * Convert world coordinates to screen coordinates
   */
  worldToScreen(worldX: number, worldY: number, stage: Konva.Stage): Position {
    const layer = stage.children[0] as Konva.Layer;
    if (!layer) return { x: 0, y: 0 };

    const screenPos = layer.getAbsoluteTransform().point({ x: worldX, y: worldY });
    return { x: screenPos.x, y: screenPos.y };
  }

  /**
   * Clamp viewport position to world bounds
   */
  clampPosition(worldBounds: WorldBounds, padding: number = VIEWPORT_CONSTANTS.PAN_CLAMP_PADDING): ViewportBounds {
    const rect = this.toRect();

    // Calculate clamped bounds
    const minX = worldBounds.minX - padding;
    const maxX = worldBounds.maxX + padding;
    const minY = worldBounds.minY - padding;
    const maxY = worldBounds.maxY + padding;

    // Clamp center position
    const clampedX = Math.max(
      minX + rect.width / 2,
      Math.min(maxX - rect.width / 2, this.x)
    );
    const clampedY = Math.max(
      minY + rect.height / 2,
      Math.min(maxY - rect.height / 2, this.y)
    );

    return new ViewportBounds(clampedX, clampedY, this.width, this.height, this.scale);
  }

  /**
   * Create a new viewport with updated position
   */
  withPosition(x: number, y: number): ViewportBounds {
    return new ViewportBounds(x, y, this.width, this.height, this.scale);
  }

  /**
   * Create a new viewport with updated scale
   */
  withScale(scale: number): ViewportBounds {
    return new ViewportBounds(this.x, this.y, this.width, this.height, scale);
  }

  /**
   * Calculate viewport that fits all points
   */
  static fitPoints(
    points: Position[],
    width: number,
    height: number,
    padding: number = 50
  ): ViewportBounds {
    if (points.length === 0) {
      return new ViewportBounds(0, 0, width, height, 1);
    }

    let minX = Infinity;
    let maxX = -Infinity;
    let minY = Infinity;
    let maxY = -Infinity;

    points.forEach(point => {
      minX = Math.min(minX, point.x);
      maxX = Math.max(maxX, point.x);
      minY = Math.min(minY, point.y);
      maxY = Math.max(maxY, point.y);
    });

    const centerX = (minX + maxX) / 2;
    const centerY = (minY + maxY) / 2;

    const boundsWidth = maxX - minX + padding * 2;
    const boundsHeight = maxY - minY + padding * 2;

    const scaleX = width / boundsWidth;
    const scaleY = height / boundsHeight;
    const scale = Math.min(scaleX, scaleY, 10); // Max scale of 10

    return new ViewportBounds(centerX, centerY, width, height, scale);
  }

  /**
   * Calculate viewport centered on a cluster of points
   */
  static centerOnCluster(
    points: Position[],
    width: number,
    height: number,
    clusterRadius: number = VIEWPORT_CONSTANTS.CLUSTER_RADIUS
  ): ViewportBounds {
    if (points.length === 0) {
      return new ViewportBounds(0, 0, width, height, 1);
    }

    let maxDensity = 0;
    let clusterCenter = { x: 0, y: 0 };

    points.forEach(point => {
      const neighbors = points.filter(p => {
        const dx = p.x - point.x;
        const dy = p.y - point.y;
        const distance = Math.sqrt(dx * dx + dy * dy);
        return distance <= clusterRadius;
      });

      if (neighbors.length > maxDensity) {
        maxDensity = neighbors.length;
        const centerX = neighbors.reduce((sum, p) => sum + p.x, 0) / neighbors.length;
        const centerY = neighbors.reduce((sum, p) => sum + p.y, 0) / neighbors.length;
        clusterCenter = { x: centerX, y: centerY };
      }
    });

    return new ViewportBounds(clusterCenter.x, clusterCenter.y, width, height, 1);
  }

  /**
   * Zoom viewport at pointer position
   * Keeps the point under the pointer stationary during zoom
   */
  static zoomAtPointer(
    current: ViewportBounds,
    pointerWorldX: number,
    pointerWorldY: number,
    zoomFactor: number
  ): ViewportBounds {
    const newScale = Math.max(
      VIEWPORT_CONSTANTS.MIN_ZOOM,
      Math.min(VIEWPORT_CONSTANTS.MAX_ZOOM, current.scale * zoomFactor)
    );

    // Calculate new center to keep pointer position stationary
    // The key insight: as we zoom in (scale increases), the world viewport shrinks
    // So we need to move the center closer to the pointer position
    const dx = pointerWorldX - current.x;
    const dy = pointerWorldY - current.y;
    const scaleRatio = current.scale / newScale; // Inverse of scale change

    const newX = pointerWorldX - dx * scaleRatio;
    const newY = pointerWorldY - dy * scaleRatio;

    return new ViewportBounds(newX, newY, current.width, current.height, newScale);
  }

  /**
   * Zoom viewport at center
   */
  static zoomAtCenter(
    current: ViewportBounds,
    zoomFactor: number
  ): ViewportBounds {
    const newScale = Math.max(
      VIEWPORT_CONSTANTS.MIN_ZOOM,
      Math.min(VIEWPORT_CONSTANTS.MAX_ZOOM, current.scale * zoomFactor)
    );

    return new ViewportBounds(current.x, current.y, current.width, current.height, newScale);
  }

  /**
   * Clamp viewport to world bounds with padding
   */
  static clampViewport(
    viewport: ViewportBounds,
    worldBounds: WorldBounds,
    padding: number = VIEWPORT_CONSTANTS.PAN_CLAMP_PADDING
  ): ViewportBounds {
    return viewport.clampPosition(worldBounds, padding);
  }
}
