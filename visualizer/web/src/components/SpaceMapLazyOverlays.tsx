import { lazy, Suspense, type ReactNode } from 'react';
import type { SelectionOverlayProps } from './SelectionOverlay';
import type { ShipTooltipOverlayProps } from './ShipTooltipOverlay';
import type { WaypointTooltipOverlayProps } from './WaypointTooltipOverlay';

const SelectionOverlayImpl = lazy(() =>
  import('./SelectionOverlay').then((module) => ({ default: module.SelectionOverlay }))
);

const ShipTooltipOverlayImpl = lazy(() =>
  import('./ShipTooltipOverlay').then((module) => ({ default: module.ShipTooltipOverlay }))
);

const WaypointTooltipOverlayImpl = lazy(() =>
  import('./WaypointTooltipOverlay').then((module) => ({ default: module.WaypointTooltipOverlay }))
);

interface LazySelectionOverlayProps extends SelectionOverlayProps {
  fallback?: ReactNode;
}

export const SelectionOverlayLazy = ({ fallback = null, ...props }: LazySelectionOverlayProps) => (
  <Suspense fallback={fallback}>
    <SelectionOverlayImpl {...props} />
  </Suspense>
);

interface LazyShipTooltipOverlayProps extends ShipTooltipOverlayProps {
  fallback?: ReactNode;
}

export const ShipTooltipOverlayLazy = ({ fallback = null, ...props }: LazyShipTooltipOverlayProps) => (
  <Suspense fallback={fallback}>
    <ShipTooltipOverlayImpl {...props} />
  </Suspense>
);

interface LazyWaypointTooltipOverlayProps extends WaypointTooltipOverlayProps {
  fallback?: ReactNode;
}

export const WaypointTooltipOverlayLazy = ({ fallback = null, ...props }: LazyWaypointTooltipOverlayProps) => (
  <Suspense fallback={fallback}>
    <WaypointTooltipOverlayImpl {...props} />
  </Suspense>
);
