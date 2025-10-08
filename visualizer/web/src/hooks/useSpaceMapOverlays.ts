import { useMemo } from 'react';
import type { TaggedShip, Waypoint as WaypointType, Market } from '../types/spacetraders';
import type { TradeOpportunity } from '../domain/market';
import { Ship } from '../domain';
import type { Position } from '../domain/ship';
import { useShipTooltip, type ShipTooltipData } from './useShipTooltip';
import { useSelectionOverlay, type SelectionOverlay as SelectionOverlayData } from './useSelectionOverlay';

export interface SelectedMapObject {
  type: 'waypoint' | 'ship';
  symbol: string;
  x: number;
  y: number;
}

type WaypointTooltipAnchor = {
  symbol: string;
  worldX: number;
  worldY: number;
} | null;

interface SpaceMapOverlayParams {
  hoveredShip: string | null;
  selectedObject: SelectedMapObject | null;
  ships: TaggedShip[];
  waypoints: Map<string, WaypointType>;
  markets: Map<string, Market>;
  projectToScreen: (point: { x: number; y: number }) => { x: number; y: number } | null;
  getWaypointPosition: (waypoint: WaypointType) => { x: number; y: number };
  getShipRenderPosition: (ship: TaggedShip, target: Position, timestamp: number) => Position;
  frameTimestamp: number;
  waypointTooltipAnchor: WaypointTooltipAnchor;
  shipTooltipOffset: { x: number; y: number };
  waypointTooltipOffset: { x: number; y: number };
  getWaypointOpportunities: (symbol: string, markets: Map<string, Market>, limit: number) => TradeOpportunity[];
  formatOpportunity: (opportunity: TradeOpportunity) => string;
  opportunityLimit?: number;
}

export interface WaypointTooltipData {
  symbol: string;
  type: string;
  traits: WaypointType['traits'];
  faction: WaypointType['faction'];
  hasMarketplace: boolean;
  marketData: {
    importsCount: number;
    exportsCount: number;
    opportunities: string[];
  } | null;
}

interface SpaceMapOverlaysResult {
  selectionOverlay: SelectionOverlayData | null;
  shipTooltip: ShipTooltipData | null;
  shipTooltipPosition: { left: number; top: number } | null;
  waypointTooltip: WaypointTooltipData | null;
  waypointTooltipPosition: { left: number; top: number } | null;
}

export function useSpaceMapOverlays({
  hoveredShip,
  selectedObject,
  ships,
  waypoints,
  markets,
  projectToScreen,
  getWaypointPosition,
  getShipRenderPosition,
  frameTimestamp,
  waypointTooltipAnchor,
  shipTooltipOffset,
  waypointTooltipOffset,
  getWaypointOpportunities,
  formatOpportunity,
  opportunityLimit = 2,
}: SpaceMapOverlayParams): SpaceMapOverlaysResult {
  const activeShipTooltipSymbol = hoveredShip ?? (selectedObject?.type === 'ship' ? selectedObject.symbol : null);

  const shipTooltip = useShipTooltip({ activeSymbol: activeShipTooltipSymbol, ships });

  const shipTooltipPosition = useMemo(() => {
    if (!activeShipTooltipSymbol) return null;

    const ship = ships.find((candidate) => candidate.symbol === activeShipTooltipSymbol);
    if (!ship) return null;

    const targetPosition = Ship.getPosition(ship, waypoints);
    const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
    if (position.x === 0 && position.y === 0) return null;

    const screenPos = projectToScreen(position);
    if (!screenPos) return null;

    return {
      left: screenPos.x - shipTooltipOffset.x,
      top: screenPos.y - shipTooltipOffset.y,
    };
  }, [
    activeShipTooltipSymbol,
    ships,
    waypoints,
    getShipRenderPosition,
    frameTimestamp,
    projectToScreen,
    shipTooltipOffset.x,
    shipTooltipOffset.y,
  ]);

  const selectionOverlay = useSelectionOverlay({
    selectedObject,
    ships,
    waypoints,
    projectToScreen,
    getWaypointPosition,
    getShipPosition: (ship) => {
      const targetPosition = Ship.getPosition(ship, waypoints);
      const position = getShipRenderPosition(ship, targetPosition, frameTimestamp);
      if (position.x === 0 && position.y === 0) {
        return null;
      }
      return position;
    },
  });

  const waypointTooltip = useMemo<WaypointTooltipData | null>(() => {
    if (!waypointTooltipAnchor) return null;

    const waypoint = waypoints.get(waypointTooltipAnchor.symbol);
    if (!waypoint) return null;

    const market = markets.get(waypointTooltipAnchor.symbol);
    const hasMarketplace = waypoint.traits.some((trait) => trait.symbol === 'MARKETPLACE');

    let marketData: WaypointTooltipData['marketData'] = null;
    if (market && hasMarketplace) {
      const opportunities = getWaypointOpportunities(waypoint.symbol, markets, opportunityLimit).map(formatOpportunity);
      marketData = {
        importsCount: market.imports?.length ?? 0,
        exportsCount: market.exports?.length ?? 0,
        opportunities,
      };
    }

    return {
      symbol: waypoint.symbol,
      type: waypoint.type,
      traits: waypoint.traits,
      faction: waypoint.faction,
      hasMarketplace,
      marketData,
    };
  }, [
    waypointTooltipAnchor,
    waypoints,
    markets,
    getWaypointOpportunities,
    formatOpportunity,
    opportunityLimit,
  ]);

  const waypointTooltipPosition = useMemo(() => {
    if (!waypointTooltipAnchor) return null;

    const screenPos = projectToScreen({
      x: waypointTooltipAnchor.worldX,
      y: waypointTooltipAnchor.worldY,
    });

    if (!screenPos) return null;

    return {
      left: screenPos.x + waypointTooltipOffset.x,
      top: screenPos.y - waypointTooltipOffset.y,
    };
  }, [waypointTooltipAnchor, projectToScreen, waypointTooltipOffset.x, waypointTooltipOffset.y]);

  return {
    selectionOverlay,
    shipTooltip,
    shipTooltipPosition,
    waypointTooltip,
    waypointTooltipPosition,
  };
}
