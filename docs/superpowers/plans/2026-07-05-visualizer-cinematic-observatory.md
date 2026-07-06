# Visualizer Cinematic Observatory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `visualizer/` into a 24/7 Deep Space Noir cinematic observatory: gliding ships with trails and engine glow, a living camera, event theater, glass HUD, and always-on resilience.

**Architecture:** Layered compositing — a DOM ambient backdrop canvas *behind* the existing Konva stage, the Konva scene upgraded in place (camera tweens, trail fade, FX layer), and a DOM glass HUD *in front*. Existing Zustand store + polling remain the data source; two small endpoints are added to the Express server (`server/routes/bot.ts`) which already has Postgres access.

**Tech Stack:** React 18, react-konva/Konva 10, Zustand 4, Tailwind 3, vitest + @testing-library/react, Express + pg (server).

**Spec:** `docs/superpowers/specs/2026-07-05-visualizer-cinematic-observatory-design.md`

## Global Constraints

- **Never modify `gobot/**`** — a 13-partition refactor is in flight there. All server-side needs go in `visualizer/server/`.
- Waypoints have **fixed positions** — no orbital motion of waypoints, ever.
- All frontend work in `visualizer/web/`, server work in `visualizer/server/`.
- Run web commands from `visualizer/web/` (`npx vitest run <path>`), server build from `visualizer/server/` (`npm run build`).
- The fleet may be stopped: daemon at `:4000` and Postgres may be down. Every task is testable via vitest and/or demo mode; steps marked **[live-only]** are skipped (not faked) when infra is down and listed in the final report.
- Noir palette lives ONLY in `web/src/theme/noir.ts` after Task 1 — no new hex literals in components.
- All buffers bounded; every `requestAnimationFrame`/interval must pause on `document.hidden`.
- Follow existing code style; comment only where existing files do.
- Commit after each task with the exact `git add` paths given (the repo has unrelated dirty files — never `git add .` / `-A`).

---

### Task 1: Noir theme tokens

**Files:**
- Create: `visualizer/web/src/theme/noir.ts`
- Test: `visualizer/web/src/theme/__tests__/noir.test.ts`

**Interfaces:**
- Produces: `NOIR` const object and `noirAlpha(hex: string, alpha: number): string` used by every later task.

- [ ] **Step 1: Write the failing test**

```ts
// visualizer/web/src/theme/__tests__/noir.test.ts
import { describe, it, expect } from 'vitest';
import { NOIR, noirAlpha } from '../noir';

describe('noir theme', () => {
  it('exposes the core palette', () => {
    expect(NOIR.bg0).toBe('#04060D');
    expect(NOIR.accent).toBe('#7DB1FF');
    expect(NOIR.star).toBe('#F5E9C8');
  });
  it('converts hex + alpha to rgba', () => {
    expect(noirAlpha('#7DB1FF', 0.5)).toBe('rgba(125, 177, 255, 0.5)');
  });
});
```

- [ ] **Step 2: Run it — expect FAIL (module not found)**

Run from `visualizer/web/`: `npx vitest run src/theme/__tests__/noir.test.ts`

- [ ] **Step 3: Implement**

```ts
// visualizer/web/src/theme/noir.ts
export const NOIR = {
  bg0: '#04060D',
  bg1: '#0A0F1E',
  nebula: '#16223F',
  panel: '#0D1220',
  ink: '#EAEEF6',
  muted: '#8B95AB',
  dim: '#5A6478',
  accent: '#7DB1FF',
  accentSoft: '#9CC5FF',
  good: '#3DD68C',
  warn: '#F5C518',
  bad: '#FF6369',
  star: '#F5E9C8',
} as const;

export function noirAlpha(hex: string, alpha: number): string {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}
```

- [ ] **Step 4: Run test — expect PASS**
- [ ] **Step 5: Commit**

```bash
git add visualizer/web/src/theme/noir.ts visualizer/web/src/theme/__tests__/noir.test.ts
git commit -m "feat(viz): noir theme tokens"
```

---

### Task 2: Ambient backdrop (starfield + nebula, parallax)

**Files:**
- Create: `visualizer/web/src/components/AmbientBackdrop.tsx`
- Create: `visualizer/web/src/domain/backdrop.ts`
- Test: `visualizer/web/src/domain/__tests__/backdrop.test.ts`
- Modify: `visualizer/web/src/components/SpaceMap.tsx` (mount backdrop behind Stage; make Stage background transparent)

**Interfaces:**
- Consumes: `NOIR` from Task 1; `viewportBounds` state that already exists in `SpaceMap.tsx` (`{ x, y, width, height, scale }`).
- Produces: `<AmbientBackdrop viewport={{x,y,scale}} />`; pure `starfield(seed: number, count: number, w: number, h: number): Array<{x:number;y:number;r:number;a:number}>` and `parallaxOffset(view: {x:number;y:number}, factor: number): {x:number;y:number}`.

- [ ] **Step 1: Write failing tests for the pure parts**

```ts
// visualizer/web/src/domain/__tests__/backdrop.test.ts
import { describe, it, expect } from 'vitest';
import { starfield, parallaxOffset } from '../backdrop';

describe('backdrop', () => {
  it('starfield is deterministic for a seed and stays in bounds', () => {
    const a = starfield(42, 200, 1000, 800);
    const b = starfield(42, 200, 1000, 800);
    expect(a).toEqual(b);
    expect(a).toHaveLength(200);
    for (const s of a) {
      expect(s.x).toBeGreaterThanOrEqual(0);
      expect(s.x).toBeLessThanOrEqual(1000);
      expect(s.r).toBeGreaterThan(0);
      expect(s.a).toBeGreaterThan(0);
      expect(s.a).toBeLessThanOrEqual(1);
    }
  });
  it('parallax moves opposite and scaled', () => {
    expect(parallaxOffset({ x: 100, y: -50 }, 0.1)).toEqual({ x: -10, y: 5 });
  });
});
```

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement domain**

```ts
// visualizer/web/src/domain/backdrop.ts
export interface BackdropStar { x: number; y: number; r: number; a: number }

// Mulberry32 — tiny deterministic PRNG so the field never "re-rolls" between renders.
function mulberry32(seed: number) {
  let t = seed >>> 0;
  return () => {
    t += 0x6d2b79f5;
    let r = Math.imul(t ^ (t >>> 15), 1 | t);
    r ^= r + Math.imul(r ^ (r >>> 7), 61 | r);
    return ((r ^ (r >>> 14)) >>> 0) / 4294967296;
  };
}

export function starfield(seed: number, count: number, w: number, h: number): BackdropStar[] {
  const rnd = mulberry32(seed);
  return Array.from({ length: count }, () => ({
    x: rnd() * w,
    y: rnd() * h,
    r: 0.4 + rnd() * 1.1,
    a: 0.25 + rnd() * 0.75,
  }));
}

export function parallaxOffset(view: { x: number; y: number }, factor: number): { x: number; y: number } {
  return { x: -view.x * factor, y: -view.y * factor };
}
```

- [ ] **Step 4: Run — expect PASS**
- [ ] **Step 5: Implement the component**

Paints ONCE to two stacked canvases (far stars + nebula, near stars), then only CSS-transforms them. Nebula = 3 large radial gradients in `NOIR.nebula`/`bg1` at low alpha. Twinkle = a 6s CSS opacity keyframe on the near layer only (subtle; honors `prefers-reduced-motion`).

```tsx
// visualizer/web/src/components/AmbientBackdrop.tsx
import { useEffect, useRef } from 'react';
import { NOIR, noirAlpha } from '../theme/noir';
import { starfield, parallaxOffset } from '../domain/backdrop';

interface Props { viewport: { x: number; y: number; scale: number } }

const FAR = 0.03;
const NEAR = 0.08;

function paint(canvas: HTMLCanvasElement, seed: number, count: number, nebula: boolean) {
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  const { width: w, height: h } = canvas;
  ctx.clearRect(0, 0, w, h);
  if (nebula) {
    const blobs: Array<[number, number, number, string]> = [
      [w * 0.72, h * 0.12, w * 0.5, noirAlpha(NOIR.nebula, 0.55)],
      [w * 0.15, h * 0.85, w * 0.45, noirAlpha(NOIR.bg1, 0.8)],
      [w * 0.45, h * 0.5, w * 0.7, noirAlpha(NOIR.nebula, 0.25)],
    ];
    for (const [x, y, r, color] of blobs) {
      const g = ctx.createRadialGradient(x, y, 0, x, y, r);
      g.addColorStop(0, color);
      g.addColorStop(1, 'rgba(0,0,0,0)');
      ctx.fillStyle = g;
      ctx.fillRect(0, 0, w, h);
    }
  }
  ctx.fillStyle = '#FFFFFF';
  for (const s of starfield(seed, count, w, h)) {
    ctx.globalAlpha = s.a;
    ctx.beginPath();
    ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.globalAlpha = 1;
}

export function AmbientBackdrop({ viewport }: Props) {
  const farRef = useRef<HTMLCanvasElement>(null);
  const nearRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const resize = () => {
      for (const [ref, seed, count, nebula] of [
        [farRef, 7, 220, true],
        [nearRef, 13, 90, false],
      ] as const) {
        const c = ref.current;
        if (!c) continue;
        c.width = window.innerWidth * 1.3;
        c.height = window.innerHeight * 1.3;
        paint(c, seed, count, nebula);
      }
    };
    resize();
    window.addEventListener('resize', resize);
    return () => window.removeEventListener('resize', resize);
  }, []);

  const far = parallaxOffset(viewport, FAR);
  const near = parallaxOffset(viewport, NEAR);
  const style = (o: { x: number; y: number }): React.CSSProperties => ({
    position: 'absolute',
    inset: '-15%',
    transform: `translate3d(${o.x}px, ${o.y}px, 0)`,
    willChange: 'transform',
    pointerEvents: 'none',
  });

  return (
    <div style={{ position: 'absolute', inset: 0, overflow: 'hidden', background: NOIR.bg0 }} aria-hidden>
      <canvas ref={farRef} style={style(far)} />
      <canvas ref={nearRef} className="viz-twinkle" style={style(near)} />
    </div>
  );
}
```

Add to `visualizer/web/src/index.css`:

```css
@keyframes viz-twinkle { 0%, 100% { opacity: 1; } 50% { opacity: 0.82; } }
.viz-twinkle { animation: viz-twinkle 6s ease-in-out infinite; }
@media (prefers-reduced-motion: reduce) { .viz-twinkle { animation: none; } }
```

- [ ] **Step 6: Mount behind the Stage in `SpaceMap.tsx`**

Locate the `return (` of the SpaceMap component (the JSX containing `<Stage`). Wrap so the backdrop is the first absolutely-positioned child of the map container div and pass the existing viewport state:

```tsx
<AmbientBackdrop viewport={{ x: viewportBounds.x, y: viewportBounds.y, scale: viewportBounds.scale }} />
```

Then find where the stage/container background color is set (search `background` in `SpaceMap.tsx` and `MapView.tsx`) and make the Stage container transparent so the backdrop shows through. Delete the now-redundant `backgroundPosition` state and its usages **only if** it was solely feeding the old background (verify with a grep for `backgroundPosition` first; if it feeds anything else, leave it).

- [ ] **Step 7: Verify visually in demo mode**

Run from `visualizer/web/`: `npm run dev`, open the printed URL — the map background must show nebula + stars, drifting subtly against pan/zoom. (Demo/mock data path: see Task 11 — at this point an empty map over the backdrop is fine.)

- [ ] **Step 8: Run full web test suite — expect no regressions**

`npx vitest run`

- [ ] **Step 9: Commit**

```bash
git add visualizer/web/src/components/AmbientBackdrop.tsx visualizer/web/src/domain/backdrop.ts visualizer/web/src/domain/__tests__/backdrop.test.ts visualizer/web/src/index.css visualizer/web/src/components/SpaceMap.tsx
git commit -m "feat(viz): ambient noir backdrop with parallax"
```

---

### Task 3: Camera math (pure domain)

**Files:**
- Create: `visualizer/web/src/domain/camera.ts`
- Test: `visualizer/web/src/domain/__tests__/camera.test.ts`

**Interfaces:**
- Produces (consumed by Task 4):

```ts
export interface CameraPose { x: number; y: number; scale: number }
export function idleDrift(tMs: number, base: CameraPose): CameraPose
export function easeToward(current: CameraPose, target: CameraPose, dtMs: number, tauMs?: number): CameraPose
export function poseSettled(a: CameraPose, b: CameraPose): boolean
```

- [ ] **Step 1: Write failing tests**

```ts
// visualizer/web/src/domain/__tests__/camera.test.ts
import { describe, it, expect } from 'vitest';
import { idleDrift, easeToward, poseSettled } from '../camera';

const base = { x: 0, y: 0, scale: 1 };

describe('camera math', () => {
  it('idleDrift orbits the base pose within tight bounds and loops smoothly', () => {
    for (const t of [0, 10_000, 60_000, 3_600_000]) {
      const p = idleDrift(t, base);
      expect(Math.abs(p.x)).toBeLessThanOrEqual(40);
      expect(Math.abs(p.y)).toBeLessThanOrEqual(40);
      expect(p.scale).toBeGreaterThanOrEqual(0.97);
      expect(p.scale).toBeLessThanOrEqual(1.08);
    }
  });
  it('easeToward converges exponentially and never overshoots', () => {
    let cur = { x: 0, y: 0, scale: 1 };
    const target = { x: 100, y: -60, scale: 2 };
    for (let i = 0; i < 300; i++) cur = easeToward(cur, target, 16);
    expect(cur.x).toBeCloseTo(100, 0);
    expect(cur.y).toBeCloseTo(-60, 0);
    expect(cur.scale).toBeCloseTo(2, 1);
  });
  it('poseSettled detects arrival', () => {
    expect(poseSettled({ x: 100, y: 50, scale: 2 }, { x: 100.05, y: 49.95, scale: 2.0005 })).toBe(true);
    expect(poseSettled(base, { x: 5, y: 0, scale: 1 })).toBe(false);
  });
});
```

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement**

```ts
// visualizer/web/src/domain/camera.ts
export interface CameraPose { x: number; y: number; scale: number }

const DRIFT_X = 32;
const DRIFT_Y = 22;
const DRIFT_PERIOD_MS = 90_000;
const BREATH_PERIOD_MS = 47_000;
const BREATH_AMP = 0.035;

export function idleDrift(tMs: number, base: CameraPose): CameraPose {
  const a = (tMs / DRIFT_PERIOD_MS) * Math.PI * 2;
  const b = (tMs / BREATH_PERIOD_MS) * Math.PI * 2;
  return {
    x: base.x + Math.sin(a) * DRIFT_X,
    y: base.y + Math.sin(a * 0.7 + 1.3) * DRIFT_Y,
    scale: base.scale * (1 + (Math.sin(b) + 1) * 0.5 * BREATH_AMP),
  };
}

export function easeToward(current: CameraPose, target: CameraPose, dtMs: number, tauMs = 450): CameraPose {
  const k = 1 - Math.exp(-Math.max(0, dtMs) / tauMs);
  return {
    x: current.x + (target.x - current.x) * k,
    y: current.y + (target.y - current.y) * k,
    scale: current.scale + (target.scale - current.scale) * k,
  };
}

export function poseSettled(a: CameraPose, b: CameraPose): boolean {
  return Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5 && Math.abs(a.scale - b.scale) < 0.005;
}
```

- [ ] **Step 4: Run — expect PASS**
- [ ] **Step 5: Commit**

```bash
git add visualizer/web/src/domain/camera.ts visualizer/web/src/domain/__tests__/camera.test.ts
git commit -m "feat(viz): pure camera math (idle drift, easing)"
```

---

### Task 4: Cinematic camera hook wired to the stage

**Files:**
- Create: `visualizer/web/src/hooks/useCinematicCamera.ts`
- Test: `visualizer/web/src/hooks/__tests__/useCinematicCamera.test.ts`
- Modify: `visualizer/web/src/components/SpaceMap.tsx` (instantiate; route wheel/drag through `notifyManual`; apply pose on the existing animation tick)

**Interfaces:**
- Consumes: Task 3 functions; the existing `stageRef` (Konva Stage) and `handleAnimationTick` cycle in `SpaceMap.tsx`.
- Produces:

```ts
interface CinematicCamera {
  mode: 'idle' | 'easing' | 'follow' | 'manual';
  applyFrame(nowMs: number, stage: Konva.Stage): void;   // call once per tick
  easeTo(pose: CameraPose): void;
  follow(getTarget: () => CameraPose | null): void;
  stopFollow(): void;
  notifyManual(): void;   // wheel/drag — wins instantly, 20s grace before idle resumes
}
function useCinematicCamera(opts?: { manualGraceMs?: number }): CinematicCamera
```

- [ ] **Step 1: Write failing tests** (hook logic exercised via `renderHook` from `@testing-library/react`; a stub stage object records `position()`/`scale()` calls)

```ts
// visualizer/web/src/hooks/__tests__/useCinematicCamera.test.ts
import { describe, it, expect, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useCinematicCamera } from '../useCinematicCamera';

function stubStage() {
  const state = { x: 0, y: 0, scale: 1 };
  return {
    state,
    position: vi.fn((p?: { x: number; y: number }) => {
      if (p) { state.x = p.x; state.y = p.y; }
      return { x: state.x, y: state.y };
    }),
    scale: vi.fn((s?: { x: number; y: number }) => {
      if (s) state.scale = s.x;
      return { x: state.scale, y: state.scale };
    }),
    batchDraw: vi.fn(),
  } as any;
}

describe('useCinematicCamera', () => {
  it('starts in idle and drifts the stage', () => {
    const { result } = renderHook(() => useCinematicCamera());
    const stage = stubStage();
    result.current.applyFrame(0, stage);
    result.current.applyFrame(20_000, stage);
    expect(result.current.mode).toBe('idle');
    expect(stage.position).toHaveBeenCalled();
    expect(stage.state.x).not.toBe(0);
  });
  it('easeTo converges then returns to idle', () => {
    const { result } = renderHook(() => useCinematicCamera());
    const stage = stubStage();
    result.current.easeTo({ x: 500, y: 300, scale: 2 });
    for (let t = 0; t < 8_000; t += 16) result.current.applyFrame(t, stage);
    expect(stage.state.x).toBeCloseTo(500, 0);
    expect(result.current.mode).toBe('idle');
  });
  it('manual wins instantly and holds through grace period', () => {
    const { result } = renderHook(() => useCinematicCamera({ manualGraceMs: 1000 }));
    const stage = stubStage();
    result.current.easeTo({ x: 500, y: 300, scale: 2 });
    result.current.applyFrame(0, stage);
    result.current.notifyManual();
    const frozen = { ...stage.state };
    result.current.applyFrame(500, stage);
    expect(result.current.mode).toBe('manual');
    expect(stage.state).toEqual(frozen);   // camera hands off — no writes during manual
    result.current.applyFrame(1600, stage); // grace expired → idle resumes from current pose
    expect(result.current.mode).toBe('idle');
  });
});
```

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement**

```ts
// visualizer/web/src/hooks/useCinematicCamera.ts
import { useRef, useCallback, useState } from 'react';
import type Konva from 'konva';
import { CameraPose, idleDrift, easeToward, poseSettled } from '../domain/camera';

type Mode = 'idle' | 'easing' | 'follow' | 'manual';

export function useCinematicCamera(opts?: { manualGraceMs?: number }) {
  const grace = opts?.manualGraceMs ?? 20_000;
  const [mode, setMode] = useState<Mode>('idle');
  const modeRef = useRef<Mode>('idle');
  const baseRef = useRef<CameraPose>({ x: 0, y: 0, scale: 1 });
  const targetRef = useRef<CameraPose | null>(null);
  const followRef = useRef<(() => CameraPose | null) | null>(null);
  const manualAtRef = useRef<number>(-Infinity);
  const lastMsRef = useRef<number | null>(null);

  const set = (m: Mode) => { modeRef.current = m; setMode(m); };

  const readPose = (stage: Konva.Stage): CameraPose => ({
    x: stage.position().x, y: stage.position().y, scale: stage.scale().x,
  });
  const writePose = (stage: Konva.Stage, p: CameraPose) => {
    stage.position({ x: p.x, y: p.y });
    stage.scale({ x: p.scale, y: p.scale });
    stage.batchDraw();
  };

  const applyFrame = useCallback((nowMs: number, stage: Konva.Stage) => {
    const dt = lastMsRef.current === null ? 16 : nowMs - lastMsRef.current;
    lastMsRef.current = nowMs;

    if (modeRef.current === 'manual') {
      if (nowMs - manualAtRef.current < grace) return;      // user owns the camera
      baseRef.current = readPose(stage);                     // resume from where they left it
      set('idle');
    }
    if (modeRef.current === 'follow' && followRef.current) {
      const t = followRef.current();
      if (t) writePose(stage, easeToward(readPose(stage), t, dt));
      return;
    }
    if (modeRef.current === 'easing' && targetRef.current) {
      const next = easeToward(readPose(stage), targetRef.current, dt);
      writePose(stage, next);
      if (poseSettled(next, targetRef.current)) {
        baseRef.current = targetRef.current;
        targetRef.current = null;
        set('idle');
      }
      return;
    }
    writePose(stage, idleDrift(nowMs, baseRef.current));
  }, [grace]);

  const easeTo = useCallback((pose: CameraPose) => { targetRef.current = pose; set('easing'); }, []);
  const follow = useCallback((get: () => CameraPose | null) => { followRef.current = get; set('follow'); }, []);
  const stopFollow = useCallback(() => { followRef.current = null; set('idle'); }, []);
  const notifyManual = useCallback(() => { manualAtRef.current = performance.now(); set('manual'); }, []);

  return { mode, applyFrame, easeTo, follow, stopFollow, notifyManual };
}
```

- [ ] **Step 4: Run tests + `npx tsc --noEmit` — expect PASS/clean**
- [ ] **Step 5: Wire into `SpaceMap.tsx`**

1. `const camera = useCinematicCamera();`
2. In `handleAnimationTick` (exists at ~line 176), after the frame bump add: `if (stageRef.current) camera.applyFrame(performance.now(), stageRef.current);`
3. Find the stage wheel/drag handlers (search `onWheel`, `draggable`, `dragmove` in `SpaceMap.tsx`) and call `camera.notifyManual()` at the top of each.
4. On ship selection (`onSelectShip` path) call `camera.easeTo({ x, y, scale })` where x/y center the ship using the existing viewport math (see how `shipFocusRequest` centers today — search `shipFocusRequest` usage and reuse that transform).
5. Manual pan/zoom must still work exactly as before while `mode==='manual'`.

- [ ] **Step 6: Verify in dev** — map drifts when idle; wheel interrupts instantly; selecting a ship glides to it; after ~20s hands-off, drift resumes.
- [ ] **Step 7: `npx vitest run` — full suite green**
- [ ] **Step 8: Commit**

```bash
git add visualizer/web/src/hooks/useCinematicCamera.ts visualizer/web/src/hooks/__tests__/useCinematicCamera.test.ts visualizer/web/src/components/SpaceMap.tsx
git commit -m "feat(viz): cinematic camera - idle drift, ease-to-target, manual override"
```

---

### Task 5: Trail fade + bounded buffer

**Files:**
- Modify: `visualizer/web/src/store/useStore.ts` (`addTrailPoint` helper at ~line 43)
- Modify: `visualizer/web/src/components/ShipTrailLayer.tsx`
- Test: `visualizer/web/src/store/__tests__/trails.test.ts` (create)

**Interfaces:**
- Consumes: existing `ShipTrailPoint` type and store actions `addTrailPosition/clearTrail`.
- Produces: trail points carry `t: number` (epoch ms); layer renders age-based alpha; buffer hard-capped.

- [ ] **Step 1: Read the current implementations** of `addTrailPoint` (useStore.ts ~line 43) and `ShipTrailLayer.tsx` + `shipTrailUtils.ts`. Note the existing `ShipTrailPoint` shape and whether it has a timestamp; note current cap if any.
- [ ] **Step 2: Write failing test**

```ts
// visualizer/web/src/store/__tests__/trails.test.ts
import { describe, it, expect } from 'vitest';
import { appendTrailPoint, trailOpacity, TRAIL_MAX_POINTS, TRAIL_FADE_MS } from '../trails';

describe('trail buffer', () => {
  it('caps the buffer at TRAIL_MAX_POINTS', () => {
    let pts: Array<{ x: number; y: number; t: number }> = [];
    for (let i = 0; i < TRAIL_MAX_POINTS + 50; i++) pts = appendTrailPoint(pts, { x: i, y: 0, t: i });
    expect(pts.length).toBe(TRAIL_MAX_POINTS);
    expect(pts[0].x).toBe(50); // oldest dropped
  });
  it('drops points older than the fade window', () => {
    const now = 100_000;
    let pts = [{ x: 0, y: 0, t: now - TRAIL_FADE_MS - 1 }, { x: 1, y: 0, t: now - 10 }];
    pts = appendTrailPoint(pts, { x: 2, y: 0, t: now });
    expect(pts.map(p => p.x)).toEqual([1, 2]);
  });
  it('opacity fades with age, clamped to [0,1]', () => {
    const now = 50_000;
    expect(trailOpacity({ x: 0, y: 0, t: now }, now)).toBeCloseTo(1, 5);
    expect(trailOpacity({ x: 0, y: 0, t: now - TRAIL_FADE_MS }, now)).toBe(0);
    expect(trailOpacity({ x: 0, y: 0, t: now - TRAIL_FADE_MS / 2 }, now)).toBeCloseTo(0.5, 2);
  });
});
```

- [ ] **Step 3: Run — expect FAIL, then implement**

```ts
// visualizer/web/src/store/trails.ts
export const TRAIL_MAX_POINTS = 120;
export const TRAIL_FADE_MS = 60_000;

export interface TimedTrailPoint { x: number; y: number; t: number }

export function appendTrailPoint(points: TimedTrailPoint[], next: TimedTrailPoint): TimedTrailPoint[] {
  const fresh = points.filter(p => next.t - p.t < TRAIL_FADE_MS);
  fresh.push(next);
  return fresh.length > TRAIL_MAX_POINTS ? fresh.slice(fresh.length - TRAIL_MAX_POINTS) : fresh;
}

export function trailOpacity(p: TimedTrailPoint, nowMs: number): number {
  return Math.max(0, Math.min(1, 1 - (nowMs - p.t) / TRAIL_FADE_MS));
}
```

- [ ] **Step 4: Integrate.** In `useStore.ts`, make the internal `addTrailPoint` helper delegate to `appendTrailPoint` stamping `t: Date.now()` (extend `ShipTrailPoint` with `t` if absent — update its type where declared). In `ShipTrailLayer.tsx`, render segments with `stroke={NOIR.accentSoft}` and per-segment `opacity={trailOpacity(point, frameTimestamp)}`, using the `frameTimestamp` prop the layer already receives (if it doesn't, pass it from `ShipLayer`/`SpaceMap` where `frameTimestamp` exists).
- [ ] **Step 5: Run new test + full suite — expect PASS (fix any `ShipTrailPoint` type fallout across usages — grep `ShipTrailPoint`)**
- [ ] **Step 6: Verify in dev**: moving ships leave a tapering, fading wake; nothing accumulates after 60s idle.
- [ ] **Step 7: Commit**

```bash
git add visualizer/web/src/store/trails.ts visualizer/web/src/store/__tests__/trails.test.ts visualizer/web/src/store/useStore.ts visualizer/web/src/components/ShipTrailLayer.tsx
git commit -m "feat(viz): time-faded bounded ship trails"
```

---

### Task 6: Engine glow + Noir sprite restyle

**Files:**
- Modify: `visualizer/web/src/components/ShipSprite.tsx` (engine glow dot, flicker)
- Modify: `visualizer/web/src/components/WaypointSprite.tsx` (rim glow, Noir fills)
- Modify: `visualizer/web/src/components/RouteVectors.tsx` (hairline Noir routes)
- Test: extend existing sprite tests only if they assert colors (grep `#` hex in `web/src/components/__tests__` for affected files and update).

**Interfaces:**
- Consumes: `NOIR`, `noirAlpha` (Task 1); `frameTimestamp` already flowing to sprites via ShipLayer.

- [ ] **Step 1: Read** `ShipSprite.tsx` and `WaypointSprite.tsx` fully. Identify: how ships are drawn (image asset vs shapes), where fill/stroke colors live, what props exist.
- [ ] **Step 2: Engine glow.** In `ShipSprite.tsx`, for ships with `nav.status === 'IN_TRANSIT'`, render behind the hull:

```tsx
<Circle
  x={-hullLength * 0.55}
  y={0}
  radius={2.2}
  fill={NOIR.accentSoft}
  opacity={0.55 + 0.35 * Math.abs(Math.sin(frameTimestamp / 90))}
  shadowColor={NOIR.accent}
  shadowBlur={8}
  shadowOpacity={0.9}
  listening={false}
/>
```

(`hullLength`: reuse the sprite's existing size constant; the glow sits at the stern given the sprite's own rotation group — verify orientation against `calculateShipRotation` usage and flip the x sign if the asset points the other way.)

- [ ] **Step 3: Waypoint rim glow.** In `WaypointSprite.tsx` give planets/gas giants a thin atmosphere ring: a `Circle` with `stroke={noirAlpha(NOIR.accentSoft, 0.5)}`, `strokeWidth={0.8}`, `shadowColor={NOIR.accentSoft}`, `shadowBlur={6}`, `listening={false}` at radius `spriteRadius + 1`. Replace any bright/saturated waypoint fills with Noir equivalents (map existing color constants through `NOIR`; keep type-differentiation by hue but desaturated — e.g. gas giant `#C9A25F`→keep, station `NOIR.muted`, asteroid `NOIR.dim`).
- [ ] **Step 4: Routes + selection.** In `RouteVectors.tsx` set stroke `noirAlpha(NOIR.accent, 0.35)`, width 1, and the moving ship's active route slightly brighter (`0.6`). In `SelectionOverlay.tsx`, restyle the selection indicator strokes/fills to `NOIR.accent` / `noirAlpha(NOIR.accent, 0.25)`.
- [ ] **Step 5: `npx vitest run` — fix any color-asserting tests; `npx tsc --noEmit` clean.**
- [ ] **Step 6: Verify in dev**: transit ships show a flickering stern glow; planets have rim light; routes are hairlines.
- [ ] **Step 7: Commit**

```bash
git add visualizer/web/src/components/ShipSprite.tsx visualizer/web/src/components/WaypointSprite.tsx visualizer/web/src/components/RouteVectors.tsx
git commit -m "feat(viz): noir restyle - engine glow, rim light, hairline routes"
```

---

### Task 7: Server endpoints — events + gate progress

**Files:**
- Modify: `visualizer/server/routes/bot.ts`
- Create: `visualizer/server/routes/__tests__` only if a test harness already exists (it does not — verification is by build + curl).

**Interfaces:**
- Produces (consumed by Task 8):
  - `GET /api/bot/events?after=<id>&limit=<n>` → `{ events: Array<{ id: number; type: string; ship: string | null; createdAt: string; processed: boolean }> }` (newest first, `limit` default 50, max 200)
  - `GET /api/bot/construction/:waypointSymbol` → `{ progress: number | null; materials: Array<{ tradeSymbol: string; required: number; fulfilled: number }> }`

- [ ] **Step 1: Read** `visualizer/server/routes/bot.ts` and `visualizer/server/db/` to learn the existing query helper pattern (how other routes acquire the pg pool / run SQL). Copy that pattern exactly.
- [ ] **Step 2: Verify schema.** Source of truth: `gobot/internal/adapters/persistence/models.go` (READ ONLY — do not modify). Find the models for `captain_events` and the construction site/materials tables; note exact column names. If Postgres is up (`docker ps | grep spacetraders-postgres`), cross-check with `docker exec spacetraders-postgres psql -U spacetraders -d spacetraders -c '\d captain_events'` and `'\d construction_sites'` **[live-only]**.
- [ ] **Step 3: Implement both routes** following the file's existing style. Reference queries (adjust column names to what Step 2 found):

```sql
-- events
SELECT id, type, ship, created_at, processed_at IS NOT NULL AS processed
FROM captain_events
WHERE ($1::bigint IS NULL OR id > $1)
ORDER BY id DESC
LIMIT LEAST($2, 200);

-- construction — reference shape; ADJUST table/column names to what Step 2 finds in models.go.
-- If materials live in a child table:
SELECT s.progress, m.trade_symbol, m.required, m.fulfilled
FROM construction_sites s
LEFT JOIN construction_materials m ON m.construction_site_id = s.id
WHERE s.waypoint_symbol = $1;
-- If materials are embedded (JSONB column), select it and unpack in TS instead.
-- If no progress column exists, compute: SUM(fulfilled)::float / NULLIF(SUM(required), 0) * 100.
```

Guard both routes: on DB error return `503 { error: 'db_unavailable' }` (never 500 crash loops for an always-on client).

- [ ] **Step 4: Build:** from `visualizer/server/`: `npm run build` — clean tsc.
- [ ] **Step 5 [live-only]:** `curl -s localhost:4000/api/bot/events | head -c 400` and the construction route for `X1-PZ28-I67` — sane JSON.
- [ ] **Step 6: Commit**

```bash
git add visualizer/server/routes/bot.ts
git commit -m "feat(viz-server): events and construction progress endpoints"
```

---

### Task 8: Client polling for events + gate; connection health state

**Files:**
- Modify: `visualizer/web/src/services/api/bot.ts` (two fetchers)
- Modify: `visualizer/web/src/services/botPolling.ts` (poll cycle + backoff)
- Modify: `visualizer/web/src/store/useStore.ts` (new slices)
- Create: `visualizer/web/src/domain/opsState.ts`
- Test: `visualizer/web/src/domain/__tests__/opsState.test.ts`

**Interfaces:**
- Consumes: Task 7 endpoints.
- Produces:
  - Store slices: `fleetEvents: FleetEvent[]` (bounded 100), `gate: { progress: number | null; materials: GateMaterial[] }`, `connection: { status: 'ok' | 'lost'; lastContactAt: number | null }`, actions `ingestEvents(events: FleetEvent[])`, `setGate(g)`, `setConnection(c)`.
  - `FleetEvent = { id: number; type: string; ship: string | null; createdAt: string; processed: boolean }`
  - Pure: `deriveOpsState(input: { connectionOk: boolean; lastEventAgeMs: number | null; anyShipInTransit: boolean }): 'live' | 'idle' | 'lost'`

- [ ] **Step 1: Failing test for the pure state**

```ts
// visualizer/web/src/domain/__tests__/opsState.test.ts
import { describe, it, expect } from 'vitest';
import { deriveOpsState, FLEET_IDLE_AFTER_MS } from '../opsState';

describe('deriveOpsState', () => {
  it('lost when polling fails', () => {
    expect(deriveOpsState({ connectionOk: false, lastEventAgeMs: 1000, anyShipInTransit: true })).toBe('lost');
  });
  it('live while ships move or events are fresh', () => {
    expect(deriveOpsState({ connectionOk: true, lastEventAgeMs: null, anyShipInTransit: true })).toBe('live');
    expect(deriveOpsState({ connectionOk: true, lastEventAgeMs: 5_000, anyShipInTransit: false })).toBe('live');
  });
  it('idle when connected but nothing has happened for the window', () => {
    expect(deriveOpsState({ connectionOk: true, lastEventAgeMs: FLEET_IDLE_AFTER_MS + 1, anyShipInTransit: false })).toBe('idle');
  });
});
```

- [ ] **Step 2: Run — FAIL, then implement**

```ts
// visualizer/web/src/domain/opsState.ts
export const FLEET_IDLE_AFTER_MS = 10 * 60_000;

export type OpsState = 'live' | 'idle' | 'lost';

export function deriveOpsState(input: {
  connectionOk: boolean;
  lastEventAgeMs: number | null;
  anyShipInTransit: boolean;
}): OpsState {
  if (!input.connectionOk) return 'lost';
  if (input.anyShipInTransit) return 'live';
  if (input.lastEventAgeMs !== null && input.lastEventAgeMs > FLEET_IDLE_AFTER_MS) return 'idle';
  return 'live';
}
```

- [ ] **Step 3: Fetchers** in `services/api/bot.ts`, matching the file's existing `fetchApi` pattern (see `getAssignments` at line 24):

```ts
export async function getFleetEvents(afterId?: number, limit = 50): Promise<FleetEvent[]> {
  const q = new URLSearchParams();
  if (afterId !== undefined) q.set('after', String(afterId));
  q.set('limit', String(limit));
  const response = await fetchApi<{ events: FleetEvent[] }>(`/bot/events?${q}`);
  return response.events;
}

export async function getGateProgress(waypointSymbol: string): Promise<GateProgress> {
  return fetchApi<GateProgress>(`/bot/construction/${encodeURIComponent(waypointSymbol)}`);
}
```

(Declare `FleetEvent`/`GateProgress`/`GateMaterial` in `web/src/types/spacetraders.ts` exactly as in Interfaces above.)

- [ ] **Step 4: Poll cycle.** In `botPolling.ts`, inside the existing cycle (near the market-freshness fetches at ~line 144): fetch events (using highest ingested id from store as `after`) and gate progress for the gate waypoint. Gate waypoint symbol: add `GATE_WAYPOINT = 'X1-PZ28-I67'` to `web/src/constants/api.ts` (single source). On ANY poll-cycle failure set `connection.status='lost'` and apply exponential backoff (double interval up to 60s; reset on success + set `lastContactAt=Date.now()`, `status='ok'`) — implement backoff in the service's existing scheduling mechanism (read how it schedules; adjust the delay variable, don't add a second timer).
- [ ] **Step 5: Store slices** in `useStore.ts`: `ingestEvents` merges by id desc, caps at 100. Full suite + tsc green.
- [ ] **Step 6: Commit**

```bash
git add visualizer/web/src/services/api/bot.ts visualizer/web/src/services/botPolling.ts visualizer/web/src/store/useStore.ts visualizer/web/src/types/spacetraders.ts visualizer/web/src/constants/api.ts visualizer/web/src/domain/opsState.ts visualizer/web/src/domain/__tests__/opsState.test.ts
git commit -m "feat(viz): fleet events + gate polling, connection health, backoff"
```

---

### Task 9: Event drama — FX mapping + Konva FX layer

**Files:**
- Create: `visualizer/web/src/domain/eventFx.ts`
- Create: `visualizer/web/src/components/EventFxLayer.tsx`
- Test: `visualizer/web/src/domain/__tests__/eventFx.test.ts`
- Modify: `visualizer/web/src/components/SpaceMap.tsx` (mount layer above ships)

**Interfaces:**
- Consumes: `FleetEvent` (Task 8), ship/waypoint positions via existing lookups.
- Produces:

```ts
export type FxKind = 'arrival-ripple' | 'gate-flash' | 'income-ping' | 'generic-ping';
export interface FxInstance { key: string; kind: FxKind; x: number; y: number; startedAt: number; ttlMs: number }
export function fxForEvent(e: FleetEvent, resolvePos: (e: FleetEvent) => { x: number; y: number } | null, now: number): FxInstance | null
export function pruneFx(fx: FxInstance[], now: number): FxInstance[]
```

- [ ] **Step 1: Discover real event types.** Grep gobot (READ ONLY) for what gets inserted into `captain_events`: `grep -rn "captain_events\|EventType" gobot/internal --include='*.go' | head -30`. List the distinct type strings in a comment atop `eventFx.ts`. Map: arrival-ish types → `arrival-ripple`; construction/delivery types → `gate-flash`; transaction/sale types → `income-ping`; anything else → `generic-ping` (feed-only, no scene FX — return `null` position tolerated).
- [ ] **Step 2: Failing tests**

```ts
// visualizer/web/src/domain/__tests__/eventFx.test.ts
import { describe, it, expect } from 'vitest';
import { fxForEvent, pruneFx } from '../eventFx';

const at = (x: number, y: number) => () => ({ x, y });

describe('eventFx', () => {
  it('maps arrival-family events to a ripple at the ship', () => {
    const fx = fxForEvent({ id: 1, type: 'SHIP_ARRIVED', ship: 'TORWIND-4', createdAt: '', processed: false }, at(10, 20), 1000);
    expect(fx).toMatchObject({ kind: 'arrival-ripple', x: 10, y: 20, startedAt: 1000 });
  });
  it('unknown types with no position yield null (feed-only)', () => {
    const fx = fxForEvent({ id: 2, type: 'WEIRD', ship: null, createdAt: '', processed: false }, () => null, 1000);
    expect(fx).toBeNull();
  });
  it('pruneFx drops expired instances', () => {
    const list = [
      { key: 'a', kind: 'arrival-ripple' as const, x: 0, y: 0, startedAt: 0, ttlMs: 2000 },
      { key: 'b', kind: 'gate-flash' as const, x: 0, y: 0, startedAt: 1500, ttlMs: 2000 },
    ];
    expect(pruneFx(list, 2100).map(f => f.key)).toEqual(['b']);
  });
});
```

(Adjust `'SHIP_ARRIVED'` to a real type string found in Step 1; keep the test aligned with the mapping table.)

- [ ] **Step 3: Implement domain** (mapping table + TTLs: ripple 2400ms, gate-flash 3000ms, income-ping 1500ms; `key = String(e.id)`).
- [ ] **Step 4: Implement `EventFxLayer`** — a `react-konva` `Layer` (or `Group` inside the main layer, matching how sibling overlay layers mount — read how `MiningLaserLayer` mounts and copy it). Per `FxInstance`, derive `age = frameTimestamp - startedAt`, `p = age / ttlMs`:
  - `arrival-ripple`: `Circle` radius `4 + p * 26`, stroke `NOIR.accentSoft`, opacity `1 - p`, strokeWidth 1.4, `listening={false}`.
  - `gate-flash`: `Rect` 10×10 centered, stroke `NOIR.warn`, opacity `(1 - p) * 0.9`, shadowBlur 10.
  - `income-ping`: `Circle` radius 3, fill `NOIR.good`, opacity `1 - p`.
  New events → instances: subscribe to `fleetEvents`, map through `fxForEvent` with a resolver that looks up the ship's current render position (ships) or the gate waypoint (gate-flash), keep in a ref, `pruneFx` each frame.
- [ ] **Step 5: Mount in `SpaceMap.tsx`** above `ShipLayer`. Full suite + tsc green.
- [ ] **Step 6: Verify in demo mode (Task 11 provides synthetic events; if executing in order, defer the visual check to Task 11 Step 6 and note it).**
- [ ] **Step 7: Commit**

```bash
git add visualizer/web/src/domain/eventFx.ts visualizer/web/src/domain/__tests__/eventFx.test.ts visualizer/web/src/components/EventFxLayer.tsx visualizer/web/src/components/SpaceMap.tsx
git commit -m "feat(viz): event drama fx layer"
```

---

### Task 10: Glass HUD + theater layout + cinema toggle

**Files:**
- Create: `visualizer/web/src/components/hud/HudRoot.tsx`, `HudTicker.tsx`, `HudEventFeed.tsx`, `HudDetailCard.tsx`
- Modify: `visualizer/web/src/pages/MapView.tsx` (full-bleed + HUD mount + `c` key)
- Test: `visualizer/web/src/components/hud/__tests__/HudTicker.test.tsx`, `HudEventFeed.test.tsx`

**Interfaces:**
- Consumes: store slices from Task 8 (`fleetEvents`, `gate`, `connection`), `deriveOpsState`, selected-ship state that `MapView`/`SpaceMap` already manage (search `selectedObject` / `SelectionOverlay` and reuse the same selection source).
- Produces: `<HudRoot />` self-contained (reads store directly); store gains `cinemaMode: boolean` + `toggleCinemaMode()`.

- [ ] **Step 1: Failing component tests**

```tsx
// visualizer/web/src/components/hud/__tests__/HudTicker.test.tsx
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { HudTicker } from '../HudTicker';

describe('HudTicker', () => {
  it('shows treasury, rate and gate progress', () => {
    render(<HudTicker treasury={7712381} ratePerHour={150000} gateProgress={12.4} opsState="live" lastContactAt={null} />);
    expect(screen.getByText(/7\.71M/)).toBeInTheDocument();
    expect(screen.getByText(/\+150K\/H/i)).toBeInTheDocument();
    expect(screen.getByText(/GATE 12%/i)).toBeInTheDocument();
  });
  it('renders SIGNAL LOST when ops state is lost', () => {
    render(<HudTicker treasury={0} ratePerHour={0} gateProgress={null} opsState="lost" lastContactAt={Date.now() - 42_000} />);
    expect(screen.getByText(/SIGNAL LOST/i)).toBeInTheDocument();
    expect(screen.getByText(/LAST CONTACT/i)).toBeInTheDocument();
  });
});
```

```tsx
// visualizer/web/src/components/hud/__tests__/HudEventFeed.test.tsx
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { HudEventFeed } from '../HudEventFeed';

describe('HudEventFeed', () => {
  it('renders newest events first, max 4', () => {
    const events = [1, 2, 3, 4, 5, 6].map(i => ({
      id: i, type: 'SHIP_ARRIVED', ship: `TORWIND-${i}`, createdAt: new Date().toISOString(), processed: false,
    }));
    render(<HudEventFeed events={events} />);
    const rows = screen.getAllByTestId('hud-event');
    expect(rows).toHaveLength(4);
    expect(rows[0]).toHaveTextContent('TORWIND-6');
  });
});
```

- [ ] **Step 2: Run — FAIL, then implement the three chips + root.** Styling: Tailwind utility classes with inline `style` for Noir colors (`backgroundColor: noirAlpha(NOIR.panel, 0.78)`, `backdropFilter: 'blur(10px)'`, `border: 1px solid noirAlpha(NOIR.accent, 0.18)`, monospace font, `borderRadius: 9999`). Layout via `HudRoot`:
  - Ticker: `absolute top-4 right-4` — treasury (compact `7.71M` formatting: `(n/1e6).toFixed(2)+'M'` ≥1e6, `(n/1e3).toFixed(0)+'K'` ≥1e3), rate, `GATE {progress.toFixed(0)}%`; when `opsState==='lost'` swap content to `SIGNAL LOST · LAST CONTACT {mm:ss}` in `NOIR.bad`; when `'idle'` append `FLEET IDLE` in `NOIR.dim`.
  - Feed: `absolute bottom-4 left-4`, newest 4 events, opacity stepped `1 / .75 / .5 / .3`, each row `data-testid="hud-event"` `{ship} · {type humanized (underscores→spaces, lowercase)} · {age}s`.
  - Detail card: `absolute right-4 bottom-4 w-64`, renders ONLY when a ship is selected — name, role/status line, fuel and cargo bars (thin 3px rounded divs, fill `NOIR.accent`/`NOIR.good`, width `%`), slide-in via Tailwind `transition-transform translate-x-0 / translate-x-[120%]`; auto-hide after 30s idle selection (a `setTimeout` cleared on re-selection; pause when `document.hidden`).
  - Treasury source: reuse what the store already exposes (grep `credits` in `useStore.ts`; the `AgentCredits` component shows where the number lives). Rate source: `getMarketTransactions(systemSymbol, limit)` already exists in `services/api/bot.ts` (line ~134) — fetch the last hour in the poll cycle, compute `ratePerHour = (latest balance − balance one hour ago)`; if fewer than 2 transactions in the window, pass `ratePerHour={null}` and `HudTicker` hides that segment (never invent data).
- [ ] **Step 3: Theater layout.** In `MapView.tsx` (191 lines — read fully): make the map container `fixed inset-0` full-bleed; move any existing sidebars/panels so they only render when `!cinemaMode` (keep their functionality — this is relocation, not deletion); mount `<HudRoot />` last (highest z). Add `c` key handler (ignore when typing in inputs: check `e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement`): `toggleCinemaMode()` hides ALL DOM chrome except the ticker (cinema = scene + ticker only).
- [ ] **Step 4: Store additions**: `cinemaMode: false`, `toggleCinemaMode`. Burn-in micro-drift: in `HudRoot`, wrap children in a div whose `transform` shifts by `((Date.now()/3_600_000) % 12)` px on a 12-px cycle, updated by a 5-minute interval (cleared on unmount + paused when `document.hidden`).
- [ ] **Step 5: Full suite + tsc green. Verify in dev**: HUD chips overlay the scene, `c` collapses chrome, selection slides the card in.
- [ ] **Step 6: Commit**

```bash
git add visualizer/web/src/components/hud visualizer/web/src/pages/MapView.tsx visualizer/web/src/store/useStore.ts
git commit -m "feat(viz): glass HUD, theater layout, cinema toggle"
```

---

### Task 11: Demo mode — synthetic motion + events (visual QA harness)

**Files:**
- Modify: `visualizer/web/src/mocks/mockScenario.ts`
- Modify: `visualizer/web/src/services/api/mockClient.ts`
- Test: `visualizer/web/src/mocks/__tests__/demoMotion.test.ts` (create)

**Interfaces:**
- Consumes: existing mock plumbing (READ `mockClient.ts` first to learn how mock mode is enabled — env var or URL param — and document the exact switch in the task report).
- Produces: in mock mode, at least 2 ships perpetually in transit (routes regenerate on arrival), a synthetic event every ~8s cycling through the real type strings from Task 9, gate progress that ticks up 0.1% per delivery event, and one simulated 20s connection loss every 3 minutes (exercises SIGNAL LOST).

- [ ] **Step 1: Read `mockClient.ts` + `mockScenario.ts` fully.** Identify where ships are produced and whether nav routes are static.
- [ ] **Step 2: Failing test for route regeneration**

```ts
// visualizer/web/src/mocks/__tests__/demoMotion.test.ts
import { describe, it, expect } from 'vitest';
import { regenerateRouteIfArrived } from '../demoMotion';

describe('demo motion', () => {
  it('re-issues a fresh route once a ship arrives', () => {
    const now = Date.now();
    const ship: any = {
      nav: {
        status: 'IN_TRANSIT',
        route: {
          origin: { symbol: 'A', x: 0, y: 0 },
          destination: { symbol: 'B', x: 100, y: 0 },
          departureTime: new Date(now - 60_000).toISOString(),
          arrival: new Date(now - 1_000).toISOString(),
        },
      },
    };
    const waypoints: any[] = [
      { symbol: 'B', x: 100, y: 0 }, { symbol: 'C', x: 0, y: 120 },
    ];
    const next = regenerateRouteIfArrived(ship, waypoints, now);
    expect(next.nav.route.origin.symbol).toBe('B');
    expect(next.nav.route.destination.symbol).toBe('C');
    expect(new Date(next.nav.route.arrival).getTime()).toBeGreaterThan(now + 20_000);
  });
});
```

- [ ] **Step 3: Implement `visualizer/web/src/mocks/demoMotion.ts`** (`regenerateRouteIfArrived(ship, waypoints, now)`: if `IN_TRANSIT` and `arrival <= now`, pick the next waypoint (deterministic round-robin by symbol order, not random), duration 45–90s scaled by distance) plus a `demoEventTicker(nowMs)` that returns a synthetic `FleetEvent` every 8s. Wire both into `mockClient.ts`'s ship/event fetch paths; add the periodic connection-loss simulation behind a `DEMO_SIGNAL_LOSS = true` const.
- [ ] **Step 4: Suite green. Run dev in mock mode** (using the switch discovered in Step 1) — full loop: ships glide continuously, trails fade, FX fire on synthetic events, feed scrolls, gate % ticks, SIGNAL LOST engages ~every 3 min and recovers.
- [ ] **Step 5: Commit**

```bash
git add visualizer/web/src/mocks visualizer/web/src/services/api/mockClient.ts
git commit -m "feat(viz): demo mode with perpetual motion and synthetic events"
```

---

### Task 12: Always-on hardening + soak

**Files:**
- Modify: `visualizer/web/src/hooks/useKonvaStage.ts` (pause tick on hidden)
- Modify: `visualizer/web/src/services/botPolling.ts` (slow-poll on hidden)
- Test: `visualizer/web/src/hooks/__tests__/visibilityPause.test.ts` (create)

- [ ] **Step 1: Read `useKonvaStage.ts`** — find how `onAnimationTick` is driven (Konva.Animation or interval).
- [ ] **Step 2: Failing test** — extract the pure gate:

```ts
// visualizer/web/src/hooks/__tests__/visibilityPause.test.ts
import { describe, it, expect } from 'vitest';
import { shouldRenderFrame } from '../renderGate';

describe('render gate', () => {
  it('blocks frames while document is hidden', () => {
    expect(shouldRenderFrame(true)).toBe(false);
    expect(shouldRenderFrame(false)).toBe(true);
  });
});
```

```ts
// visualizer/web/src/hooks/renderGate.ts
export function shouldRenderFrame(hidden: boolean): boolean {
  return !hidden;
}
```

- [ ] **Step 3: Wire**: in `useKonvaStage.ts`, skip the tick when `!shouldRenderFrame(document.hidden)`; add a `visibilitychange` listener that forces one immediate tick on return (so the scene snaps current). In `botPolling.ts`, multiply the poll interval by 6 while hidden.
- [ ] **Step 4: Memory bound audit.** Grep for every unbounded collection touched this plan: trails (bounded Task 5), `fleetEvents` (bounded Task 8), FX list (pruned Task 9), position cache (already pruned to known ships in `SpaceMap.tsx:233-241`). Confirm each bound in a checklist in the commit message.
- [ ] **Step 5: Soak [manual, mock mode]**: leave dev running ≥2h (24h ideal) with the Performance monitor; heap must plateau. Record start/end heap MB in the task report.
- [ ] **Step 6: Full suite + tsc. Commit**

```bash
git add visualizer/web/src/hooks/useKonvaStage.ts visualizer/web/src/hooks/renderGate.ts visualizer/web/src/hooks/__tests__/visibilityPause.test.ts visualizer/web/src/services/botPolling.ts
git commit -m "feat(viz): always-on hardening - visibility pause, slow-poll, bounded buffers"
```

---

## Execution notes

- **Motion core already exists** — do not build it: `Ship.getPosition` (`web/src/domain/ship.ts`) interpolates transit positions from `route.departureTime`/`route.arrival`, `getShipRenderPosition` (`SpaceMap.tsx:190`) smooths status transitions, and `calculateShipRotation` handles heading. The spec's "motion core" workstream is satisfied by these plus Tasks 5/6 polish.
- Task order is dependency order; 1→2→3→4 and 5/6 can interleave after 1; 7→8→9→10; 11 unlocks full visual verification; 12 last.
- Any **[live-only]** step skipped because daemon/Postgres were down must be listed in the final report for the Admiral to run after fleet restart.
- After all tasks: run the demo-mode loop once end-to-end and capture a short screen recording for the Admiral.
