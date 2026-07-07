import { useEffect, useRef, useState } from 'react';
import type { CSSProperties } from 'react';
import { NOIR, noirAlpha } from '../theme/noir';
import { starfield, parallaxOffset } from '../domain/backdrop';

interface Props { pan: { x: number; y: number } }

const FAR = 0.03;
const NEAR = 0.08;
const BLEED = 0.15; // matches the canvas `inset: -15%` below

function paint(
  canvas: HTMLCanvasElement,
  dpr: number,
  w: number,
  h: number,
  seed: number,
  count: number,
  nebula: boolean
) {
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  // Draw in CSS pixels; the backing store is dpr-scaled so stars stay crisp on HiDPI.
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);
  if (nebula) {
    // Core -> mid -> transparent keeps the noir mood while staying legible on
    // real displays; the original single dark stop blended invisibly into bg0.
    const blobs: Array<[number, number, number, string, string]> = [
      [w * 0.72, h * 0.12, w * 0.5, noirAlpha(NOIR.nebulaCore, 0.7), noirAlpha(NOIR.nebula, 0.45)],
      [w * 0.15, h * 0.85, w * 0.45, noirAlpha(NOIR.nebula, 0.6), noirAlpha(NOIR.bg1, 0.5)],
      [w * 0.45, h * 0.5, w * 0.7, noirAlpha(NOIR.nebulaCore, 0.35), noirAlpha(NOIR.nebula, 0.2)],
    ];
    for (const [x, y, r, core, mid] of blobs) {
      const g = ctx.createRadialGradient(x, y, 0, x, y, r);
      g.addColorStop(0, core);
      g.addColorStop(0.45, mid);
      g.addColorStop(1, 'rgba(0,0,0,0)');
      ctx.fillStyle = g;
      ctx.fillRect(0, 0, w, h);
    }
  }
  ctx.fillStyle = NOIR.star;
  for (const s of starfield(seed, count, w, h)) {
    ctx.globalAlpha = s.a;
    ctx.beginPath();
    ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.globalAlpha = 1;
}

export function AmbientBackdrop({ pan }: Props) {
  const farRef = useRef<HTMLCanvasElement>(null);
  const nearRef = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });

  useEffect(() => {
    const layers = [
      [farRef, 7, 220, true],
      [nearRef, 13, 90, false],
    ] as const;
    const dpr = window.devicePixelRatio || 1;
    const render = () => {
      let cssW = 0;
      let cssH = 0;
      for (const [ref, seed, count, nebula] of layers) {
        const c = ref.current;
        if (!c) continue;
        // Size the backing store from the element's own CSS box (which already
        // includes the -15% bleed), not the window, so it never distorts.
        cssW = c.clientWidth || window.innerWidth;
        cssH = c.clientHeight || window.innerHeight;
        c.width = Math.max(1, Math.round(cssW * dpr));
        c.height = Math.max(1, Math.round(cssH * dpr));
        paint(c, dpr, cssW, cssH, seed, count, nebula);
      }
      setSize({ w: cssW, h: cssH });
    };
    render();
    const target = farRef.current?.parentElement;
    const ro =
      target && typeof ResizeObserver !== 'undefined' ? new ResizeObserver(render) : null;
    ro?.observe(target as Element);
    return () => ro?.disconnect();
  }, []);

  // Keep each layer within its bleed so panning/zooming can never expose a starless edge.
  const clampAxis = (v: number, extent: number) => {
    const limit = BLEED * extent;
    return Math.max(-limit, Math.min(limit, v));
  };
  const offset = (factor: number) => {
    const o = parallaxOffset(pan, factor);
    return { x: clampAxis(o.x, size.w), y: clampAxis(o.y, size.h) };
  };
  const far = offset(FAR);
  const near = offset(NEAR);
  const style = (o: { x: number; y: number }): CSSProperties => ({
    position: 'absolute',
    inset: '-15%',
    // A <canvas> is a replaced element, so `inset` alone only positions it — it
    // keeps its intrinsic 300x150 box. Give it an explicit 130% CSS size (100% +
    // the 15% bleed on each side) so it actually fills the viewport; otherwise
    // clientWidth reads 300 and the whole nebula paints into a corner tile.
    width: '130%',
    height: '130%',
    transform: `translate3d(${o.x}px, ${o.y}px, 0)`,
    willChange: 'transform',
    pointerEvents: 'none',
  });

  return (
    <div
      style={{
        position: 'absolute',
        inset: 0,
        zIndex: -1,
        overflow: 'hidden',
        background: NOIR.bg0,
        pointerEvents: 'none',
      }}
      aria-hidden
    >
      <canvas ref={farRef} style={style(far)} />
      <canvas ref={nearRef} className="viz-twinkle" style={style(near)} />
    </div>
  );
}
