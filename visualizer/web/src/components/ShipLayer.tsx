import { memo } from 'react';
import { Group, Circle } from 'react-konva';
import type { ShipTrailPoint, TaggedShip, Waypoint as WaypointType, ShipAssignment } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import { ShipSprite } from './ShipSprite';
import { ShipNameLabel } from './ShipNameLabel';
import { ShipOperationBadge } from './ShipOperationBadge';
import { calculateShipRotation, getShipLabelInfo } from '../utils/shipDisplay';
import { getShipOperation } from '../utils/shipOperations';

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
  shipSpriteSize: number;
  getShipRenderPosition: (ship: TaggedShip, target: Point, timestamp: number) => Point;
  selectShipAsset: (ship: TaggedShip) => string | null;
  projectToScreen: (point: Point) => Point | null;
  projectToWorld: (point: Point) => Point | null;
  onSelectShip: (ship: TaggedShip, position: Point) => void;
  onHoverShip: (symbol: string | null) => void;
  assignments: Map<string, ShipAssignment>;
  showOperationBadges: boolean;
}

export const ShipLayer = memo(function ShipLayer({
  ships,
  trails,
  waypoints,
  frameTimestamp,
  currentScale,
  showShipNames,
  shipSpriteSize,
  getShipRenderPosition,
  selectShipAsset,
  projectToScreen,
  projectToWorld,
  onSelectShip,
  onHoverShip,
  assignments,
  showOperationBadges,
}: ShipLayerProps) {
  return (
    <>
      {ships.map((ship) => {
        const targetPosition = Ship.getPosition(ship, waypoints);
        if (targetPosition.x === 0 && targetPosition.y === 0) {
          return null;
        }

        const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
        const shipAssetPath = selectShipAsset(ship);
        const shipTrail = trails.get(ship.symbol);
        const rotation = calculateShipRotation(ship, position, waypoints, shipTrail);

        const labelInfo = getShipLabelInfo(ship, position, {
          currentScale,
          projectToScreen,
          projectToWorld,
        });

        const assignment = getShipOperation(ship.symbol, assignments);
        const operationType = assignment?.operation || null;

        return (
          <Group key={ship.symbol} x={position.x} y={position.y}>
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
                    container.style.cursor = 'default';
                  }
                }}
              />
              <ShipSprite assetPath={shipAssetPath} size={shipSpriteSize} />
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

            {showOperationBadges && operationType && (
              <ShipOperationBadge
                operationType={operationType}
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

