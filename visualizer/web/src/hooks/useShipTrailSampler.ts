import { useEffect } from 'react';
import type { TaggedShip, Waypoint as WaypointType, ShipTrailPoint, FlightMode } from '../types/spacetraders';
import { Ship } from '../domain/ship';

export interface ShipTrailSamplerOptions {
  animationFrame: number;
  sampleRate?: number;
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  currentSystem: string | null;
  addTrailPoint: (shipSymbol: string, point: ShipTrailPoint) => void;
  clearTrail: (shipSymbol: string) => void;
  resolveWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
}

const DEFAULT_SAMPLE_RATE = 4;

export const useShipTrailSampler = ({
  animationFrame,
  sampleRate = DEFAULT_SAMPLE_RATE,
  ships,
  waypoints,
  currentSystem,
  addTrailPoint,
  clearTrail,
  resolveWaypointPosition,
}: ShipTrailSamplerOptions) => {
  useEffect(() => {
    if (ships.length === 0) return;
    if (animationFrame % sampleRate !== 0) return;

    const timestamp = Date.now();

    ships.forEach((ship) => {
      if (currentSystem && ship.nav.systemSymbol !== currentSystem) {
        const destinationSystem = ship.nav.route?.destination?.systemSymbol;
        if (destinationSystem !== currentSystem) {
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

      const position = Ship.getPosition(ship, waypoints, {
        waypointPositionResolver: resolveWaypointPosition,
      });
      if (position.x === 0 && position.y === 0) {
        return;
      }

      const point: ShipTrailPoint = {
        shipSymbol: ship.symbol,
        x: position.x,
        y: position.y,
        timestamp,
        flightMode,
      };

      addTrailPoint(ship.symbol, point);
    });
  }, [
    animationFrame,
    sampleRate,
    ships,
    waypoints,
    currentSystem,
    addTrailPoint,
    clearTrail,
    resolveWaypointPosition,
  ]);
};
