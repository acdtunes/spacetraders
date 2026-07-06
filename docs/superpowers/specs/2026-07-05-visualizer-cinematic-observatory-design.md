# Visualizer: Cinematic Observatory — Design

**Date:** 2026-07-05
**Status:** Approved (Admiral, via brainstorm w/ visual companion)
**Scope:** `visualizer/` (React 18 + Konva + Zustand + Tailwind web app, `web/` + `server/`)

## Purpose

Turn the fleet visualizer into a **cinematic observatory**: watching the TORWIND fleet
should feel like a living game world. Primary deployment is a **dedicated, always-on
display** (24/7 wall of mission control). Beauty of the scene comes first; operational
truth stays one glance away.

Decided via visual companion boards:

| Decision | Choice |
|---|---|
| Primary job | Cinematic observatory (not mission-control density, not analyst drill-down) |
| Art direction | **Deep Space Noir** — near-black indigo, faint nebula haze, hairline orbit rings, atmosphere rim-glow, crisp ship glyphs, luminous trails |
| Motion layers | **Ship life** (primary), **cinematic camera**, **event drama**. NO orbital motion — SpaceTraders waypoints have fixed positions |
| Chrome | **Theater + HUD** — full-bleed scene, floating glass chips; Director's-cut letterbox as a `c` toggle |
| Build approach | **Layered compositing** — ambient backdrop + upgraded Konva scene + DOM glass HUD; no renderer rewrite |

## The experience

A full-bleed Deep Space Noir rendering of X1-PZ28 that is never still and never loud:

- **Ships glide** continuously along routes — position computed per frame from nav route
  departure/arrival timestamps (`(navRoute, now) → {x, y, heading, progress}`), so motion
  is true, not poll-jumpy. Engine glow flickers in transit; a luminous trail fades behind
  each ship (~60s). Nose points along the route.
- **Events are quiet theater**: arrival ripple where a ship drops out of transit,
  siphon/mining beam pulses, gate-delivery flash with a progress tick, soft income ping
  in the ticker.
- **The camera is alive**: slow idle drift + breathing zoom (attract mode), smooth
  ease-to-target on selection or major events, optional follow-cam on a chosen ship.
  Manual wheel/drag always wins instantly and pauses auto-camera for a grace period.
- **Waypoints are fixed** (game constraint). Ambient life = star breath, star twinkle,
  backdrop parallax — subtle enough for 24/7 duty.
- **HUD is floating glass**: treasury ticker + rate + gate % top-right; fading event feed
  bottom-left; detail card slides in on selection, auto-hides after idle. `c` collapses
  all chrome (cinema mode).
- **Failure is beautiful**: daemon unreachable → scene dims to **SIGNAL LOST**, starfield
  keeps breathing, HUD greys with "last contact mm:ss ago". Fleet stopped but daemon up →
  calm "FLEET IDLE · last activity hh:mm".

## Architecture — 3 composited layers, existing app intact

```
┌─ DOM HUD (React, Tailwind glass) ── ticker · event feed · detail card ─┐
│ ┌─ Konva scene (existing, upgraded) ─ ships · trails · routes · fx ──┐ │
│ │ ┌─ Ambient backdrop ─ pre-rendered nebula/starfield, parallax ──┐ │ │
└─┴─┴────────────────────────────────────────────────────────────────┴─┴─┘
         ▲ one rAF clock drives interpolation, tweens, fades
         ▲ existing Zustand stores + polling hooks remain the data source
```

- **Backdrop layer**: pre-rendered Noir nebula/starfield art on its own canvas; slow
  parallax tied to camera transform; zero per-frame cost beyond a transform.
- **Scene layer**: existing Konva components upgraded in place (no renderer swap).
- **HUD layer**: DOM text (crisp at any scale), `backdrop-filter` glass.
- **Data flow unchanged**: polling cadence stays; interpolation decouples visual
  smoothness from poll rate. Server additions only if event/gate endpoints are missing.

## Workstreams

1. **Motion core** — single rAF clock; `useShipInterpolation` pure function with
   clamping before departure / after arrival; gentle arrival easing.
2. **Trails & engine FX** — time-based fade (~60s), bounded ring buffer per ship,
   layered glow sprites for engine flicker.
3. **Cinematic camera** — idle drift + breathing zoom; ease-to-target; follow-cam;
   manual-override-wins with grace period.
4. **Event drama** — FX layer mapping event stream → scene effects (arrival ripple,
   siphon beam, gate flash + tick, income ping).
5. **Ambient backdrop** — nebula/starfield art + parallax.
6. **Glass HUD** — ticker chip, fading event feed, slide-in detail card, `c` cinema
   toggle, SIGNAL LOST / FLEET IDLE treatments.
7. **Noir restyle** — palette tokens; restyled waypoint/route/selection sprites (rim
   glow, hairline decorative orbit rings around the star).
8. **Always-on hardening** — the contract below.

## Always-on contract

- Tab hidden → rendering suspends (polls may continue cheaply).
- Daemon unreachable → SIGNAL LOST + exponential backoff + silent auto-recover.
- Fleet stopped, daemon up → FLEET IDLE with last-activity timestamp (truthful, calm).
- All buffers bounded (trails, event feed, FX queue). Target: **24h soak, flat memory**.
- HUD micro-drifts a few px/hour against burn-in; palette is dark by design.

## Testing

- Pure units (vitest, suite exists): interpolation math incl. clamping, trail ring
  buffer, camera easing targets, event→FX mapping.
- Component tests (testing-library, exists): HUD states incl. SIGNAL LOST / FLEET IDLE.
- **Demo mode**: extend `web/src/mocks/mockScenario.ts` into a synthetic-motion scenario
  — the visual QA harness; enables building everything while the fleet is stopped.
- Manual: 24h soak against demo mode watching memory.

## Out of scope

Galaxy view redesign (stays functional as-is), 3D, orbital motion, mobile, functional
redesign of market/finance panels (keep function, gain Noir skin only).

## Assumptions to verify at planning

1. Ship nav data reaching the web app includes route origin/destination coords AND
   departure/arrival timestamps (needed for interpolation).
2. An event stream (arrivals, deliveries, siphon activity) is already exposed to the
   web app, or `server/` can cheaply expose it.
3. Gate (construction) progress is available or cheaply exposable for the HUD ticker.
