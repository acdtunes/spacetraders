import { memo, useMemo } from 'react';
import { Group, Circle } from 'react-konva';
import type { ShipTrailPoint, TaggedShip, Waypoint as WaypointType, ShipAssignment } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import type { ShipPositionOptions } from '../domain';
import { ShipSprite } from './ShipSprite';
import { ShipNameLabel } from './ShipNameLabel';
import { ShipOperationBadge } from './ShipOperationBadge';
import { ShipTaskBadge } from './ShipTaskBadge';
import { SiphonActivityIndicator } from './SiphonActivityIndicator';
import { StorageShipIndicator } from './StorageShipIndicator';
import { calculateShipRotation, getShipLabelInfo } from '../utils/shipDisplay';
import { getShipOperation } from '../utils/shipOperations';
import { Waypoint } from '../domain/waypoint';

interface Point {
  x: number;
  y: number;
}

export interface ShipLayerProps {
  ships: TaggedShip[];
  trails: Map<string, ShipTrailPoint[]>;
  waypoints: Map<string, WaypointType>;
  frameTimestamp: number;
  currentScale: number;
  showShipNames: boolean;
  getShipSize: (role: string | undefined) => number;
  getShipRenderPosition: (ship: TaggedShip, target: Point, timestamp: number) => Point;
  selectShipAsset: (ship: TaggedShip) => string | null;
  projectToScreen: (point: Point) => Point | null;
  projectToWorld: (point: Point) => Point | null;
  onSelectShip: (ship: TaggedShip, position: Point) => void;
  onHoverShip: (symbol: string | null) => void;
  assignments: Map<string, ShipAssignment>;
  shipPositionOptions?: ShipPositionOptions;
  getWaypointPosition: (waypoint: WaypointType) => Point;
}

export const ShipLayer = memo(function ShipLayer({
  ships,
  trails,
  waypoints,
  frameTimestamp,
  currentScale,
  showShipNames,
  getShipSize,
  getShipRenderPosition,
  selectShipAsset,
  projectToScreen,
  projectToWorld,
  onSelectShip,
  onHoverShip,
  assignments,
  shipPositionOptions,
  getWaypointPosition,
}: ShipLayerProps) {
  const dockedFormations = useMemo(() => {
    const formations = new Map<string, Map<string, Point>>();
    const groupedByWaypoint = new Map<string, TaggedShip[]>();

    ships.forEach((ship) => {
      if (ship.nav.status !== 'DOCKED') return;
      if (!ship.nav.waypointSymbol) return;
      const existing = groupedByWaypoint.get(ship.nav.waypointSymbol);
      if (existing) {
        existing.push(ship);
      } else {
        groupedByWaypoint.set(ship.nav.waypointSymbol, [ship]);
      }
    });

    const DOCKED_RING_CAPACITY = 6;
    const DOCKED_RING_GAP = 6;
    const DOCKED_BASE_OFFSET = 6;

    groupedByWaypoint.forEach((group, waypointSymbol) => {
      if (group.length <= 1) {
        return;
      }

      const waypoint = waypoints.get(waypointSymbol);
      if (!waypoint) return;

      const center = getWaypointPosition(waypoint);
      const waypointRadius = Waypoint.getRadius(waypoint);
      const shipsSorted = [...group].sort((a, b) => a.symbol.localeCompare(b.symbol));
      const formationPositions = new Map<string, Point>();

      shipsSorted.forEach((ship, index) => {
        const ringIndex = Math.floor(index / DOCKED_RING_CAPACITY);
        const ringStartIndex = ringIndex * DOCKED_RING_CAPACITY;
        const positionsInRing = Math.min(
          DOCKED_RING_CAPACITY,
          shipsSorted.length - ringStartIndex
        );
        const positionInRing = index - ringStartIndex;
        const angleStep = (Math.PI * 2) / positionsInRing;
        const angle = positionInRing * angleStep;
        const radius = waypointRadius + DOCKED_BASE_OFFSET + ringIndex * DOCKED_RING_GAP;

        formationPositions.set(ship.symbol, {
          x: center.x + Math.cos(angle) * radius,
          y: center.y + Math.sin(angle) * radius,
        });
      });

      formations.set(waypointSymbol, formationPositions);
    });

    return formations;
  }, [ships, waypoints, getWaypointPosition]);

  return (
    <>
      {ships.map((ship) => {
        const targetPosition = Ship.getPosition(ship, waypoints, shipPositionOptions);
        if (targetPosition.x === 0 && targetPosition.y === 0) {
          return null;
        }

        let adjustedTarget = targetPosition;

        if (ship.nav.status === 'DOCKED' && ship.nav.waypointSymbol) {
          const formation = dockedFormations.get(ship.nav.waypointSymbol);
          const dockedOffset = formation?.get(ship.symbol);
          if (dockedOffset) {
            adjustedTarget = dockedOffset;
          }
        }

        const position = getShipRenderPosition(ship, adjustedTarget, frameTimestamp);
        const shipAssetPath = selectShipAsset(ship);
        const shipTrail = trails.get(ship.symbol);
        const rotation = calculateShipRotation(
          ship,
          position,
          waypoints,
          shipTrail,
          getWaypointPosition
        );

        const labelInfo = getShipLabelInfo(ship, position, {
          currentScale,
          projectToScreen,
          projectToWorld,
        });

        const assignment = getShipOperation(ship.symbol, assignments);
        const operationType = assignment?.operation || null;

        // Check if ship is actively siphoning (gas operation + has active cooldown)
        // Cooldown indicates ship just performed a siphon action
        const hasActiveCooldown = ship.cooldown && ship.cooldown.remainingSeconds > 0;
        const isGasSiphoning = operationType === 'gas' && hasActiveCooldown;

        // Check if ship is a storage ship actively buffering
        const isStorageActive = operationType === 'gas-storage' &&
          (ship.nav.status === 'DOCKED' || ship.nav.status === 'IN_ORBIT');

        return (
          <Group key={ship.symbol} x={position.x} y={position.y}>
            {/* Siphon activity indicator (behind ship) */}
            <SiphonActivityIndicator
              frameTimestamp={frameTimestamp}
              currentScale={currentScale}
              isActive={isGasSiphoning}
            />

            {/* Storage ship indicator (behind ship) */}
            <StorageShipIndicator
              frameTimestamp={frameTimestamp}
              currentScale={currentScale}
              isActive={isStorageActive}
            />

            <Group rotation={rotation}>
              <Circle
                radius={4}
                fill="transparent"
                onClick={() => onSelectShip(ship, position)}
                onMouseEnter={(event) => {
                  onHoverShip(ship.symbol);
                  const container = event.target.getStage()?.container();
                  if (container) {
                    container.style.cursor = 'pointer';
                  }
                }}
                onMouseLeave={(event) => {
                  onHoverShip(null);
                  const container = event.target.getStage()?.container();
                  if (container) {
                    container.style.cursor = 'grab';
                  }
                }}
              />
              <ShipSprite assetPath={shipAssetPath} size={getShipSize(ship.registration.role)} />
            </Group>

            {showShipNames && labelInfo && (
              <ShipNameLabel
                labelText={labelInfo.labelText}
                labelWidth={labelInfo.labelWidth}
                labelHeight={labelInfo.labelHeight}
                labelScale={labelInfo.labelScale}
                offsetX={labelInfo.offsetX}
                offsetY={labelInfo.offsetY}
              />
            )}

            {showShipNames && operationType && (
              <ShipOperationBadge
                operationType={operationType}
                currentScale={currentScale}
                labelInfo={labelInfo}
              />
            )}

            {showShipNames && assignment?.metadata?.task_type && (
              <ShipTaskBadge
                taskType={assignment.metadata.task_type as string}
                good={assignment.metadata.good as string | null}
                currentScale={currentScale}
                labelInfo={labelInfo}
              />
            )}
          </Group>
        );
      })}
    </>
  );
});
