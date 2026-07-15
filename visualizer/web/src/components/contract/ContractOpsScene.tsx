import { useEffect, useMemo, useRef, useState } from 'react';
import { Stage, Layer, Circle, Text, Group, Line, Arc, RegularPolygon, Rect } from 'react-konva';
import type Konva from 'konva';
import { useContractOpsStore } from '../../store/contractOpsStore';
import { useRafClock } from '../../hooks/useRafClock';
import { CANVAS_CONSTANTS } from '../../constants/canvas';
import { NOIR, noirAlpha } from '../../theme/noir';
import { positionShip } from '../../utils/transitMemory';
import { selectWaypointAsset, selectShipAssetByRole, waypointVisualRadius } from '../../utils/spriteAssets';
import { WaypointSprite } from '../WaypointSprite';
import { ShipSprite } from '../ShipSprite';
import type { OpsShip, OpsShipRole } from '../../types/contractOps';

// Systems get their own patch of the plane (waypoint x/y are per-system coords).
const SYSTEM_SPREAD = 340;

export const ROLE_COLORS: Record<OpsShipRole, string> = {
  worker: NOIR.warn,
  delivery: NOIR.good,
  stocker: NOIR.accent,
  warehouse: NOIR.star,
  contract: NOIR.muted,
};

// Warehouse donut segments: shade by rank within the ring; the good's name is
// carried by the label, never by color alone.
const RING_SHADES = [NOIR.accent, NOIR.good, NOIR.warn, NOIR.accentSoft];

const shortName = (symbol: string) => symbol.replace(/^[A-Z0-9]+-/, '');
const systemOf = (waypoint: string) => waypoint.split('-').slice(0, 2).join('-');

export default function ContractOpsScene() {
  const topology = useContractOpsStore((s) => s.topology);
  const live = useContractOpsStore((s) => s.live);
  const memory = useContractOpsStore((s) => s.memory);
  const pass = useContractOpsStore((s) => s.pass);
  const selectedShip = useContractOpsStore((s) => s.selectedShip);
  const selectShip = useContractOpsStore((s) => s.selectShip);

  const stageRef = useRef<Konva.Stage>(null);
  const [scale, setScale] = useState(2.2);
  const nowMs = useRafClock();
  const centeredRef = useRef<string | null>(null);

  const width = window.innerWidth;
  const height = window.innerHeight - 64; // minus nav bar

  // system → x-offset, so multi-system depots render side by side.
  const systemOffset = useMemo(() => {
    const offsets = new Map<string, number>();
    (topology?.systems ?? []).forEach((s, i) => offsets.set(s, i * SYSTEM_SPREAD));
    return offsets;
  }, [topology]);

  const wpPos = useMemo(() => {
    const index = new Map<string, { x: number; y: number }>();
    for (const w of topology?.waypoints ?? []) {
      index.set(w.symbol, { x: w.x + (systemOffset.get(w.system) ?? 0), y: w.y });
    }
    return index;
  }, [topology, systemOffset]);

  const shipPos = (ship: OpsShip) => {
    const dx = systemOffset.get(ship.system) ?? 0;
    let mem = memory.get(ship.symbol);
    // Cross-system flights (rare — contract ops are single-system by ruling
    // #14) can't be lerped across system patches; degrade them to inbound.
    if (mem?.transit && systemOf(mem.transit.originWaypoint) !== ship.system) mem = { ...mem, transit: undefined };
    const p = positionShip(
      { symbol: ship.symbol, navStatus: ship.navStatus, waypoint: ship.waypoint, x: ship.x, y: ship.y, arrivalTime: ship.arrivalTime },
      mem,
      nowMs,
    );
    return { ...p, x: p.x + dx, y: p.y, mem };
  };

  // Center once per topology (mirrors FlowGalaxyScene's guard).
  useEffect(() => {
    if (!stageRef.current || !topology || topology.waypoints.length === 0) return;
    const key = topology.systems.join(',');
    if (centeredRef.current === key) return;
    centeredRef.current = key;
    let sx = 0, sy = 0;
    for (const w of topology.waypoints) {
      sx += w.x + (systemOffset.get(w.system) ?? 0);
      sy += w.y;
    }
    const avgX = sx / topology.waypoints.length;
    const avgY = sy / topology.waypoints.length;
    const initial = 2.2;
    setScale(initial);
    stageRef.current.scale({ x: initial, y: initial });
    stageRef.current.position({ x: width / 2 - avgX * initial, y: height / 2 - avgY * initial });
  }, [topology, systemOffset, width, height]);

  const handleWheel = (e: Konva.KonvaEventObject<WheelEvent>) => {
    e.evt.preventDefault();
    const stage = e.target.getStage();
    if (!stage) return;
    const oldScale = stage.scaleX();
    const pointer = stage.getPointerPosition();
    if (!pointer) return;
    const mousePointTo = { x: (pointer.x - stage.x()) / oldScale, y: (pointer.y - stage.y()) / oldScale };
    const delta = e.evt.deltaY > 0 ? CANVAS_CONSTANTS.ZOOM_OUT_FACTOR : CANVAS_CONSTANTS.ZOOM_IN_FACTOR;
    const newScale = Math.max(0.4, Math.min(14, oldScale * delta));
    stage.scale({ x: newScale, y: newScale });
    stage.position({ x: pointer.x - mousePointTo.x * newScale, y: pointer.y - mousePointTo.y * newScale });
    setScale(newScale);
  };

  const depots = topology?.depots ?? [];
  const ships = live?.ships ?? [];
  const pulse = 0.5 + 0.5 * Math.sin(nowMs / 400);

  // Warehouse fill by waypoint (top goods first — server pre-sorts).
  const levelsByWaypoint = useMemo(() => {
    const map = new Map<string, Array<{ good: string; units: number }>>();
    for (const l of live?.warehouses ?? []) {
      const list = map.get(l.waypoint) ?? [];
      list.push({ good: l.good, units: l.units });
      map.set(l.waypoint, list);
    }
    return map;
  }, [live]);

  // Delivery-hub anchors + Voronoi-ish service radius (half distance to the
  // nearest other hub) — the on-map face of "clusters are nearest-hull-wins".
  const deliveryHubs = useMemo(() => {
    const hubs: Array<{ waypoint: string; shipSymbol: string; x: number; y: number; radius: number; depotId: string }> = [];
    for (const depot of depots) {
      for (const hull of depot.deliveryHulls) {
        const p = wpPos.get(hull.waypoint);
        if (p) hubs.push({ waypoint: hull.waypoint, shipSymbol: hull.shipSymbol, x: p.x, y: p.y, radius: 24, depotId: depot.id });
      }
    }
    for (const hub of hubs) {
      let nearest = Infinity;
      for (const other of hubs) {
        if (other === hub) continue;
        const d = Math.hypot(other.x - hub.x, other.y - hub.y);
        if (d < nearest) nearest = d;
      }
      hub.radius = Number.isFinite(nearest) ? Math.min(42, Math.max(10, nearest / 2)) : 30;
    }
    return hubs;
  }, [depots, wpPos]);

  // The active destination's serving hub: nearest delivery hub, engine rule.
  const clusterLinks = useMemo(() => {
    const links: Array<{ from: { x: number; y: number }; to: { x: number; y: number } }> = [];
    for (const dest of live?.destinations ?? []) {
      const dx = systemOffset.get(dest.system) ?? 0;
      const to = { x: dest.x + dx, y: dest.y };
      let best: { x: number; y: number } | null = null;
      let bestD = Infinity;
      for (const hub of deliveryHubs) {
        const d = Math.hypot(hub.x - to.x, hub.y - to.y);
        if (d < bestD) { bestD = d; best = { x: hub.x, y: hub.y }; }
      }
      if (best) links.push({ from: best, to });
    }
    return links;
  }, [live, deliveryHubs, systemOffset]);

  // Waypoints that carry their own operation labels skip the backdrop label.
  const featuredWaypoints = useMemo(() => {
    const set = new Set<string>();
    for (const d of live?.destinations ?? []) set.add(d.symbol);
    for (const depot of depots) {
      for (const e of [...depot.warehouses, ...depot.sourceHubs, ...depot.deliveryHulls]) set.add(e.waypoint);
    }
    return set;
  }, [live, depots]);

  // Co-located stationary ships fan out on a small parking ring (sorted, so
  // slots are stable across polls); orbiters drift slowly around it.
  const stationaryLayout = useMemo(() => {
    const groups = new Map<string, string[]>();
    for (const s of ships) {
      if (s.navStatus === 'IN_TRANSIT') continue;
      const list = groups.get(s.waypoint) ?? [];
      list.push(s.symbol);
      groups.set(s.waypoint, list);
    }
    const layout = new Map<string, { angle0: number; ring: number }>();
    for (const [wp, symbols] of groups) {
      symbols.sort();
      symbols.forEach((sym, i) =>
        layout.set(sym, {
          angle0: (i / symbols.length) * Math.PI * 2 + wp.length * 0.7,
          ring: 6 + Math.floor(i / 8) * 4,
        }),
      );
    }
    return layout;
  }, [ships]);

  const hair = (w: number) => Math.max(0.15, w / scale);
  // Labels and their offsets are SCREEN-sized (constant px at any zoom).
  // World-sized text with a floor turns into billboards when zoomed in.
  const font = (f: number) => f / scale;
  const sp = (px: number) => px / scale;

  return (
    <div className="absolute inset-0" style={{ background: NOIR.bg0 }}>
      <Stage
        ref={stageRef}
        width={width}
        height={height}
        draggable
        onWheel={handleWheel}
        onClick={(e) => { if (e.target === e.target.getStage()) selectShip(null); }}
      >
        <Layer>
          {topology && (
            <>
              {/* Backdrop: every waypoint in the involved systems */}
              <Group listening={false}>
                {topology.waypoints.map((w) => {
                  const p = wpPos.get(w.symbol)!;
                  const r = waypointVisualRadius(w.type);
                  const featured = featuredWaypoints.has(w.symbol);
                  return (
                    <Group key={w.symbol} x={p.x} y={p.y} opacity={featured ? 1 : 0.82}>
                      <WaypointSprite
                        assetPath={selectWaypointAsset(w.symbol, w.type, w.traits)}
                        type={w.type}
                        x={0}
                        y={0}
                        radius={r}
                        scale={scale}
                      />
                      {scale > 5 && !featured && (
                        <Text text={shortName(w.symbol)} fontSize={font(10)} fill={NOIR.dim} x={r + sp(6)} y={-font(10) / 2} fontFamily="monospace" />
                      )}
                    </Group>
                  );
                })}
                {topology.systems.map((s) => (
                  <Text
                    key={s}
                    text={s}
                    fontSize={font(13)}
                    fill={noirAlpha(NOIR.dim, 0.8)}
                    fontFamily="monospace"
                    x={(systemOffset.get(s) ?? 0) - 30}
                    y={118}
                    listening={false}
                  />
                ))}
              </Group>

              {/* Pass 0+ · destination beacons */}
              <Group listening={false}>
                {(live?.destinations ?? []).map((d) => {
                  const dx = systemOffset.get(d.system) ?? 0;
                  const good = live?.contract?.deliveries.find((del) => del.destinationSymbol === d.symbol)?.tradeSymbol;
                  return (
                    <Group key={d.symbol} x={d.x + dx} y={d.y}>
                      <Circle radius={6 + pulse * 2.4} stroke={NOIR.warn} strokeWidth={hair(1.4)} opacity={0.85 - pulse * 0.4} />
                      <Circle radius={3.2} stroke={NOIR.warn} strokeWidth={hair(1.2)} />
                      <Line points={[-8, 0, -4.5, 0]} stroke={NOIR.warn} strokeWidth={hair(1)} />
                      <Line points={[4.5, 0, 8, 0]} stroke={NOIR.warn} strokeWidth={hair(1)} />
                      <Line points={[0, -8, 0, -4.5]} stroke={NOIR.warn} strokeWidth={hair(1)} />
                      <Line points={[0, 4.5, 0, 8]} stroke={NOIR.warn} strokeWidth={hair(1)} />
                      {/* own lane: above the beacon, clear of the warehouse/ship lanes */}
                      <Text
                        text={`◈ ${good ?? 'DELIVERY'} → ${shortName(d.symbol)}`}
                        fontSize={font(12)}
                        fill={NOIR.warn}
                        fontFamily="monospace"
                        x={-sp(10)}
                        y={-9 - sp(22)}
                        shadowColor={NOIR.bg0}
                        shadowBlur={sp(4)}
                        shadowOpacity={0.9}
                      />
                    </Group>
                  );
                })}
              </Group>

              {/* Pass 1+ · depot topology */}
              {pass >= 1 && (
                <Group listening={false}>
                  {/* cluster service territories + serving link */}
                  {deliveryHubs.map((hub) => (
                    <Group key={`hub-${hub.depotId}-${hub.waypoint}-${hub.shipSymbol}`} x={hub.x} y={hub.y}>
                      <Circle radius={hub.radius} stroke={noirAlpha(NOIR.good, 0.28)} strokeWidth={hair(0.8)} dash={[hair(4), hair(6)]} />
                      <Circle radius={2.1} stroke={noirAlpha(NOIR.good, 0.85)} strokeWidth={hair(1)} />
                    </Group>
                  ))}
                  {clusterLinks.map((l, i) => (
                    <Line
                      key={`cl-${i}`}
                      points={[l.from.x, l.from.y, l.to.x, l.to.y]}
                      stroke={noirAlpha(NOIR.good, 0.5)}
                      strokeWidth={hair(1)}
                      dash={[hair(3), hair(4)]}
                    />
                  ))}

                  {/* source hubs (dashed diamonds; uncrewed slots dimmer) */}
                  {depots.flatMap((depot) =>
                    depot.sourceHubs.map((hub, i) => {
                      const p = wpPos.get(hub.waypoint);
                      if (!p) return null;
                      const crewed = hub.shipSymbol !== '';
                      return (
                        <Group key={`src-${depot.id}-${i}`} x={p.x} y={p.y}>
                          <RegularPolygon
                            sides={4}
                            radius={4}
                            rotation={0}
                            stroke={noirAlpha(NOIR.accent, crewed ? 0.9 : 0.4)}
                            strokeWidth={hair(1.1)}
                            dash={crewed ? undefined : [hair(2), hair(2)]}
                          />
                          {scale > 3 && (
                            <Text text="HUB" fontSize={font(9)} fill={noirAlpha(NOIR.accent, 0.7)} fontFamily="monospace" x={4 + sp(5)} y={4 + sp(2)} shadowColor={NOIR.bg0} shadowBlur={sp(4)} shadowOpacity={0.9} />
                          )}
                        </Group>
                      );
                    }),
                  )}

                  {/* warehouses: hex + stock ring */}
                  {depots.flatMap((depot) =>
                    depot.warehouses.map((wh, i) => {
                      const p = wpPos.get(wh.waypoint);
                      if (!p) return null;
                      const levels = levelsByWaypoint.get(wh.waypoint) ?? [];
                      const total = levels.reduce((sum, l) => sum + l.units, 0);
                      let angle = -90;
                      return (
                        <Group key={`wh-${depot.id}-${i}-${wh.shipSymbol}`} x={p.x} y={p.y}>
                          <RegularPolygon sides={6} radius={5.2} stroke={NOIR.star} strokeWidth={hair(1.3)} fill={noirAlpha(NOIR.star, 0.12)} />
                          {levels.slice(0, RING_SHADES.length).map((l, li) => {
                            const sweep = total > 0 ? (l.units / total) * 360 : 0;
                            const arc = (
                              <Arc
                                key={l.good}
                                innerRadius={7.2}
                                outerRadius={8.8}
                                angle={Math.max(sweep - 4, 2)}
                                rotation={angle}
                                fill={noirAlpha(RING_SHADES[li], 0.9)}
                              />
                            );
                            angle += sweep;
                            return arc;
                          })}
                          {/* own lane: right of the stock ring, goods stacked beneath */}
                          <Text
                            text={`⬢ ${depot.id.toUpperCase()} · ${wh.shipSymbol || 'uncrewed'}${total > 0 ? ` · ${total}u` : ' · empty'}`}
                            fontSize={font(11)}
                            fill={NOIR.star}
                            fontFamily="monospace"
                            x={9.5 + sp(6)}
                            y={-font(11) / 2}
                            shadowColor={NOIR.bg0}
                            shadowBlur={sp(4)}
                            shadowOpacity={0.9}
                          />
                          {scale > 3.4 &&
                            levels.slice(0, RING_SHADES.length).map((l, li) => (
                              <Text
                                key={`lbl-${l.good}`}
                                text={`${l.good} ${l.units}`}
                                fontSize={font(9.5)}
                                fill={noirAlpha(RING_SHADES[li], 1)}
                                fontFamily="monospace"
                                x={9.5 + sp(6)}
                                y={font(11) / 2 + sp(4) + li * font(13)}
                                shadowColor={NOIR.bg0}
                                shadowBlur={sp(4)}
                                shadowOpacity={0.9}
                              />
                            ))}
                        </Group>
                      );
                    }),
                  )}
                </Group>
              )}

              {/* Pass 2+ · the fleet */}
              {pass >= 2 && (
                <Group>
                  {ships.map((ship) => {
                    const p = shipPos(ship);
                    const color = ROLE_COLORS[ship.role];
                    const selected = selectedShip === ship.symbol;
                    const emphasized = selected || ship.role === 'worker';
                    const reg = ship.registrationRole.toUpperCase();
                    const isSmallHull = reg.includes('PROBE') || reg.includes('SATELLITE') || reg.includes('SURVEYOR');
                    const spriteSize = ship.role === 'worker' ? 7.2 : isSmallHull ? 3.6 : 5.8;
                    const r = spriteSize * 0.62; // role ring radius; adornments key off it
                    const cargoFrac = ship.cargoCapacity > 0 ? ship.cargoUnits / ship.cargoCapacity : 0;
                    let gx = p.x;
                    let gy = p.y;
                    if (p.mode === 'stationary') {
                      const lay = stationaryLayout.get(ship.symbol);
                      if (lay) {
                        const angle = lay.angle0 + (ship.navStatus === 'IN_ORBIT' ? nowMs / 30000 : 0);
                        gx += Math.cos(angle) * lay.ring;
                        gy += Math.sin(angle) * lay.ring;
                      }
                    }
                    return (
                      <Group
                        key={ship.symbol}
                        x={gx}
                        y={gy}
                        onClick={() => selectShip(selected ? null : ship.symbol)}
                        onTap={() => selectShip(selected ? null : ship.symbol)}
                        onMouseEnter={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
                        onMouseLeave={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
                      >
                        {/* generous hit target */}
                        <Circle radius={Math.max(r + 1.5, 10 / scale)} fill="transparent" />
                        {selected && <Circle radius={r + 2.6} stroke={NOIR.ink} strokeWidth={hair(1)} opacity={0.9} listening={false} />}

                        {/* engine wake while flying a reconstructed path */}
                        {p.mode === 'exact' && p.headingRad !== null && (
                          <Group listening={false}>
                            {[0, 1, 2].map((ti) => (
                              <Circle
                                key={ti}
                                x={-Math.cos(p.headingRad!) * (r + 2 + ti * 2.6)}
                                y={-Math.sin(p.headingRad!) * (r + 2 + ti * 2.6)}
                                radius={Math.max(0.5, 0.9 - ti * 0.25)}
                                fill={noirAlpha(color, 0.4 - ti * 0.1)}
                              />
                            ))}
                          </Group>
                        )}
                        {p.mode === 'inbound' && (
                          <Circle radius={r + 1.6 + pulse * 1.2} stroke={noirAlpha(color, 0.55)} strokeWidth={hair(1)} dash={[hair(2), hair(2)]} listening={false} />
                        )}

                        {/* role ring (the op-role encoding) around the hull sprite */}
                        <Circle radius={r} stroke={noirAlpha(color, ship.role === 'worker' ? 0.95 : 0.65)} strokeWidth={hair(ship.role === 'worker' ? 1.4 : 1)} listening={false} />
                        <Group rotation={p.mode === 'exact' && p.headingRad !== null ? (p.headingRad * 180) / Math.PI + 90 : 0} listening={false}>
                          <ShipSprite
                            assetPath={selectShipAssetByRole(ship.symbol, ship.registrationRole)}
                            size={spriteSize}
                            showEngineGlow={p.mode === 'exact'}
                            frameTimestamp={nowMs}
                          />
                        </Group>

                        {/* cargo bar */}
                        {ship.cargoCapacity > 0 && (
                          <Group y={r + 2.2} listening={false}>
                            <Rect x={-4} width={8} height={1.1} fill={noirAlpha(NOIR.panel, 0.9)} stroke={noirAlpha(NOIR.dim, 0.5)} strokeWidth={hair(0.4)} cornerRadius={0.5} />
                            <Rect x={-4} width={8 * cargoFrac} height={1.1} fill={noirAlpha(color, 0.95)} cornerRadius={0.5} />
                          </Group>
                        )}

                        {(emphasized || scale > 4.2) && (
                          <Text
                            text={`${shortName(ship.symbol)}${ship.role === 'worker' ? ' ◆ WORKER' : ''}${p.mode === 'inbound' ? ' · inbound' : ''}`}
                            fontSize={font(10)}
                            fill={emphasized ? color : NOIR.muted}
                            fontFamily="monospace"
                            x={r + sp(5)}
                            y={-font(10) - sp(2)}
                            shadowColor={NOIR.bg0}
                            shadowBlur={sp(4)}
                            shadowOpacity={0.9}
                            listening={false}
                          />
                        )}
                      </Group>
                    );
                  })}
                </Group>
              )}
            </>
          )}
        </Layer>
      </Stage>
      {!topology && (
        <div className="absolute inset-0 flex items-center justify-center pointer-events-none" style={{ color: NOIR.muted }}>
          Charting the depot system…
        </div>
      )}
      {topology && topology.depots.length === 0 && (
        <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
          <div className="px-4 py-3 rounded text-sm" style={{ background: NOIR.panel, color: NOIR.muted }}>
            No contract depots configured — the fleet is running depot-less. The contract card still tracks the live loop.
          </div>
        </div>
      )}
    </div>
  );
}
