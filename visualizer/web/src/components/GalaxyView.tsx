import { useEffect, useState, useRef } from 'react';
import { Stage, Layer, Circle, Text, Group } from 'react-konva';
import type Konva from 'konva';
import { useStore } from '../store/useStore';
import { getAllSystems } from '../services/api';
import { CANVAS_CONSTANTS } from '../constants/canvas';

const GalaxyView = () => {
  const { systems, setSystems, ships, currentSystem, setCurrentSystem, setViewMode } = useStore();
  const [isLoading, setIsLoading] = useState(false);
  const [hoveredSystem, setHoveredSystem] = useState<string | null>(null);
  const stageRef = useRef<Konva.Stage>(null);

  const width = window.innerWidth - 256;
  const height = window.innerHeight - 64;

  // Fetch all systems if not already loaded
  useEffect(() => {
    if (systems.length === 0 && !isLoading) {
      setIsLoading(true);
      getAllSystems()
        .then((data) => {
          setSystems(data);
          setIsLoading(false);
        })
        .catch((error) => {
          console.error('Failed to load systems:', error);
          setIsLoading(false);
        });
    }
  }, [systems.length, setSystems, isLoading]);

  // Count ships per system
  const shipCounts = new Map<string, number>();
  ships.forEach((ship: any) => {
    const count = shipCounts.get(ship.nav.systemSymbol) || 0;
    shipCounts.set(ship.nav.systemSymbol, count + 1);
  });

  // Center view on systems with ships when systems load
  useEffect(() => {
    if (!stageRef.current || systems.length === 0) return;

    const stage = stageRef.current;
    const systemsWithShips = systems.filter((s) => shipCounts.has(s.symbol));
    const viewSystems = systemsWithShips.length > 0 ? systemsWithShips : systems;

    const avgX = viewSystems.reduce((sum, s) => sum + s.x, 0) / viewSystems.length;
    const avgY = viewSystems.reduce((sum, s) => sum + s.y, 0) / viewSystems.length;

    const initialScale = 0.5;
    stage.scale({ x: initialScale, y: initialScale });
    stage.position({
      x: width / 2 - avgX * initialScale,
      y: height / 2 - avgY * initialScale,
    });
  }, [systems, width, height]);

  const handleWheel = (e: any) => {
    e.evt.preventDefault();
    const stage = e.target.getStage();
    if (!stage) return;

    const oldScale = stage.scaleX();
    const pointer = stage.getPointerPosition();
    if (!pointer) return;

    const mousePointTo = {
      x: (pointer.x - stage.x()) / oldScale,
      y: (pointer.y - stage.y()) / oldScale,
    };

    const delta = e.evt.deltaY > 0
      ? CANVAS_CONSTANTS.ZOOM_OUT_FACTOR
      : CANVAS_CONSTANTS.ZOOM_IN_FACTOR;

    const newScale = Math.max(
      CANVAS_CONSTANTS.MIN_ZOOM_GALAXY,
      Math.min(CANVAS_CONSTANTS.MAX_ZOOM_GALAXY, oldScale * delta)
    );

    stage.scale({ x: newScale, y: newScale });
    stage.position({
      x: pointer.x - mousePointTo.x * newScale,
      y: pointer.y - mousePointTo.y * newScale,
    });
  };

  return (
    <div className="relative w-full h-full">
      <Stage
        ref={stageRef}
        width={width}
        height={height}
        draggable
        onWheel={handleWheel}
      >
        <Layer>
          {systems.map((system) => {
            const shipCount = shipCounts.get(system.symbol) || 0;
            const hasShips = shipCount > 0;

            const baseRadius = 2;
            const radius = baseRadius + Math.min(system.waypoints.length / 20, 5);

            const color = hasShips ? '#4ECDC4' : '#666666';
            const isHovered = hoveredSystem === system.symbol;
            const isCurrent = system.symbol === currentSystem;

            return (
              <Group key={system.symbol}>
                {/* Current system highlight */}
                {isCurrent && (
                  <Circle
                    x={system.x}
                    y={system.y}
                    radius={radius + 4}
                    stroke="#FFE66D"
                    strokeWidth={2}
                    opacity={0.8}
                  />
                )}

                {/* Hover effect */}
                {isHovered && (
                  <Circle
                    x={system.x}
                    y={system.y}
                    radius={radius + 2}
                    stroke="#ffffff"
                    strokeWidth={1}
                    opacity={0.8}
                  />
                )}

                {/* Main system circle */}
                <Circle
                  x={system.x}
                  y={system.y}
                  radius={radius}
                  fill={color}
                  opacity={hasShips ? 0.9 : 0.5}
                />

                {/* Interactive area */}
                <Circle
                  x={system.x}
                  y={system.y}
                  radius={radius + 3}
                  fill="transparent"
                  onMouseEnter={() => setHoveredSystem(system.symbol)}
                  onMouseLeave={() => setHoveredSystem(null)}
                  onClick={() => {
                    setCurrentSystem(system.symbol);
                    setViewMode('system');
                  }}
                  onTouchStart={() => {
                    setCurrentSystem(system.symbol);
                    setViewMode('system');
                  }}
                />

                {/* Ship count label */}
                {hasShips && (
                  <Text
                    x={system.x}
                    y={system.y}
                    text={shipCount.toString()}
                    fontSize={12}
                    fill="#ffffff"
                    fontStyle="bold"
                    offsetX={6}
                    offsetY={6}
                  />
                )}

                {/* System symbol label */}
                <Text
                  x={system.x + radius + 2}
                  y={system.y - 4}
                  text={system.symbol}
                  fontSize={8}
                  fill="#ffffff"
                  opacity={0.3}
                />
              </Group>
            );
          })}
        </Layer>
      </Stage>

      {/* Loading state */}
      {isLoading && (
        <div className="absolute inset-0 flex items-center justify-center">
          <div className="bg-gray-800 bg-opacity-90 rounded-lg p-8 text-center">
            <h2 className="text-xl font-bold mb-2">Loading Systems...</h2>
            <p className="text-gray-400">Fetching galaxy data from SpaceTraders API</p>
          </div>
        </div>
      )}

      {/* Legend */}
      <div className="absolute bottom-4 left-4 bg-gray-800 bg-opacity-80 rounded p-3 text-xs">
        <div className="font-bold mb-2">Legend</div>
        <div className="flex items-center gap-2 mb-1">
          <div className="w-3 h-3 rounded-full bg-[#4ECDC4]" />
          <span>Systems with ships</span>
        </div>
        <div className="flex items-center gap-2 mb-1">
          <div className="w-3 h-3 rounded-full bg-[#666666]" />
          <span>Empty systems</span>
        </div>
        <div className="flex items-center gap-2">
          <div className="w-3 h-3 rounded-full border-2 border-[#FFE66D]" />
          <span>Current system</span>
        </div>
      </div>
    </div>
  );
};

export default GalaxyView;
