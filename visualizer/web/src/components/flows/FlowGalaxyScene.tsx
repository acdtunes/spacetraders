import { useEffect, useRef, useState } from 'react';
import { Stage, Layer, Circle, Text, Group, Line } from 'react-konva';
import type Konva from 'konva';
import { useFlowStore } from '../../store/flowStore';
import { useRafClock } from '../../hooks/useRafClock';
import { CANVAS_CONSTANTS } from '../../constants/canvas';
import { NOIR, noirAlpha } from '../../theme/noir';
import { buildSystemIndex, type Point } from './flowGeometry';
import { FlowLaneLayer } from './FlowLaneLayer';
import { FlowShipLayer } from './FlowShipLayer';
import { FlowPlanPath } from './FlowPlanPath';

export default function FlowGalaxyScene() {
  const topology = useFlowStore((s) => s.topology);
  const lanes = useFlowStore((s) => s.lanes);
  const live = useFlowStore((s) => s.live);
  const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
  const selectFlow = useFlowStore((s) => s.selectFlow);
  const openDrilldown = useFlowStore((s) => s.openDrilldown);

  const stageRef = useRef<Konva.Stage>(null);
  const [scale, setScale] = useState(0.5);
  const nowMs = useRafClock();
  const centeredRef = useRef<string | null>(null);

  const width = window.innerWidth;
  const height = window.innerHeight - 64; // minus nav bar

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
  }, [topology, width, height]);

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
  };

  const systemPos: Map<string, Point> = topology ? buildSystemIndex(topology) : new Map();
  const flows = live?.flows ?? [];
  const homeSystem = topology?.homeSystem ?? null;

  // Mount the Stage unconditionally (even before topology loads) so stageRef is
  // attached by the time the centering effect fires. Gating the Stage behind a
  // loading return races the ref and leaves the galaxy uncentered at scale 1
  // (mirrors GalaxyView, which always mounts its Stage).
  return (
    <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
      <Stage ref={stageRef} width={width} height={height} draggable onWheel={handleWheel}>
        <Layer>
          {topology && (
            <>
          <FlowLaneLayer lanes={lanes} systemPos={systemPos} scale={scale} nowMs={nowMs} />

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
              star treatment), so it reads apart from ordinary nodes at any zoom. */}
          <Group>
            {topology.systems.map((s) => {
              const isHome = homeSystem !== null && s.symbol === homeSystem;
              const nodeR = Math.max(2, 3 / scale);
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
                    fill={isHome ? NOIR.star : noirAlpha(NOIR.nebulaCore, 0.9)}
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
          <Group listening={false}>
            {flows.filter((f) => f.remainingHops.length > 0).map((f) => (
              <FlowPlanPath key={`pp-${f.containerId}`} flow={f} systemPos={systemPos} scale={scale} />
            ))}
          </Group>

          <FlowShipLayer
            flows={flows}
            systemPos={systemPos}
            nowMs={nowMs}
            scale={scale}
            selectedFlowId={selectedFlowId}
            onSelect={selectFlow}
          />
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
