import { useEffect, useMemo, useRef, useState } from 'react';
import { Stage, Layer, Circle, Text, Group, Line } from 'react-konva';
import type Konva from 'konva';
import { useFlowStore } from '../../store/flowStore';
import { useRafClock } from '../../hooks/useRafClock';
import { CANVAS_CONSTANTS } from '../../constants/canvas';
import { NOIR, noirAlpha } from '../../theme/noir';
import type { LiveFlow } from '../../types/flows';
import { AmbientBackdrop } from '../AmbientBackdrop';
import { buildSystemIndex, type Point } from './flowGeometry';
import { buildAdjacency, buildSystemGates, projectFlowMotion } from './flowMotion';
import { FlowLaneLayer } from './FlowLaneLayer';
import { FlowShipLayer } from './FlowShipLayer';
import { FlowPlanPath } from './FlowPlanPath';
import { FreshnessLayer } from './FreshnessLayer';

export default function FlowGalaxyScene() {
  const topology = useFlowStore((s) => s.topology);
  const lanes = useFlowStore((s) => s.lanes);
  const live = useFlowStore((s) => s.live);
  const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
  const selectFlow = useFlowStore((s) => s.selectFlow);
  const openDrilldown = useFlowStore((s) => s.openDrilldown);
  const hoverFlow = useFlowStore((s) => s.hoverFlow);
  const hoveredFlowId = useFlowStore((s) => s.hoveredFlowId);
  const setTooltip = useFlowStore((s) => s.setTooltip);
  const focusFlowId = useFlowStore((s) => s.focusFlowId);
  const clearFocus = useFlowStore((s) => s.clearFocus);
  const layerToggles = useFlowStore((s) => s.layerToggles);
  const staleFlows = useFlowStore((s) => s.staleFlows);
  const freezeAtMs = useFlowStore((s) => s.freezeAtMs);
  const freshness = useFlowStore((s) => s.freshness);
  const freshnessMissedPolls = useFlowStore((s) => s.freshnessMissedPolls);

  const stageRef = useRef<Konva.Stage>(null);
  const [scale, setScale] = useState(0.5);
  const nowMs = useRafClock();
  const centeredRef = useRef<string | null>(null);

  const width = window.innerWidth;
  const height = window.innerHeight - 64; // minus nav bar

  const adj = useMemo(() => (topology ? buildAdjacency(topology) : new Map<string, string[]>()), [topology]);
  const systemGates = useMemo(() => (topology ? buildSystemGates(topology) : new Map<string, string>()), [topology]);
  const activityBySystem = useMemo(() => {
    const m = new Map<string, number>();
    for (const a of lanes?.systemActivity ?? []) m.set(a.system, a.realizedProfit);
    return m;
  }, [lanes]);
  const [pan, setPan] = useState({ x: 0, y: 0 });

  // Center once per topology (mirrors GalaxyView's centeredKeyRef guard).
  useEffect(() => {
    if (!stageRef.current || !topology || topology.systems.length === 0) return;
    const key = topology.systems.map((s) => s.symbol).sort().join(',');
    if (centeredRef.current === key) return;
    centeredRef.current = key;
    const avgX = topology.systems.reduce((sum, s) => sum + s.x, 0) / topology.systems.length;
    const avgY = topology.systems.reduce((sum, s) => sum + s.y, 0) / topology.systems.length;
    const initial = 0.4;
    setScale(initial);
    stageRef.current.scale({ x: initial, y: initial });
    stageRef.current.position({ x: width / 2 - avgX * initial, y: height / 2 - avgY * initial });
    setPan({ x: width / 2 - avgX * initial, y: height / 2 - avgY * initial });
  }, [topology, width, height]);

  const systemPos: Map<string, Point> = topology ? buildSystemIndex(topology) : new Map();
  const flows = live?.flows ?? [];
  const homeSystem = topology?.homeSystem ?? null;

  const feedLost = live?.feedLost ?? false;
  const renderFlows = feedLost && staleFlows ? staleFlows : flows;
  const clockMs = feedLost && freezeAtMs ? freezeAtMs : nowMs; // frozen clock = frozen glides
  const staleOpacity = feedLost ? 0.45 : 1;
  const presence = useFlowPresence(renderFlows, clockMs);

  // One-shot camera ease to a focused flow (roster/card click), then release.
  useEffect(() => {
    if (!focusFlowId || !stageRef.current || !topology) return;
    const flow = flows.find((f) => f.containerId === focusFlowId);
    if (flow) {
      const m = projectFlowMotion(flow, adj, systemGates, systemPos, Date.now(), scale);
      if (m) {
        const stage = stageRef.current;
        stage.to({
          x: width / 2 - m.x * scale,
          y: height / 2 - m.y * scale,
          duration: 0.6,
          onFinish: () => setPan({ x: stage.x(), y: stage.y() }),
        });
      }
    }
    clearFocus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusFlowId]);

  const handleWheel = (e: Konva.KonvaEventObject<WheelEvent>) => {
    e.evt.preventDefault();
    const stage = e.target.getStage();
    if (!stage) return;
    const oldScale = stage.scaleX();
    const pointer = stage.getPointerPosition();
    if (!pointer) return;
    const mousePointTo = { x: (pointer.x - stage.x()) / oldScale, y: (pointer.y - stage.y()) / oldScale };
    const delta = e.evt.deltaY > 0 ? CANVAS_CONSTANTS.ZOOM_OUT_FACTOR : CANVAS_CONSTANTS.ZOOM_IN_FACTOR;
    const newScale = Math.max(
      CANVAS_CONSTANTS.MIN_ZOOM_GALAXY,
      Math.min(CANVAS_CONSTANTS.MAX_ZOOM_GALAXY, oldScale * delta),
    );
    stage.scale({ x: newScale, y: newScale });
    stage.position({ x: pointer.x - mousePointTo.x * newScale, y: pointer.y - mousePointTo.y * newScale });
    setScale(newScale);
    setPan({ x: stage.x(), y: stage.y() });
  };

  // Mount the Stage unconditionally (even before topology loads) so stageRef is
  // attached by the time the centering effect fires. Gating the Stage behind a
  // loading return races the ref and leaves the galaxy uncentered at scale 1
  // (mirrors GalaxyView, which always mounts its Stage).
  return (
    <div
      className="relative w-full h-full overflow-hidden"
      // isolation creates a stacking context so the zIndex:-1 AmbientBackdrop
      // paints above this wrapper's opaque background instead of behind it
      // (mirrors SpaceMap; without it the nebula/starfield is invisible).
      style={{ isolation: 'isolate', background: NOIR.bg0 }}
    >
      <AmbientBackdrop pan={pan} />
      <Stage
        ref={stageRef}
        width={width}
        height={height}
        draggable
        onWheel={handleWheel}
        onDragMove={(e) => {
          const stage = e.target.getStage();
          if (stage && e.target === stage) setPan({ x: stage.x(), y: stage.y() });
        }}
      >
        <Layer>
          {topology && (
            <>
          {/* Freshness halos paint first — under lanes, edges, nodes, ships. */}
          {layerToggles.freshness && freshness && (
            <FreshnessLayer
              records={freshness.systems}
              systemPos={systemPos}
              scale={scale}
              nowMs={nowMs}
              degraded={freshnessMissedPolls >= 5}
            />
          )}

          {layerToggles.lanes && (
            <FlowLaneLayer
              records={lanes ? lanes.systemLanes : null}
              systemPos={systemPos}
              scale={scale}
              nowMs={nowMs}
              onLaneHover={(key, x, y) => setTooltip(key ? { kind: 'lane', key, x, y } : null)}
            />
          )}

          {/* Gate edges as hairlines (dashed when under construction) */}
          <Group listening={false}>
            {topology.edges.map((e, i) => {
              const a = systemPos.get(e.from);
              const b = systemPos.get(e.to);
              if (!a || !b) return null;
              return (
                <Line
                  key={`edge-${i}-${e.from}-${e.to}`}
                  points={[a.x, a.y, b.x, b.y]}
                  stroke={noirAlpha(NOIR.nebula, 0.6)}
                  strokeWidth={Math.max(0.25, 0.5 / scale)}
                  dash={e.underConstruction ? [4 / scale, 4 / scale] : undefined}
                  listening={false}
                />
              );
            })}
          </Group>

          {/* System nodes — the home system is ringed + always labeled (distinct
              star treatment), so it reads apart from ordinary nodes at any zoom.
              Ordinary nodes size/brighten with realized activity. */}
          <Group>
            {topology.systems.map((s) => {
              const isHome = homeSystem !== null && s.symbol === homeSystem;
              const activity = activityBySystem.get(s.symbol) ?? 0;
              const bump = activity !== 0 ? Math.min(5, Math.max(0, Math.log10(Math.abs(activity) + 10) - 3)) : 0;
              const nodeR = Math.max(2, (3 + bump) / scale);
              return (
                <Group key={s.symbol} x={s.x} y={s.y}>
                  {isHome && (
                    <>
                      <Circle radius={nodeR + 9 / scale} stroke={NOIR.star} strokeWidth={1 / scale} opacity={0.9} listening={false} />
                      <Circle radius={nodeR + 5 / scale} stroke={noirAlpha(NOIR.star, 0.5)} strokeWidth={0.75 / scale} listening={false} />
                    </>
                  )}
                  <Circle
                    radius={nodeR}
                    fill={isHome ? NOIR.star : noirAlpha(NOIR.nebulaCore, Math.min(1, 0.55 + 0.09 * bump))}
                    stroke={isHome ? NOIR.star : NOIR.accent}
                    strokeWidth={0.5 / scale}
                    onMouseEnter={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
                    onMouseLeave={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
                    onClick={() => openDrilldown(s.symbol)}
                  />
                  {(isHome || scale > 0.3) && (
                    <Text
                      text={isHome ? `★ ${s.symbol} · HOME` : s.symbol}
                      fontSize={Math.max(5, 8 / scale)}
                      fontStyle={isHome ? 'bold' : 'normal'}
                      fill={isHome ? NOIR.star : NOIR.dim}
                      x={(isHome ? nodeR + 9 / scale : 4 / scale)}
                      y={-4 / scale}
                      listening={false}
                    />
                  )}
                </Group>
              );
            })}
          </Group>

          {/* Plan paths for flows that actually published intent */}
          {layerToggles.paths &&
            presence
              .filter((p) => p.flow.remainingHops.length > 0)
              .map((p) => (
                <Group key={`pp-${p.flow.containerId}`} opacity={p.opacity * staleOpacity} listening={false}>
                  <FlowPlanPath
                    flow={p.flow}
                    adj={adj}
                    systemPos={systemPos}
                    scale={scale}
                    bright={p.flow.containerId === hoveredFlowId || p.flow.containerId === selectedFlowId}
                  />
                </Group>
              ))}

          {layerToggles.ships && (
            <FlowShipLayer
              flows={presence.map((p) => p.flow)}
              adj={adj}
              systemGates={systemGates}
              systemPos={systemPos}
              nowMs={clockMs}
              scale={scale}
              selectedFlowId={selectedFlowId}
              onSelect={selectFlow}
              onHover={hoverFlow}
              opacityById={new Map(presence.map((p) => [p.flow.containerId, p.opacity * staleOpacity]))}
            />
          )}
            </>
          )}
        </Layer>
      </Stage>
      {!topology && (
        <div
          className="absolute inset-0 flex items-center justify-center pointer-events-none"
          style={{ color: NOIR.muted }}
        >
          Loading gate network…
        </div>
      )}
    </div>
  );
}

// Enter/exit presence: new flows fade in over 2s; departed flows linger 2s
// fading out, rendered from their last snapshot. The FIRST non-empty delivery
// (initial page population) mounts at full opacity — fading the whole fleet in
// from nothing on load reads as "no ships", and freezes invisible in stills.
function useFlowPresence(
  flows: LiveFlow[],
  nowMs: number,
): { flow: LiveFlow; opacity: number }[] {
  const ref = useRef(new Map<string, { flow: LiveFlow; enterAt: number; exitAt: number | null }>());
  const bootedRef = useRef(false);
  const seen = new Set<string>();
  for (const f of flows) {
    seen.add(f.containerId);
    const cur = ref.current.get(f.containerId);
    if (cur) { cur.flow = f; cur.exitAt = null; }
    else ref.current.set(f.containerId, { flow: f, enterAt: bootedRef.current ? nowMs : nowMs - 2000, exitAt: null });
  }
  if (flows.length > 0) bootedRef.current = true;
  for (const [id, rec] of ref.current) {
    if (!seen.has(id) && rec.exitAt === null) rec.exitAt = nowMs;
    if (rec.exitAt !== null && nowMs - rec.exitAt > 2000) ref.current.delete(id);
  }
  const out: { flow: LiveFlow; opacity: number }[] = [];
  for (const rec of ref.current.values()) {
    const enter = Math.min(1, (nowMs - rec.enterAt) / 2000);
    const exit = rec.exitAt === null ? 1 : Math.max(0, 1 - (nowMs - rec.exitAt) / 2000);
    out.push({ flow: rec.flow, opacity: enter * exit });
  }
  return out;
}
