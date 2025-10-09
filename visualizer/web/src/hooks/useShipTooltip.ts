import { useMemo } from 'react';
import type { TaggedShip } from '../types/spacetraders';
import { getCargoIcon, getCargoLabel } from '../utils/cargo';

export type ShipTooltipData = {
  symbol: string;
  registrationName: string;
  role: string;
  statusText: string;
  flightMode: string;
  location: string;
  routeSummary: string | null;
  etaText: string | null;
  fuelCurrent: number;
  fuelCapacity: number;
  fuelPercent: number;
  cargoUnits: number;
  cargoCapacity: number;
  cargoPercent: number;
  cargoEntries: { icon: string; label: string; units: number }[];
  extraCargoCount: number;
  cooldownSeconds: number | null;
};

interface UseShipTooltipParams {
  activeSymbol: string | null;
  ships: TaggedShip[];
  now?: number;
}

export const useShipTooltip = ({ activeSymbol, ships, now = Date.now() }: UseShipTooltipParams): ShipTooltipData | null => {
  return useMemo(() => {
    if (!activeSymbol) return null;
    const ship = ships.find((candidate) => candidate.symbol === activeSymbol);
    if (!ship) return null;

    const statusText = ship.nav.status === 'DOCKED'
      ? `Docked at ${ship.nav.waypointSymbol}`
      : ship.nav.status.replace(/_/g, ' ');
    const location = ship.nav.waypointSymbol.split('-').pop() ?? ship.nav.waypointSymbol;

    let routeSummary: string | null = null;
    let etaText: string | null = null;
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route) {
      const origin = ship.nav.route.origin.symbol.split('-').pop() ?? ship.nav.route.origin.symbol;
      const destination = ship.nav.route.destination.symbol.split('-').pop() ?? ship.nav.route.destination.symbol;
      routeSummary = `${origin}→${destination}`;

      const arrivalTime = new Date(ship.nav.route.arrival).getTime();
      const remainingMs = Math.max(0, arrivalTime - now);
      const totalSeconds = Math.floor(remainingMs / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      const pad = (value: number) => value.toString().padStart(2, '0');
      etaText = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
    }

    const fuelPercent = ship.fuel.capacity > 0
      ? Math.round((ship.fuel.current / ship.fuel.capacity) * 100)
      : 0;

    const cargoPercent = ship.cargo.capacity > 0
      ? Math.round((ship.cargo.units / ship.cargo.capacity) * 100)
      : 0;

    const cargoEntries = ship.cargo.inventory.slice(0, 4).map((item) => ({
      icon: getCargoIcon(item.symbol),
      label: getCargoLabel(item.symbol),
      units: item.units,
    }));
    const extraCargoCount = Math.max(0, ship.cargo.inventory.length - cargoEntries.length);

    const cooldownSeconds = ship.cooldown && ship.cooldown.remainingSeconds > 0
      ? ship.cooldown.remainingSeconds
      : null;

    return {
      symbol: ship.symbol,
      registrationName: ship.registration.name,
      role: ship.registration.role,
      statusText,
      flightMode: ship.nav.flightMode,
      location,
      routeSummary,
      etaText,
      fuelCurrent: ship.fuel.current,
      fuelCapacity: ship.fuel.capacity,
      fuelPercent,
      cargoUnits: ship.cargo.units,
      cargoCapacity: ship.cargo.capacity,
      cargoPercent,
      cargoEntries,
      extraCargoCount,
      cooldownSeconds,
    };
  }, [activeSymbol, ships, now]);
};
