// The Trade Flows page: the Living Nebula scene (pixi) plus the HTML overlay
// chrome — window switch, layer toggles, detail panel, tour roster, feed-lost
// chip, fill ticker, and the shared hover tooltip. The old Konva galaxy scene
// and its modal drilldown retired in Task 13; the SYSTEM band (orb tap) is the
// in-place drilldown now.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useFlowStore } from '../store/flowStore';
import { useFlowsPolling } from '../hooks/useFlowsPolling';
import { NebulaScene, type HoverTarget, type NebulaApi } from '../nebula/NebulaScene';
import { buildSceneData } from '../nebula/sceneData';
import { FlowDetailPanel } from '../components/flows/FlowDetailPanel';
import { TourRoster } from '../components/flows/TourRoster';
import { FeedLostChip } from '../components/flows/FeedLostChip';
import { FillTicker } from '../components/flows/FillTicker';
import { FlowTooltip } from '../components/flows/FlowTooltip';
import { coverageNotice } from '../components/flows/coverageNotice';
import { NOIR } from '../theme/noir';
import type { FlowWindow } from '../types/flows';

const WINDOWS: FlowWindow[] = ['1h', '6h', '24h'];

export function TradeFlowsView() {
  useFlowsPolling();
  const window = useFlowStore((s) => s.window);
  const setWindow = useFlowStore((s) => s.setWindow);
  const live = useFlowStore((s) => s.live);
  const lanes = useFlowStore((s) => s.lanes);
  const topology = useFlowStore((s) => s.topology);
  const lastPlanAt = useFlowStore((s) => s.lastPlanAt);
  const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
  const selectFlow = useFlowStore((s) => s.selectFlow);
  const hoveredFlowId = useFlowStore((s) => s.hoveredFlowId);
  const layerToggles = useFlowStore((s) => s.layerToggles);
  const toggleLayer = useFlowStore((s) => s.toggleLayer);
  const error = useFlowStore((s) => s.error);
  const setTooltip = useFlowStore((s) => s.setTooltip);

  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  // Scene-reported focus (orb tap → symbol; wheel-out/Escape → null): drives
  // the roster auto-filter and gives the detail-error chip its subject.
  const [focusedSystem, setFocusedSystem] = useState<string | null>(null);
  // The focused system's waypoint-fetch failure (useSystemDetail's channel) —
  // shown as a one-line chip so a dark SYSTEM band is never silent.
  const [detailError, setDetailError] = useState<string | null>(null);

  const nebulaApiRef = useRef<NebulaApi | null>(null);

  // One SceneData frame per poll delivery (identity change on any slice);
  // between polls the scene dead-reckons, so no rAF rebuild is needed here.
  const sceneData = useMemo(
    () => buildSceneData(topology, lanes, live, Date.now()),
    [topology, lanes, live],
  );

  // Scene hover → the shared tooltip card. Systems and lanes have cards;
  // cluster→cluster currents don't (their $/hr labels are on-canvas).
  const onHover = useCallback(
    (t: HoverTarget | null) => {
      if (t == null || t.kind === 'current') {
        setTooltip(null);
        return;
      }
      setTooltip({ kind: t.kind, key: t.key, x: t.clientX, y: t.clientY });
    },
    [setTooltip],
  );

  const onSelectSystem = useCallback((sym: string | null) => {
    setFocusedSystem(sym);
  }, []);

  const flows = live?.flows ?? [];
  // What the map cannot draw, stated out loud — /topology serves only systems it
  // can place truthfully, so the omission needs a voice (sp-fw6a2).
  const coverage = useMemo(() => coverageNotice(topology, flows), [topology, flows]);
  const selectedFlow = flows.find((f) => f.containerId === selectedFlowId) ?? null;
  const hoveredFlow = flows.find((f) => f.containerId === hoveredFlowId) ?? null;
  // Focused system → the roster narrows to its resident hulls.
  const rosterFlows =
    focusedSystem == null ? flows : flows.filter((f) => f.shipNav?.systemSymbol === focusedSystem);

  return (
    <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
      <div className="absolute inset-0">
        <NebulaScene
          data={sceneData}
          onSelectSystem={onSelectSystem}
          onHover={onHover}
          apiRef={nebulaApiRef}
          layerToggles={layerToggles}
          onDetailError={setDetailError}
        />
      </div>

      {/* Window switch */}
      <div className="absolute bottom-4 left-4 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
        {WINDOWS.map((w) => (
          <button
            key={w}
            onClick={() => setWindow(w)}
            className="px-3 py-1 text-xs rounded"
            style={{
              background: window === w ? NOIR.accent : 'transparent',
              color: window === w ? NOIR.bg0 : NOIR.muted,
            }}
          >
            {w}
          </button>
        ))}
      </div>

      {/* Layer toggles */}
      <div className="absolute bottom-4 left-44 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
        {(['lanes', 'paths', 'ships', 'freshness', 'lattice'] as const).map((k) => (
          <button
            key={k}
            onClick={() => toggleLayer(k)}
            className="px-3 py-1 text-xs rounded capitalize"
            style={{
              background: layerToggles[k] ? NOIR.accent : 'transparent',
              color: layerToggles[k] ? NOIR.bg0 : NOIR.muted,
            }}
          >
            {k}
          </button>
        ))}
      </div>

      {/* Map-coverage notice: the galaxy is bigger than what is drawn. Sits top-left
          where the eye lands, muted so it informs without crying error. */}
      {coverage && (
        <div
          className="absolute top-4 left-4 px-3 py-1.5 rounded text-xs"
          style={{ background: NOIR.panel, color: NOIR.muted }}
          data-testid="coverage-notice"
          title="Only systems with real coordinates for the current era are drawn — the rest are known to exist from a neighbour's gate read but cannot be placed truthfully. Coverage grows on its own as coordinates are fetched, so this fraction should climb."
        >
          {coverage.text}
        </div>
      )}

      <FlowDetailPanel flow={hoveredFlow ?? selectedFlow} />
      <TourRoster
        flows={rosterFlows}
        lanes={lanes}
        selectedFlowId={selectedFlowId}
        onRowClick={(id) => {
          selectFlow(id);
          nebulaApiRef.current?.focusTour(id);
        }}
      />
      <FeedLostChip feedLost={live?.feedLost ?? false} lastPlanAt={lastPlanAt} nowMs={nowMs} />

      {(error != null || detailError != null) && (
        <div className="absolute bottom-4 right-4 flex flex-col items-end gap-1">
          {error && (
            <div className="px-3 py-1.5 rounded text-xs" style={{ background: NOIR.panel, color: NOIR.bad }}>
              {error}
            </div>
          )}
          {detailError && (
            <div className="px-3 py-1.5 rounded text-xs" style={{ background: NOIR.panel, color: NOIR.bad }}>
              {focusedSystem ? `${focusedSystem}: ${detailError}` : detailError}
            </div>
          )}
        </div>
      )}

      {/* Ambient fill ticker + shared hover card — the tooltip mounts last so it
          floats above every panel. */}
      <FillTicker />
      <FlowTooltip />
    </div>
  );
}
