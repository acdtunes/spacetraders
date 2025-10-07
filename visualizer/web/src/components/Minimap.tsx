import { useMemo, useState } from 'react';
import { Stage, Layer, Circle, Rect } from 'react-konva';
import type { Waypoint, Ship as ShipType } from '../types/spacetraders';
import { ViewportBounds, Ship as ShipDomain } from '../domain';
import { VIEWPORT_CONSTANTS } from '../constants/viewport';

interface MinimapProps {
  waypoints: Map<string, Waypoint>;
  ships: ShipType[];
  viewportBounds: {
    x: number;
    y: number;
    width: number;
    height: number;
    scale: number;
  };
  onNavigate: (x: number, y: number) => void;
  onZoom: (worldX: number, worldY: number, zoomFactor: number) => void;
  animationFrame: number;
}

const Minimap = ({ waypoints, ships, viewportBounds, onNavigate, onZoom, animationFrame }: MinimapProps) => {
  const [isMinimized, setIsMinimized] = useState(false);

  const minimapData = useMemo(() => {
    if (waypoints.size === 0) return null;

    const waypointArray = Array.from(waypoints.values());

    // Calculate bounds
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    waypointArray.forEach(wp => {
      minX = Math.min(minX, wp.x);
      maxX = Math.max(maxX, wp.x);
      minY = Math.min(minY, wp.y);
      maxY = Math.max(maxY, wp.y);
    });

    const width = maxX - minX;
    const height = maxY - minY;
    const padding = 10;
    const canvasSize = 250;

    // Calculate scale to fit minimap
    const scaleX = (canvasSize - padding * 2) / width;
    const scaleY = (canvasSize - padding * 2) / height;
    const scale = Math.min(scaleX, scaleY);

    // Create ViewportBounds for coordinate conversions
    const viewport = new ViewportBounds(
      viewportBounds.x,
      viewportBounds.y,
      viewportBounds.width,
      viewportBounds.height,
      viewportBounds.scale
    );

    const minimapInfo = { minX, minY, scale, padding, canvasSize };

    // Map waypoints to minimap coordinates using ViewportBounds
    const mappedWaypoints = waypointArray.map(wp => {
      const minimapPos = viewport.toMinimapCoords(wp.x, wp.y, minimapInfo);

      // Color code by type
      let color = '#4a9eff';
      if (wp.type === 'JUMP_GATE') color = '#9b59b6';
      else if (wp.type === 'ORBITAL_STATION') color = '#3498db';
      else if (wp.type === 'PLANET') color = '#2980b9';
      else if (wp.type === 'GAS_GIANT') color = '#e67e22';
      else if (wp.type === 'ASTEROID' || wp.type === 'ASTEROID_FIELD') color = '#7f8c8d';

      return { x: minimapPos.x, y: minimapPos.y, color };
    });

    const mappedShips = ships
      .map(ship => {
        const position = ShipDomain.getPosition(ship, waypoints);
        if (position.x === 0 && position.y === 0) return null;

        const minimapPos = viewport.toMinimapCoords(position.x, position.y, minimapInfo);
        const color = ShipDomain.getDisplayColor(ship);
        return { x: minimapPos.x, y: minimapPos.y, color };
      })
      .filter((value): value is { x: number; y: number; color: string } => Boolean(value));

    // Calculate viewport rectangle using ViewportBounds
    const vpRect = viewport.toRect();
    const vpTopLeft = viewport.toMinimapCoords(vpRect.x, vpRect.y, minimapInfo);

    const viewportRect = {
      x: vpTopLeft.x,
      y: vpTopLeft.y,
      width: vpRect.width * scale,
      height: vpRect.height * scale,
    };

    return {
      waypoints: mappedWaypoints,
      ships: mappedShips,
      viewport: viewportRect,
      canvasSize,
      minX,
      minY,
      scale,
      padding,
    };
  }, [waypoints, viewportBounds, ships, animationFrame]);

  if (!minimapData) return null;

  const minimapInfo = {
    minX: minimapData.minX,
    minY: minimapData.minY,
    scale: minimapData.scale,
    padding: minimapData.padding,
  };

  const navigateFromMinimap = (minimapX: number, minimapY: number) => {
    const worldX = minimapInfo.minX + (minimapX - minimapInfo.padding) / minimapInfo.scale;
    const worldY = minimapInfo.minY + (minimapY - minimapInfo.padding) / minimapInfo.scale;
    onNavigate(worldX, worldY);
  };

  const handleStageClick = (e: any) => {
    const stage = e.target.getStage();
    const pointerPosition = stage.getPointerPosition();
    if (pointerPosition) {
      navigateFromMinimap(pointerPosition.x, pointerPosition.y);
    }
  };

  const handleViewportDrag = (dragX: number, dragY: number) => {
    const centerX = dragX + minimapData.viewport.width / 2;
    const centerY = dragY + minimapData.viewport.height / 2;
    navigateFromMinimap(centerX, centerY);
  };

  const handleViewportDragMove = (e: any) => {
    handleViewportDrag(e.target.x(), e.target.y());
  };

  const handleViewportDragEnd = (e: any) => {
    handleViewportDrag(e.target.x(), e.target.y());
  };

  const constrainViewportDrag = (pos: { x: number; y: number }) => {
    const maxX = minimapData.canvasSize - minimapData.viewport.width;
    const maxY = minimapData.canvasSize - minimapData.viewport.height;
    return {
      x: Math.max(0, Math.min(maxX, pos.x)),
      y: Math.max(0, Math.min(maxY, pos.y)),
    };
  };

  const handleStageWheel = (e: any) => {
    e.evt.preventDefault();
    e.cancelBubble = true;

    const stage = e.target.getStage();
    const pointerPosition = stage.getPointerPosition();
    if (!pointerPosition) return;

    const worldX = minimapInfo.minX + (pointerPosition.x - minimapInfo.padding) / minimapInfo.scale;
    const worldY = minimapInfo.minY + (pointerPosition.y - minimapInfo.padding) / minimapInfo.scale;

    const zoomFactor = e.evt.deltaY > 0 ? VIEWPORT_CONSTANTS.WHEEL_ZOOM_OUT : VIEWPORT_CONSTANTS.WHEEL_ZOOM_IN;
    onZoom(worldX, worldY, zoomFactor);
  };

  return (
    <div className="absolute bottom-4 right-4 bg-gray-800 border-2 border-gray-700 rounded-lg shadow-lg overflow-hidden z-10">
      <div className="bg-gray-750 px-2 py-1 border-b border-gray-700 flex items-center justify-between">
        <span className="text-xs font-bold">Minimap</span>
        <button
          onClick={() => setIsMinimized(!isMinimized)}
          className="text-xs px-1.5 py-0.5 hover:bg-gray-600 rounded transition-colors"
          title={isMinimized ? "Maximize" : "Minimize"}
        >
          {isMinimized ? '□' : '−'}
        </button>
      </div>
      {!isMinimized && (
        <Stage
          width={minimapData.canvasSize}
          height={minimapData.canvasSize}
          onClick={handleStageClick}
          onWheel={handleStageWheel}
          style={{ cursor: 'pointer' }}
        >
          <Layer>
            {/* Background */}
            <Rect
              x={0}
              y={0}
              width={minimapData.canvasSize}
              height={minimapData.canvasSize}
              fill="#0a0a1a"
            />

            {/* Waypoints */}
            {minimapData.waypoints.map((wp, i) => (
              <Circle
                key={i}
                x={wp.x}
                y={wp.y}
                radius={2}
                fill={wp.color}
                opacity={0.8}
                listening={false}
              />
            ))}

            {/* Ships */}
            {minimapData.ships.map((ship, i) => (
              <Circle
                key={`ship-${i}`}
                x={ship.x}
                y={ship.y}
                radius={3}
                fill={ship.color}
                stroke="#000000"
                strokeWidth={1}
                opacity={0.9}
                listening={false}
              />
            ))}

            {/* Viewport rectangle */}
            <Rect
              x={minimapData.viewport.x}
              y={minimapData.viewport.y}
              width={minimapData.viewport.width}
              height={minimapData.viewport.height}
              fill="rgba(255, 170, 0, 0.15)"
              stroke="#ffaa00"
              strokeWidth={2}
              draggable
              dragBoundFunc={constrainViewportDrag}
              onDragMove={handleViewportDragMove}
              onDragEnd={handleViewportDragEnd}
              onDragStart={(e) => e.cancelBubble = true}
              onMouseDown={(e) => e.cancelBubble = true}
              onTouchStart={(e) => e.cancelBubble = true}
            />
          </Layer>
        </Stage>
      )}
    </div>
  );
};

export default Minimap;
