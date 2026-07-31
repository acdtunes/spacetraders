// Mount shell for the Living Nebula: owns the pixi Application lifecycle, the
// layer scaffold, and the camera loop (600ms eased flights via lerpCam). It
// draws nothing itself — later tasks attach content to the layer registry. If
// WebGL is unavailable (init rejects) the scene degrades to a static notice;
// the surrounding panels stay live.
import { useCallback, useEffect, useRef, useState, type MutableRefObject } from 'react';
import { Application } from 'pixi.js';
import { createLayers, type Layers, type PointerHooks } from './layers/registry';
import { buildBackdrop, BACKDROP_COLOR } from './layers/backdrop';
import { buildGalaxyBand, type GalaxyBandHandle } from './layers/galaxyBand';
import { buildOrbs, type OrbsHandle } from './layers/orbs';
import { buildStreams, type StreamsHandle } from './layers/streams';
import { buildSystemBand, clearSystemBand, type SystemBandHandle } from './layers/systemBand';
import { useSystemDetail, type SystemDetail } from './useSystemDetail';
import { bandFor, type Band } from './lod';
import { anchoredZoom, fitTransform, lerpCam, worldBounds, type CamXform } from './camera';
import type { SceneData } from './sceneData';

export interface NebulaApi {
  fitGalaxy(): void;
  focusSystem(symbol: string): void;
  focusTour(flowId: string): void;
  band(): Band;
}

export type HoverTarget = {
  kind: 'system' | 'lane' | 'current';
  key: string;
  clientX: number;
  clientY: number;
};

/** Page-owned layer visibility switches (the Lanes/Paths/Ships/Freshness/Lattice
 * buttons). lanes/ships gate the REGION-band stream + mote containers, paths
 * gates the GALAXY-band currents (the nearest surviving "routes across the
 * galaxy" visual — per-flow plan polylines did not carry over), freshness gates
 * every market-freshness visual in the scene: the GALAXY band's per-cluster
 * rings, the REGION band's per-system auras and dark rings, and the SYSTEM
 * band's per-waypoint rings.
 *
 * `lattice` is the odd one out: the other four SUBTRACT from the default view,
 * so they default on, while lattice ADDS the far thread tier that the SYSTEM
 * band would otherwise be the only way to see — so it defaults OFF. */
export interface NebulaLayerToggles {
  lanes: boolean;
  paths: boolean;
  ships: boolean;
  freshness: boolean;
  lattice: boolean;
}

/** Absent-prop default (dev harness, tests): everything the view shows by
 * default — which is every subtractive toggle on and the additive one off. */
const DEFAULT_LAYERS: NebulaLayerToggles = { lanes: true, paths: true, ships: true, freshness: true, lattice: false };

export interface NebulaSceneProps {
  data: SceneData | null;
  /** Focused-system channel: symbol on orb tap / api focus, null when the focus
   * clears (wheel-out past the SYSTEM band or Escape). */
  onSelectSystem: (sym: string | null) => void;
  onHover: (t: HoverTarget | null) => void;
  apiRef: MutableRefObject<NebulaApi | null>;
  /** Absent → everything visible (dev harness, tests). */
  layerToggles?: NebulaLayerToggles;
  /** The focused system's detail-fetch failure (null once it recovers/clears) —
   * the page surfaces it so a dark SYSTEM band is never silent. */
  onDetailError?: (message: string | null) => void;
}

const FIT_PAD = 60;
const FLIGHT_MS = 600;
/** Band-visibility cross-fade (each band's containers toward 1 or 0). */
const BAND_FADE_MS = 250;
/** Ticker dt clamp — a background-tab resume must not teleport particles. */
const MAX_DT_MS = 100;
/** Below this fade the container is culled outright (visible=false), so a
 * faded-out band costs no render work. */
const FADE_EPS = 0.01;
/** focusSystem camera: z = 12 × fitScale, centered on the system. */
const SYSTEM_FOCUS_ZOOM = 12;
/** Non-focused content alpha while a system is focused. */
const DIM_ALPHA = 0.25;
/** Anchored wheel zoom: delta-proportional (a trackpad flick's dozens of small
 * pixel-mode deltas integrate into one smooth motion instead of compounding a
 * fixed step per event — the "too sensitive" report). Calibrated so one classic
 * wheel notch (100px pixel-mode / 3 lines line-mode) is exactly a ×1.1 step,
 * the old per-notch feel, and in/out round-trip exactly. */
const WHEEL_STEP = 1.1;
const WHEEL_NOTCH_PX = 100; // Chrome/Blink pixel-mode deltaY per notch
const WHEEL_SENSITIVITY = Math.log(WHEEL_STEP) / WHEEL_NOTCH_PX; // ≈ 9.5e-4 per px
const WHEEL_LINE_PX = WHEEL_NOTCH_PX / 3; // deltaMode 1: a Firefox notch = 3 lines
/** Pointer travel (px) below which a down→up still counts as a tap-select. */
const DRAG_THRESHOLD_PX = 3;
/** `?fps=1` overlay: EMA horizon (~frames) and DOM-text cadence (~2 Hz). */
const FPS_EMA_FRAMES = 30;
const FPS_TEXT_MS = 500;

/** Dev-only band override (`?band=region|system`): initial-camera zoom
 * multiplier over the fit scale — region lands at z≈3×fit, system at z≈12×fit.
 * Screenshot/QA seam for the deeper bands; Tasks 11/14 reuse it. */
function devBandZoom(): number {
  if (typeof location === 'undefined') return 1;
  const band = new URLSearchParams(location.search).get('band');
  if (band === 'region') return 3;
  if (band === 'system') return 12;
  return 1;
}

/** Dev-only focus override (`?band=system&focus=<symbol>`): the system the
 * landing camera drills into; absent → the home system, else the first. */
function devFocusParam(): string | null {
  if (typeof location === 'undefined') return null;
  const p = new URLSearchParams(location.search).get('focus');
  return p != null && p !== '' ? p : null;
}

/** Dev-only FPS overlay (`?fps=1`): a small non-interactive DOM readout of the
 * pixi ticker's rolling-average frame rate. Always available — no build flag. */
function devFpsEnabled(): boolean {
  if (typeof location === 'undefined') return false;
  return new URLSearchParams(location.search).get('fps') === '1';
}

interface SceneState {
  app: Application | null;
  layers: Layers | null;
  cam: CamXform;
  /** 0 until the first fit, so band() reads GALAXY before any fit. */
  fitScale: number;
  band: Band;
  flight: { from: CamXform; to: CamXform; startMs: number } | null;
  viewport: { w: number; h: number };
  data: SceneData | null;
  /** Snapshot the backdrop was last built from — rebuild only on identity change. */
  backdropFor: SceneData | null;
  /** Snapshot the GALAXY band was last built from — same identity-guard seam. */
  galaxyBandFor: SceneData | null;
  galaxyBand: GalaxyBandHandle | null;
  /** Current GALAXY-band visibility (0..1); the ticker eases it toward band==='GALAXY'. */
  galaxyFade: number;
  /** Snapshot the REGION band (orbs/threads/labels) was last built from. */
  orbsFor: SceneData | null;
  orbs: OrbsHandle | null;
  /** Snapshot the lane streams + ship motes were last built from. */
  streamsFor: SceneData | null;
  streams: StreamsHandle | null;
  /** Current REGION-band visibility (0..1); eased toward band ∈ {REGION, SYSTEM}. */
  regionFade: number;
  /** The focused system's detail the SYSTEM band was last built from. */
  systemBandFor: SystemDetail | null;
  /** ...and the SceneData snapshot it was paired with (ships move per poll). */
  systemBandDataFor: SceneData | null;
  systemBand: SystemBandHandle | null;
  /** Current SYSTEM-band visibility (0..1); eased toward band === SYSTEM. */
  systemFade: number;
  /** The focused system symbol; null when nothing is focused. */
  focus: string | null;
  /** Scene-level dimmer (1 → DIM_ALPHA while focused) over non-focused content. */
  dim: number;
  didInitialFit: boolean;
  /** System focus consumes this when a flight was requested before data/fit
   * existed. (Tour focus never queues — the follow loop waits on data itself.) */
  pendingFocus: { kind: 'system'; key: string } | null;
  /** The flowId whose ship the camera tracks each tick; wheel/drag/flights
   * break it, and it self-clears when the flow leaves the data. */
  follow: string | null;
  /** Live pointer-drag bookkeeping (screen px); null when no button is down. */
  drag: { pointerId: number | null; startX: number; startY: number; lastX: number; lastY: number } | null;
  /** True once the current press crossed DRAG_THRESHOLD_PX — suppresses the
   * tap-select that this pointer's release would otherwise deliver. Reset on
   * the next pointerdown (not on up: pixi's pointertap dispatch order relative
   * to our own up listener is not guaranteed). */
  dragMoved: boolean;
}

export function NebulaScene({ data, onSelectSystem, onHover, apiRef, layerToggles, onDetailError }: NebulaSceneProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const [failed, setFailed] = useState(false);

  // Dev `?fps=1` overlay: presence decided once per mount; the ticker writes
  // the readout straight into the DOM node (no React state — never per frame).
  const [fpsEnabled] = useState(devFpsEnabled);
  const fpsRef = useRef<HTMLDivElement | null>(null);

  // Ticker-read layer switches (mount-once effect ⇒ refs, never closures).
  const togglesRef = useRef<NebulaLayerToggles>(DEFAULT_LAYERS);
  togglesRef.current = layerToggles ?? DEFAULT_LAYERS;

  const stateRef = useRef<SceneState>({
    app: null,
    layers: null,
    cam: { x: 0, y: 0, scale: 1 },
    fitScale: 0,
    band: 'GALAXY',
    flight: null,
    viewport: { w: 1, h: 1 },
    data: null,
    backdropFor: null,
    galaxyBandFor: null,
    galaxyBand: null,
    galaxyFade: 1, // the landing band is GALAXY — start visible, no fade-in pop
    orbsFor: null,
    orbs: null,
    streamsFor: null,
    streams: null,
    regionFade: 0, // ...and the REGION band starts hidden
    systemBandFor: null,
    systemBandDataFor: null,
    systemBand: null,
    systemFade: 0, // ...as does the SYSTEM band
    focus: null,
    dim: 1,
    didInitialFit: false,
    pendingFocus: null,
    follow: null,
    drag: null,
    dragMoved: false,
  });

  // Focused system as React state so useSystemDetail refetches on change; the
  // imperative paths (api.focusSystem, the ticker's wheel-out clear) drive it
  // through this stable setter.
  const [focusedSymbol, setFocusedSymbol] = useState<string | null>(null);
  const { detail, error: detailError } = useSystemDetail(focusedSymbol);
  const detailRef = useRef<SystemDetail | null>(null);
  detailRef.current = detail;

  // Surface the detail-fetch failure to the page (latest callback via ref so
  // an inline prop lambda cannot retrigger the effect).
  const onDetailErrorRef = useRef(onDetailError);
  onDetailErrorRef.current = onDetailError;
  useEffect(() => {
    onDetailErrorRef.current?.(detailError ?? null);
  }, [detailError]);

  // Pointer hooks the interactive layers (orbs, galaxyBand) call back into.
  // Stable identity (closes over refs only); handlers resolve the live
  // callbacks/api lazily, so builders can capture this once per rebuild.
  const hooksRef = useRef<PointerHooks | null>(null);
  if (hooksRef.current == null) {
    hooksRef.current = {
      hover(kind, key, at) {
        callbacksRef.current.onHover({ kind, key, clientX: at.clientX, clientY: at.clientY });
      },
      hoverOut() {
        callbacksRef.current.onHover(null);
      },
      tapSystem(symbol) {
        // A press that crossed the drag threshold pans — its release is not a select.
        if (stateRef.current.dragMoved) return;
        apiHolder.current?.focusSystem(symbol);
      },
    };
  }

  // Rebuild the backdrop layer for the current SceneData snapshot. Safe to call
  // from both the async init path and the data effect (whichever lands last
  // does the build); no-ops until renderer+layers+data all exist, and skips
  // when the snapshot identity is unchanged (buildBackdrop clears before
  // drawing, so a rebuild never stacks or leaks).
  const rebuildBackdrop = useCallback(() => {
    const st = stateRef.current;
    if (st.app?.renderer == null || st.layers == null || st.data == null) return;
    if (st.backdropFor === st.data) return;
    st.backdropFor = st.data;
    buildBackdrop(st.layers.backdrop, st.data, st.app.renderer);
  }, []);

  // Same identity-guard seam for the GALAXY band (auras + currents + labels).
  const rebuildGalaxyBand = useCallback(() => {
    const st = stateRef.current;
    if (st.app?.renderer == null || st.layers == null || st.data == null) return;
    if (st.galaxyBandFor === st.data) return;
    st.galaxyBandFor = st.data;
    st.galaxyBand = buildGalaxyBand(st.layers, st.data, st.app.renderer, hooksRef.current ?? undefined);
  }, []);

  // ...and for the REGION band (orbs + dormant threads + region labels).
  const rebuildOrbs = useCallback(() => {
    const st = stateRef.current;
    if (st.app?.renderer == null || st.layers == null || st.data == null) return;
    if (st.orbsFor === st.data) return;
    st.orbsFor = st.data;
    st.orbs = buildOrbs(st.layers, st.data, st.app.renderer, hooksRef.current ?? undefined);
  }, []);

  // ...and for the lane streams + ship motes. The previous handle is threaded
  // through so ship velocities dead-reckon from the last two snapshots.
  const rebuildStreams = useCallback(() => {
    const st = stateRef.current;
    if (st.app?.renderer == null || st.layers == null || st.data == null) return;
    if (st.streamsFor === st.data) return;
    st.streamsFor = st.data;
    st.streams = buildStreams(st.layers, st.data, st.app.renderer, st.streams ?? undefined);
  }, []);

  // ...and for the SYSTEM band (the in-place drilldown). Rebuilds when either
  // the fetched detail or the SceneData snapshot changes identity (resident
  // ships move per poll); dismantles its containers whenever focus and detail
  // fall out of agreement (unfocused, or a stale system's payload).
  const rebuildSystemBand = useCallback(() => {
    const st = stateRef.current;
    if (st.app?.renderer == null || st.layers == null) return;
    const d = detailRef.current;
    if (st.focus == null || d == null || d.symbol !== st.focus || st.data == null) {
      // An unfocused band still fading out keeps its containers — the ticker
      // reaps them once systemFade drops below FADE_EPS (no wheel-out pop).
      if (st.focus == null && st.systemFade > FADE_EPS) return;
      if (st.systemBandFor != null || st.systemBand != null) {
        clearSystemBand(st.layers);
        st.systemBandFor = null;
        st.systemBandDataFor = null;
        st.systemBand = null;
      }
      return;
    }
    if (st.systemBandFor === d && st.systemBandDataFor === st.data) return;
    st.systemBandFor = d;
    st.systemBandDataFor = st.data;
    st.systemBand = buildSystemBand(st.layers, d, st.focus, st.app.renderer, st.data);
  }, []);

  // Detail arrivals (and focus changes routed through React state) rebuild the
  // band; the data effect below covers per-poll ship movement.
  useEffect(() => {
    rebuildSystemBand();
  }, [detail, rebuildSystemBand]);

  // Latest interaction callbacks — the interaction tasks read these from the
  // ticker/pointer paths without re-running the mount effect.
  const callbacksRef = useRef({ onSelectSystem, onHover });
  callbacksRef.current = { onSelectSystem, onHover };

  // Stable NebulaApi, created once (closes over stateRef only, which is stable).
  const apiHolder = useRef<NebulaApi | null>(null);
  if (apiHolder.current == null) {
    const flyTo = (to: CamXform) => {
      const st = stateRef.current;
      st.follow = null; // any commanded flight takes the camera back from a tour follow
      st.flight = { from: { ...st.cam }, to, startMs: performance.now() };
    };
    apiHolder.current = {
      fitGalaxy() {
        const st = stateRef.current;
        const d = st.data;
        if (d == null || d.fitPoints.length === 0) return;
        let to = fitTransform(worldBounds(d.fitPoints, FIT_PAD), st.viewport.w, st.viewport.h);
        st.fitScale = to.scale; // TRUE fit scale — band() must read z = zoom multiple
        // Dev-only band override: zoom the fit about the viewport center
        // (x = vw/2 - cx·s ⇒ scaling s by m keeps the center iff the offset
        // from vw/2 scales by m too).
        const m = devBandZoom();
        if (m !== 1) {
          to = {
            x: st.viewport.w / 2 + (to.x - st.viewport.w / 2) * m,
            y: st.viewport.h / 2 + (to.y - st.viewport.h / 2) * m,
            scale: to.scale * m,
          };
        }
        flyTo(to);
      },
      focusSystem(symbol: string) {
        const st = stateRef.current;
        const sys = st.data?.systems.find((s) => s.symbol === symbol);
        if (sys == null || st.fitScale <= 0) {
          // No data or no fit yet — retry when the data effect can satisfy it.
          st.pendingFocus = { kind: 'system', key: symbol };
          return;
        }
        const s = st.fitScale * SYSTEM_FOCUS_ZOOM;
        flyTo({
          x: st.viewport.w / 2 - sys.x * s,
          y: st.viewport.h / 2 - sys.y * s,
          scale: s,
        });
        st.focus = symbol;
        setFocusedSymbol(symbol); // triggers the detail fetch
        callbacksRef.current.onSelectSystem(symbol);
      },
      focusTour(flowId: string) {
        // Tween-free camera follow: the ticker centers this flow's ship each
        // frame at the current zoom until the user wheels/drags (break-on-
        // input) or the flow leaves the data. No data yet → the follow simply
        // waits; the loop only self-clears against a live snapshot.
        const st = stateRef.current;
        st.follow = flowId;
        st.flight = null;
      },
      band() {
        const st = stateRef.current;
        st.band = bandFor(st.cam.scale, st.fitScale, st.band);
        return st.band;
      },
    };
  }

  useEffect(() => {
    apiRef.current = apiHolder.current;
    return () => {
      apiRef.current = null;
    };
  }, [apiRef]);

  // A system-focus request that predated data/fit (api call or the dev
  // `?band=system` landing) flies as soon as both exist. focusSystem re-queues
  // only when the system is missing or the fit is absent — both checked here,
  // so consumption never loops.
  const consumePendingFocus = useCallback(() => {
    const st = stateRef.current;
    const pf = st.pendingFocus;
    if (pf == null) return;
    if (st.fitScale <= 0 || st.data?.systems.some((s) => s.symbol === pf.key) !== true) return;
    st.pendingFocus = null;
    apiHolder.current?.focusSystem(pf.key);
  }, []);

  // Pixi lifecycle: init once, append canvas, scaffold layers, run the camera
  // loop; tear everything down on unmount. All pixi calls are guarded behind
  // a live renderer so a failed/absent WebGL context can never crash React.
  useEffect(() => {
    const host = hostRef.current;
    if (host == null) return;
    const st = stateRef.current;

    const measure = () => {
      st.viewport = {
        w: Math.max(1, host.clientWidth),
        h: Math.max(1, host.clientHeight),
      };
      const app = st.app;
      if (app != null && app.renderer != null) {
        app.renderer.resize(st.viewport.w, st.viewport.h);
      }
    };
    measure();

    let lastTickMs = performance.now();
    // `?fps=1` meter: EMA over ~FPS_EMA_FRAMES frames, seeded by the first sample.
    const fpsMeter = { ema: 0, lastTextMs: 0 };
    const tick = () => {
      const app = st.app;
      if (app == null || app.renderer == null || st.layers == null) return;
      const nowMs = performance.now();
      const rawDtMs = Math.max(0, nowMs - lastTickMs);
      const dtMs = Math.min(MAX_DT_MS, rawDtMs);
      lastTickMs = nowMs;
      // FPS overlay: averages the RAW frame delta (the sim clamp above would
      // flatter a stall) and touches the DOM text at ~2 Hz, never per frame.
      const fpsEl = fpsRef.current;
      if (fpsEl != null && rawDtMs > 0) {
        const inst = 1000 / rawDtMs;
        fpsMeter.ema = fpsMeter.ema <= 0 ? inst : fpsMeter.ema + (inst - fpsMeter.ema) / FPS_EMA_FRAMES;
        if (nowMs - fpsMeter.lastTextMs >= FPS_TEXT_MS) {
          fpsMeter.lastTextMs = nowMs;
          fpsEl.textContent = `${Math.round(fpsMeter.ema)} fps`;
        }
      }
      if (st.flight != null) {
        const t = (nowMs - st.flight.startMs) / FLIGHT_MS;
        if (t >= 1) {
          st.cam = st.flight.to;
          st.flight = null;
        } else {
          st.cam = lerpCam(st.flight.from, st.flight.to, t);
        }
      }
      // Tour follow: center the tracked flow's ship at the current zoom, every
      // tick, tween-free. Self-clears only against a live snapshot missing the
      // flow (pre-data it just waits); wheel/drag/flights clear it at input.
      if (st.follow != null) {
        const ship = st.data?.ships.find((s) => s.flowId === st.follow);
        if (ship != null) {
          st.cam = {
            x: st.viewport.w / 2 - ship.x * st.cam.scale,
            y: st.viewport.h / 2 - ship.y * st.cam.scale,
            scale: st.cam.scale,
          };
        } else if (st.data != null) {
          st.follow = null; // the flow ended — hold position, stop tracking
        }
      }
      const world = st.layers.world;
      world.position.set(st.cam.x, st.cam.y);
      world.scale.set(st.cam.scale);
      st.band = bandFor(st.cam.scale, st.fitScale, st.band);

      // Wheel-out (or any camera move) past SYSTEM_EXIT while focused clears
      // the focus and restores the dimmer. Guarded on no in-progress flight so
      // the outbound leg of the focus flight itself (which starts below the
      // SYSTEM band) can never cancel the focus it is delivering. The fx
      // containers are NOT torn down here — they ride the systemFade tail and
      // are reaped below once invisible, so the exit fades instead of popping.
      if (st.focus != null && st.flight == null && st.band !== 'SYSTEM') {
        st.focus = null;
        setFocusedSymbol(null);
        callbacksRef.current.onSelectSystem(null);
      }

      // Band visibility cross-fades: each band's containers ease toward 1 only
      // while its band is current (bound to the band value, never raw zoom),
      // and a fully faded-out container is culled (visible=false) so it costs
      // no render work. Particles advance only while actually visible. The
      // page's layer toggles AND the band fade both gate a container — either
      // off hides it.
      const step = dtMs / BAND_FADE_MS;
      const toggles = togglesRef.current;

      // Scene-level dimmer: while a system is focused everything except the
      // SYSTEM band's own content (fx + system labels) eases to 25% alpha —
      // labels included, so the drilldown owns the eye.
      const dimTarget = st.focus != null ? DIM_ALPHA : 1;
      st.dim += Math.max(-step, Math.min(step, dimTarget - st.dim));
      st.layers.backdrop.alpha = st.dim;

      const galaxyTarget = st.band === 'GALAXY' ? 1 : 0;
      st.galaxyFade += Math.max(-step, Math.min(step, galaxyTarget - st.galaxyFade));
      const galaxyOn = st.galaxyFade > FADE_EPS;
      st.layers.auras.alpha = st.galaxyFade * st.dim;
      st.layers.auras.visible = galaxyOn;
      // Currents (and their +k/hr labels — that is all the galaxy-band label
      // box holds) are the Paths toggle's target.
      st.layers.currents.alpha = st.galaxyFade * st.dim;
      st.layers.currents.visible = galaxyOn && toggles.paths;
      if (st.galaxyBand != null) {
        st.galaxyBand.labels.alpha = st.galaxyFade * st.dim;
        st.galaxyBand.labels.visible = galaxyOn && toggles.paths;
        // Cluster freshness rings ride the galaxy fade with the auras they sit
        // in; the Freshness toggle is the only thing that hides them separately.
        st.galaxyBand.freshness.visible = toggles.freshness;
        if (galaxyOn) st.galaxyBand.update(dtMs);
      }

      // REGION-band visibility: orbs (with their thread underlay) + region
      // labels show at REGION and stay up through SYSTEM, dimmed with the rest
      // of the scene while a system is focused.
      const regionTarget = st.band === 'REGION' || st.band === 'SYSTEM' ? 1 : 0;
      st.regionFade += Math.max(-step, Math.min(step, regionTarget - st.regionFade));
      const regionOn = st.regionFade > FADE_EPS;
      st.layers.orbs.alpha = st.regionFade * st.dim;
      st.layers.orbs.visible = regionOn;
      if (st.orbs != null) {
        st.orbs.labels.alpha = st.regionFade * st.dim;
        st.orbs.labels.visible = regionOn;
        // Lattice density (sp-fw6a2): the near tier — threads touching the
        // traded neighbourhood — rides the REGION band unconditionally, so the
        // context that frames the trade lanes is always there. The far tier is
        // the other ~95% of a 5.2k-edge gate graph; at REGION it is an opaque
        // mat, so it is revealed only once the camera reaches SYSTEM (where the
        // viewport covers one system) or the Lattice toggle asks for the whole
        // web. Both tiers ride layers.orbs above, so GALAXY still shows neither.
        st.orbs.farThreads.visible = st.band === 'SYSTEM' || toggles.lattice;
      }
      // Per-system freshness marks. They are orbs.ts content but they live in
      // their OWN layer, under the lanes (sp-9m0bd), so the REGION fade has to
      // be driven here rather than inherited from layers.orbs. The Freshness
      // toggle hides them independently, as before.
      st.layers.freshness.alpha = st.regionFade * st.dim;
      st.layers.freshness.visible = regionOn && toggles.freshness;
      // Lane streams + ship motes ride the same REGION fade; particles and
      // dead-reckoned ships advance only while actually visible. These two are
      // ADDITIVE particle layers: dense chains sum back to white through any
      // single-sprite alpha, so the focus dimmer is squared here (1 when not
      // focused) to keep their perceived brightness near the 25% the flat
      // layers get.
      const motionDim = st.dim * st.dim;
      st.layers.lanes.alpha = st.regionFade * motionDim;
      st.layers.lanes.visible = regionOn && toggles.lanes;
      st.layers.ships.alpha = st.regionFade * motionDim;
      st.layers.ships.visible = regionOn && toggles.ships;
      if (st.streams != null && regionOn) st.streams.update(dtMs);

      // SYSTEM-band visibility: the fx drilldown + system labels show only at
      // SYSTEM (never dimmed — they are the focused content).
      const systemTarget = st.band === 'SYSTEM' ? 1 : 0;
      st.systemFade += Math.max(-step, Math.min(step, systemTarget - st.systemFade));
      const systemOn = st.systemFade > FADE_EPS;
      st.layers.fx.alpha = st.systemFade;
      st.layers.fx.visible = systemOn;
      // Freshness toggle: gates the SYSTEM band's market-freshness rings (the
      // one freshness visual in this scene) without touching the rest of the
      // drilldown. Direct child of fx by label — cheap per-tick lookup.
      const freshRings = st.layers.fx.children.find((c) => c.label === 'system-freshness');
      if (freshRings != null) freshRings.visible = toggles.freshness;
      if (st.systemBand != null) {
        st.systemBand.labels.alpha = st.systemFade;
        st.systemBand.labels.visible = systemOn;
      }
      // Deferred unfocus teardown: once the fade tail has fully hidden an
      // unfocused SYSTEM band, dismantle its containers (rebuildSystemBand
      // holds off while systemFade is still live for exactly this hand-off).
      if (st.focus == null && !systemOn && (st.systemBand != null || st.systemBandFor != null)) {
        clearSystemBand(st.layers);
        st.systemBandFor = null;
        st.systemBandDataFor = null;
        st.systemBand = null;
      }
    };

    let cancelled = false;
    let observer: ResizeObserver | null = null;
    let unlisten: (() => void) | null = null;
    const app = new Application();

    (async () => {
      try {
        await app.init({
          background: BACKDROP_COLOR,
          antialias: true,
          resolution: Math.min(window.devicePixelRatio || 1, 2),
          // The canvas's CSS size must equal the renderer's LOGICAL size: with
          // pixi's default (false) a DPR-2 display lays the canvas out at 2×
          // the host (scene renders 2×-clipped) and wheel anchors land at 2×
          // the cursor offset ("doesn't center on the cursor" report).
          autoDensity: true,
        });
      } catch {
        if (!cancelled) setFailed(true);
        return;
      }
      if (cancelled) {
        // Unmounted (or StrictMode remounted) while init was in flight.
        app.destroy(true);
        return;
      }
      st.app = app;
      host.appendChild(app.canvas);
      st.layers = createLayers(app.stage);
      measure();
      app.ticker.add(tick);
      if (typeof ResizeObserver !== 'undefined') {
        observer = new ResizeObserver(measure);
        observer.observe(host);
      }

      // ---- Input (Task 12): anchored wheel zoom, drag pan, F/Escape keys.
      // Down lands on the canvas; move/up ride the window so a drag that
      // leaves the canvas keeps panning and always releases.
      const canvas = app.canvas;
      const onPointerDown = (e: PointerEvent) => {
        st.drag = {
          pointerId: typeof e.pointerId === 'number' ? e.pointerId : null,
          startX: e.clientX,
          startY: e.clientY,
          lastX: e.clientX,
          lastY: e.clientY,
        };
        st.dragMoved = false;
      };
      const samePointer = (e: PointerEvent) =>
        st.drag != null &&
        (st.drag.pointerId == null || typeof e.pointerId !== 'number' || e.pointerId === st.drag.pointerId);
      const onPointerMove = (e: PointerEvent) => {
        const drag = st.drag;
        if (drag == null || !samePointer(e)) return;
        if (!st.dragMoved) {
          // Below the threshold the press is still a prospective tap-select.
          if (Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) <= DRAG_THRESHOLD_PX) return;
          st.dragMoved = true;
          st.flight = null;
          st.follow = null;
        }
        st.cam = {
          x: st.cam.x + (e.clientX - drag.lastX),
          y: st.cam.y + (e.clientY - drag.lastY),
          scale: st.cam.scale,
        };
        drag.lastX = e.clientX;
        drag.lastY = e.clientY;
      };
      const onPointerUp = (e: PointerEvent) => {
        // dragMoved deliberately survives until the next down: pixi delivers
        // the pointertap during this same native event, in either order.
        if (samePointer(e)) st.drag = null;
      };
      const onWheel = (e: WheelEvent) => {
        e.preventDefault();
        if (e.deltaY === 0) return; // horizontal trackpad swipe — not a zoom (and not a follow-break)
        if (st.fitScale <= 0) return; // the zoom clamp is meaningless before the first fit
        st.flight = null;
        st.follow = null;
        const rect = canvas.getBoundingClientRect();
        // Delta-proportional factor: deltaMode 0 is pixels (trackpads, Chrome
        // wheels), 1 is lines (Firefox wheels), 2 is pages — normalize to px.
        const dy =
          e.deltaMode === 1 ? e.deltaY * WHEEL_LINE_PX :
          e.deltaMode === 2 ? e.deltaY * st.viewport.h :
          e.deltaY;
        const factor = Math.exp(-dy * WHEEL_SENSITIVITY);
        // Canvas-local CSS px → renderer logical px (anchoredZoom's space).
        // The two diverge whenever the canvas's CSS size differs from its
        // logical size (the DPR>1 anchor skew; autoDensity now pins them
        // equal — the ratio keeps the anchor exact under any styling).
        const sx = rect.width > 0 ? st.viewport.w / rect.width : 1;
        const sy = rect.height > 0 ? st.viewport.h / rect.height : 1;
        st.cam = anchoredZoom(st.cam, (e.clientX - rect.left) * sx, (e.clientY - rect.top) * sy, factor, st.fitScale);
      };
      const isTyping = (t: EventTarget | null): boolean =>
        t instanceof HTMLElement &&
        (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable);
      const onKeyDown = (e: KeyboardEvent) => {
        // Browser chords (Cmd+F / Ctrl+F find-in-page, Alt combos) are not ours.
        if (e.metaKey || e.ctrlKey || e.altKey) return;
        // A held key must not restart the tween on every OS auto-repeat.
        if (e.repeat) return;
        if (isTyping(e.target)) return;
        if (e.key === 'f' || e.key === 'F') {
          apiHolder.current?.fitGalaxy();
        } else if (e.key === 'Escape') {
          // Clear the focus first (dimmer + detail teardown; the fx band rides
          // the fade tail), then fly home.
          if (st.focus != null) {
            st.focus = null;
            setFocusedSymbol(null);
            callbacksRef.current.onSelectSystem(null);
          }
          apiHolder.current?.fitGalaxy();
        }
      };
      canvas.addEventListener('pointerdown', onPointerDown);
      window.addEventListener('pointermove', onPointerMove);
      window.addEventListener('pointerup', onPointerUp);
      window.addEventListener('pointercancel', onPointerUp);
      canvas.addEventListener('wheel', onWheel, { passive: false });
      window.addEventListener('keydown', onKeyDown);
      unlisten = () => {
        canvas.removeEventListener('pointerdown', onPointerDown);
        window.removeEventListener('pointermove', onPointerMove);
        window.removeEventListener('pointerup', onPointerUp);
        window.removeEventListener('pointercancel', onPointerUp);
        canvas.removeEventListener('wheel', onWheel);
        window.removeEventListener('keydown', onKeyDown);
      };
      // Data may have landed before the renderer came up — take the landing
      // fit now (fitGalaxy is idempotent state, tick applies it next frame)
      // and build the backdrop from that early snapshot.
      if (!st.didInitialFit && st.data != null && st.data.fitPoints.length > 0) {
        st.didInitialFit = true;
        apiHolder.current?.fitGalaxy();
        // Dev-only (`?band=system`): land already drilled into a system —
        // the focus param, else home, else the first system.
        if (devBandZoom() === SYSTEM_FOCUS_ZOOM && st.pendingFocus == null) {
          const target = devFocusParam() ?? st.data.homeSystem ?? st.data.systems[0]?.symbol ?? null;
          if (target != null) st.pendingFocus = { kind: 'system', key: target };
        }
      }
      consumePendingFocus();
      rebuildBackdrop();
      rebuildGalaxyBand();
      rebuildOrbs();
      rebuildStreams();
      rebuildSystemBand();
    })();

    return () => {
      cancelled = true;
      observer?.disconnect();
      observer = null;
      unlisten?.();
      unlisten = null;
      if (st.app != null) {
        st.app.ticker.remove(tick);
        st.app.destroy(true);
        st.app = null;
        st.layers = null;
        // Destroying the app took every band's containers with it; drop the
        // handles and their identity guards so a remount rebuilds into fresh
        // layers instead of skipping on a stale snapshot identity.
        st.backdropFor = null;
        st.galaxyBand = null;
        st.galaxyBandFor = null;
        st.orbs = null;
        st.orbsFor = null;
        st.streams = null;
        st.streamsFor = null;
        st.systemBand = null;
        st.systemBandFor = null;
        st.systemBandDataFor = null;
      }
    };
    // Mount-once: everything dynamic flows through refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Snapshot the latest data for the layer tasks; first arrival with fit
  // points triggers the landing fit exactly once. Every new snapshot identity
  // rebuilds the backdrop (no-op until the renderer is up — the init path
  // covers that ordering).
  useEffect(() => {
    const st = stateRef.current;
    st.data = data;
    if (!st.didInitialFit && data != null && data.fitPoints.length > 0) {
      st.didInitialFit = true;
      apiHolder.current?.fitGalaxy();
      // Dev-only (`?band=system`): land already drilled into a system.
      if (devBandZoom() === SYSTEM_FOCUS_ZOOM && st.pendingFocus == null) {
        const target = devFocusParam() ?? data.homeSystem ?? data.systems[0]?.symbol ?? null;
        if (target != null) st.pendingFocus = { kind: 'system', key: target };
      }
    }
    consumePendingFocus();
    rebuildBackdrop();
    rebuildGalaxyBand();
    rebuildOrbs();
    rebuildStreams();
    rebuildSystemBand();
  }, [data, consumePendingFocus, rebuildBackdrop, rebuildGalaxyBand, rebuildOrbs, rebuildStreams, rebuildSystemBand]);

  if (failed) {
    return <div className="nebula-fallback">WebGL unavailable — panels remain live.</div>;
  }
  return (
    <div
      ref={hostRef}
      className="nebula-host"
      data-testid="nebula-host"
      style={{ position: 'relative', width: '100%', height: '100%', overflow: 'hidden' }}
    >
      {fpsEnabled && (
        <div
          ref={fpsRef}
          className="nebula-fps"
          data-testid="nebula-fps"
          aria-hidden="true"
          style={{
            position: 'absolute',
            top: 8,
            left: 8,
            zIndex: 10,
            padding: '2px 6px',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            fontSize: 11,
            lineHeight: '14px',
            color: '#8fe3ff',
            background: 'rgba(7, 3, 18, 0.6)',
            borderRadius: 3,
            pointerEvents: 'none',
            userSelect: 'none',
          }}
        >
          — fps
        </div>
      )}
    </div>
  );
}
