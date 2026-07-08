import { memo, useRef } from 'react';
import { Circle, Rect } from 'react-konva';
import type { FleetEvent, TaggedShip, Waypoint as WaypointType } from '../types/spacetraders';
import { Ship } from '../domain/ship';
import {
  findGateWaypoint,
  fxAnchor,
  fxForEvent,
  fxProgress,
  pruneFx,
  type FxInstance,
  type FxKind,
  type FxPoint,
} from '../domain/eventFx';
import { useStore } from '../store/useStore';
import { NOIR } from '../theme/noir';
import { GATE_WAYPOINT } from '../constants/api';

interface Point {
  x: number;
  y: number;
}

// Same prop contract as MiningLaserLayer so SpaceMap wires both identically.
export interface EventFxLayerProps {
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  animationFrame: number;
  frameTimestamp: number;
  getShipRenderPosition: (ship: TaggedShip, target: Point, timestamp: number) => Point;
  getWaypointPosition: (waypoint: WaypointType) => Point;
}

/**
 * Event drama layer: turns new gobot captain_events into short-lived scene FX
 * (arrival ripples at ships, contract flashes at the jump gate, income pings).
 *
 * Effects live in a ref — they are per-frame ephemera, not app state — and are
 * pruned every frame with the shared frameTimestamp, the same clock they were
 * spawned with. Spawn/prune runs in the render body: the layer re-renders on
 * every animation tick anyway (frameTimestamp changes), and the monotonic
 * highest-seen-id guard makes the spawn pass idempotent, so a StrictMode
 * double render cannot duplicate effects.
 */
export const EventFxLayer = memo(function EventFxLayer({
  ships,
  waypoints,
  animationFrame,
  frameTimestamp,
  getShipRenderPosition,
  getWaypointPosition,
}: EventFxLayerProps) {
  const fleetEvents = useStore((state) => state.fleetEvents);

  const fxRef = useRef<FxInstance[]>([]);
  // null = not yet seeded. The FIRST non-empty fleetEvents snapshot is the
  // backlog (the first poll fetches up to 50 rows; the store then caps history
  // at 100): seed the cursor from it WITHOUT spawning, so an always-on client
  // never opens with a backlog burst. Only ids newer than the seed ever spawn FX.
  const highestSeenIdRef = useRef<number | null>(null);

  // World-space anchor for an event, per its kind's anchor contract:
  //   ship — event.ship looked up in the (filtered) ships currently on the map,
  //          positioned exactly like ShipLayer does (Ship.getPosition + the
  //          shared smoothing cache), so FX land on the rendered sprite;
  //   gate — the current-era jump-gate construction waypoint;
  //   none — feed-only kinds; null means "no scene FX" and is always tolerated.
  const resolveFxPosition = (event: FleetEvent, kind: FxKind): FxPoint | null => {
    switch (fxAnchor(kind)) {
      case 'none':
        return null;
      case 'gate': {
        // Prefer the current-era gate symbol, but fall back to any JUMP_GATE so
        // the flash still lands in demo mode (no X1-PZ28) and survives era drift.
        const gate = findGateWaypoint(waypoints, GATE_WAYPOINT);
        return gate ? getWaypointPosition(gate) : null;
      }
      case 'ship': {
        if (!event.ship) return null;
        const ship = ships.find((candidate) => candidate.symbol === event.ship);
        if (!ship) return null;
        const target = Ship.getPosition(ship, waypoints, {
          waypointPositionResolver: getWaypointPosition,
        });
        return getShipRenderPosition(ship, target, frameTimestamp);
      }
    }
  };

  if (fleetEvents.length > 0) {
    if (highestSeenIdRef.current === null) {
      // Backlog snapshot: advance the cursor, spawn nothing.
      highestSeenIdRef.current = fleetEvents.reduce((max, e) => Math.max(max, e.id), 0);
    } else {
      let highest = highestSeenIdRef.current;
      for (const event of fleetEvents) {
        // The store merge keeps fleetEvents newest-first by id, so the first
        // already-seen id means everything after it is old too.
        if (event.id <= highestSeenIdRef.current) break;
        const instance = fxForEvent(event, resolveFxPosition, frameTimestamp);
        if (instance) fxRef.current.push(instance);
        if (event.id > highest) highest = event.id;
      }
      highestSeenIdRef.current = highest;
    }
  }

  // Prune every frame so each effect self-expires on its ttl.
  fxRef.current = pruneFx(fxRef.current, frameTimestamp);

  if (fxRef.current.length === 0) return null;

  const time = animationFrame / 60; // Convert to seconds (same clock as lasers)

  return (
    <>
      {fxRef.current.map((instance) => {
        const p = fxProgress(instance, frameTimestamp);
        const fade = 1 - p;

        switch (instance.kind) {
          case 'arrival-ripple':
            return (
              <Circle
                key={`eventfx-${instance.key}`}
                x={instance.x}
                y={instance.y}
                radius={4 + p * 26}
                stroke={NOIR.accentSoft}
                strokeWidth={0.6}
                opacity={fade}
                listening={false}
                perfectDrawEnabled={false}
              />
            );
          case 'gate-flash': {
            const side = 12 + p * 8;
            const pulse = 0.75 + 0.25 * Math.sin(time * 8);
            return (
              <Rect
                key={`eventfx-${instance.key}`}
                x={instance.x}
                y={instance.y}
                width={side}
                height={side}
                offsetX={side / 2}
                offsetY={side / 2}
                stroke={NOIR.warn}
                strokeWidth={0.8}
                opacity={fade}
                shadowColor={NOIR.warn}
                shadowBlur={14 * fade * pulse}
                listening={false}
                perfectDrawEnabled={false}
              />
            );
          }
          case 'income-ping':
            return (
              <Circle
                key={`eventfx-${instance.key}`}
                x={instance.x}
                y={instance.y}
                radius={3}
                fill={NOIR.good}
                opacity={fade}
                listening={false}
                perfectDrawEnabled={false}
              />
            );
          case 'generic-ping':
            // Feed-only kinds never spawn (anchor 'none'); defensive no-op.
            return null;
        }
      })}
    </>
  );
});
