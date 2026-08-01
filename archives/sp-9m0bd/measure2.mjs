// sp-9m0bd acceptance rig. One calibrated REGION camera over the reported dense
// neighbourhood; two shots at that camera (Freshness ON and OFF) so every claim
// is a DIFFERENCE the freshness layer is responsible for, not an absolute a
// changing scene could drift.
//
//  (a) density   — crossings of the marks' nominal radii in frame, AND the hard
//                  edge the freshness layer actually adds to the framebuffer.
//  (b) labels    — glyph contrast against the ground immediately around each
//                  glyph. The glyph MASK comes from the Freshness-OFF shot, so
//                  it is exact and identical between the two builds.
//  (c) ramp      — each step's rendered colour, sampled only on systems whose
//                  mark no neighbour crosses, and the pairwise CIEDE2000.
//  (e) invariants— priced vs dark peak luminance in the same annulus, and the
//                  fresh mark vs the active orb halo.
import { launch, decodePng, px, contrastRatio, deltaE2000, lum255 } from './cdp.mjs';
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';

const SP = process.env.SP;
const PORT = process.env.VIZ_PORT ?? '5199';
const OUT = process.env.OUT;
const TAG = process.env.TAG ?? 'x';
const NOTCHES = Number(process.env.NOTCHES ?? 15);
const FOCUS = process.env.FOCUS ?? 'X1-KD64';
const K = JSON.parse(process.env.KNOBS); // { markScale, markMinPx }
mkdirSync(OUT, { recursive: true });

const topology = JSON.parse(readFileSync(`${SP}/topology.json`, 'utf8'));
const scene = JSON.parse(readFileSync(`${SP}/scene.json`, 'utf8'));

const b = await launch({ width: 1600, height: 900, port: Number(process.env.CDP_PORT ?? 9333) });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const waitFresh = async () => {
  for (let i = 0; i < 60; i++) {
    const s = await b.evaluate(`document.querySelector('[data-testid="freshness-full-scale"]')?.textContent ?? ''`);
    if (s && s !== '—') return s;
    await sleep(400);
  }
  throw new Error('freshness poll never landed — a measurement here would be worthless');
};

await b.goto(`http://localhost:${PORT}/trade-flows?band=system&focus=${FOCUS}`, 3000);
const fullScale = await waitFresh();
await sleep(3500);
const canvas = await b.evaluate(`(() => { const c=document.querySelector('canvas'); const r=c.getBoundingClientRect();
  return {left:r.left, top:r.top, cw:c.clientWidth, ch:c.clientHeight}; })()`);
// NO Escape here: its handler calls fitGalaxy(), which flies the camera back to
// the galaxy centre and silently invalidates every predicted position below.
// Wheeling out past SYSTEM_EXIT clears the focus by itself, without a flight, so
// the anchored zoom keeps FOCUS dead centre — which is the whole calibration.
await b.evaluate(`(() => { const c=document.querySelector('canvas'); const r=c.getBoundingClientRect();
  const cx=r.left+r.width/2, cy=r.top+r.height/2;
  for(let i=0;i<${NOTCHES};i++) c.dispatchEvent(new WheelEvent('wheel',{deltaY:100,clientX:cx,clientY:cy,bubbles:true,cancelable:true}));
  return true; })()`);
await sleep(2500);

const onPath = `${OUT}/${TAG}-region-dense.png`;
await b.shot(onPath);
const ON = decodePng(readFileSync(onPath));
// Freshness OFF at the SAME camera — the reference frame.
const toggled = await b.evaluate(`(() => {
  const el=[...document.querySelectorAll('button')].find(x=>/freshness/i.test(x.textContent.trim()));
  if (el) el.click(); return !!el; })()`);
if (!toggled) throw new Error('Freshness toggle not found — the OFF reference frame is the whole method');
await sleep(1400);
const offPath = `${OUT}/${TAG}-region-dense-freshness-off.png`;
await b.shot(offPath);
const OFF = decodePng(readFileSync(offPath));

// ---- camera, derived exactly as NebulaScene does, then verified -----------
const PAD = 60;
const xs = topology.systems.map((s) => s.x), ys = topology.systems.map((s) => s.y);
const bw = Math.max(...xs) + PAD - (Math.min(...xs) - PAD);
const bh = Math.max(...ys) + PAD - (Math.min(...ys) - PAD);
const fitScale = Math.min(canvas.cw / bw, canvas.ch / bh);
const scale = (fitScale * 12) / Math.pow(1.1, NOTCHES);
const f0 = scene.systems.find((s) => s.symbol === FOCUS);
const camX = canvas.left + canvas.cw / 2 - f0.x * scale;
const camY = canvas.top + canvas.ch / 2 - f0.y * scale;
const toScreen = (wx, wy) => [camX + wx * scale, camY + wy * scale];
const worldPerPx = scene.worldPerPx;
const inFrame = (x, y, m = 0) =>
  x >= canvas.left + m && x < canvas.left + canvas.cw - m && y >= canvas.top + m && y < canvas.top + canvas.ch - m;

// STRICT calibration. "Something bright is near the prediction" is not a check:
// in a field this dense a neighbour's halo satisfies it, and a loose version of
// this guard passed a camera that was centred on the wrong point entirely. An
// ACTIVE orb has a white-hot core, so demand that the brightest pixel in a +-4px
// window IS the predicted centre, to within a pixel, and is actually white-hot.
const calib = [];
for (const s of [...scene.systems].sort((a, c) => c.orbPx - a.orbPx)) {
  if (s.activityAbs <= 0) continue;                 // dormant orbs have no white core
  const [sx, sy] = toScreen(s.x, s.y);
  if (!inFrame(sx, sy, 30)) continue;
  // CENTROID, not argmax: an active core saturates at 255 across several pixels,
  // so "the brightest pixel" is whichever the scan reaches first — a scan-order
  // artefact that reads as a 4px camera error on a camera that is exact.
  let wsum = 0, wx = 0, wy = 0, peak = 0;
  for (let dy = -5; dy <= 5; dy++) for (let dx = -5; dx <= 5; dx++) {
    const L = lum255(px(ON, Math.round(sx + dx), Math.round(sy + dy)));
    peak = Math.max(peak, L);
    if (L > 200) { wsum += L; wx += dx * L; wy += dy * L; }
  }
  const off = wsum > 0 ? [Number((wx / wsum).toFixed(2)), Number((wy / wsum).toFixed(2))] : [99, 99];
  calib.push({ symbol: s.symbol, peak: Number(peak.toFixed(0)), coreCentroidOffset: off,
               ok: peak > 200 && Math.hypot(off[0], off[1]) <= 1.5 });
  if (calib.length >= 10) break;
}
// Judged on the MEDIAN offset, with a cap on outliers. A per-system all-must-pass
// rule is brittle here for an honest reason: at this density a neighbour's core
// lands inside the +-5px window and drags one centroid, which says nothing about
// the camera. A wholesale camera error moves EVERY core by tens of pixels and
// drops the peaks to background, so the median catches it with room to spare.
const dist = calib.map((c) => Math.hypot(c.coreCentroidOffset[0], c.coreCentroidOffset[1])).sort((a, c) => a - c);
const medianOffset = dist.length ? dist[Math.floor(dist.length / 2)] : Infinity;
const outliers = dist.filter((d) => d > 3).length;
const dim = calib.filter((c) => c.peak <= 200).length;
if (calib.length < 8 || medianOffset > 1.5 || outliers > 2 || dim > 0)
  throw new Error(`camera calibration failed (n=${calib.length}, median core offset ${medianOffset.toFixed(2)}px, `
    + `${outliers} beyond 3px, ${dim} cores below peak 200): ` + JSON.stringify(calib));
const calibrated = `n=${calib.length}, median core offset ${medianOffset.toFixed(2)}px, ${outliers} beyond 3px`;

// ---- marks in frame -------------------------------------------------------
const marks = scene.systems.map((s) => {
  const [sx, sy] = toScreen(s.x, s.y);
  const r = Math.max(K.markMinPx * worldPerPx, s.orbPx * worldPerPx * K.markScale) * scale;
  return { ...s, sx, sy, r };
}).filter((m) => inFrame(m.sx, m.sy, -m.r * 0.5));

let crossPairs = 0, pricedPairs = 0;
const crossedBy = new Map();
for (let i = 0; i < marks.length; i++) for (let j = i + 1; j < marks.length; j++) {
  const a = marks[i], c = marks[j];
  const d = Math.hypot(a.sx - c.sx, a.sy - c.sy);
  if (d < a.r + c.r && d > Math.abs(a.r - c.r)) {
    crossPairs++; if (a.priced && c.priced) pricedPairs++;
    crossedBy.set(a.symbol, (crossedBy.get(a.symbol) ?? 0) + 1);
    crossedBy.set(c.symbol, (crossedBy.get(c.symbol) ?? 0) + 1);
  }
}

// ---- (a) rendered hard-edge energy the freshness layer ADDS ---------------
// |∇L| per pixel, summed over the canvas, ON minus OFF. A hard stroke is a step
// in luminance; a soft band is a gradient. This is the moiré, in a number.
function edgeEnergy(img) {
  let strong = 0, total = 0;
  const x0 = Math.round(canvas.left) + 1, x1 = Math.round(canvas.left + canvas.cw) - 2;
  const y0 = Math.round(canvas.top) + 1, y1 = Math.round(canvas.top + canvas.ch) - 2;
  for (let y = y0; y <= y1; y++) for (let x = x0; x <= x1; x++) {
    const gx = lum255(px(img, x + 1, y)) - lum255(px(img, x - 1, y));
    const gy = lum255(px(img, x, y + 1)) - lum255(px(img, x, y - 1));
    const g = Math.hypot(gx, gy);
    total += g;
    if (g > 24) strong += 1; // a step the eye reads as a contour
  }
  return { strongEdgePixels: strong, gradientSum: Math.round(total) };
}
const edgeOn = edgeEnergy(ON), edgeOff = edgeEnergy(OFF);

// ---- (b) label glyph contrast --------------------------------------------
// Glyph mask from the OFF frame (labels draw above everything and are identical
// in both), measured in the ON frame against the ground right next to them.
const labelled = [...scene.systems]
  .sort((a, c) => c.activityAbs - a.activityAbs || (a.symbol < c.symbol ? -1 : 1))
  .slice(0, 20);
function labelReport(s) {
  const markR = Math.max(K.markMinPx * worldPerPx, s.orbPx * worldPerPx * K.markScale);
  const gapFrom = K.labelFromMark ? markR : s.orbPx * worldPerPx;
  const [sx, top] = toScreen(s.x, s.y + gapFrom + 5 * worldPerPx);
  const h = 11 * worldPerPx * scale;
  const w = s.symbol.length * 6.8 * worldPerPx * scale;
  const box = { x0: Math.round(sx - w / 2) - 2, x1: Math.round(sx + w / 2) + 2, y0: Math.round(top) - 2, y1: Math.round(top + h) + 2 };
  if (!inFrame(box.x0, box.y0, 2) || !inFrame(box.x1, box.y1, 2)) return null;
  const glyph = [], near = [];
  const isGlyph = (x, y) => lum255(px(OFF, x, y)) > 45;
  for (let y = box.y0; y <= box.y1; y++) for (let x = box.x0; x <= box.x1; x++) {
    if (isGlyph(x, y)) { glyph.push([x, y]); continue; }
    // ground pixel adjacent to a glyph pixel — what the eye separates against
    let adj = false;
    for (let dy = -2; dy <= 2 && !adj; dy++) for (let dx = -2; dx <= 2; dx++) {
      if (isGlyph(x + dx, y + dy)) { adj = true; break; }
    }
    if (adj) near.push([x, y]);
  }
  if (glyph.length < 20 || near.length < 20) return { symbol: s.symbol, glyphPx: glyph.length, note: 'glyph not resolved in frame' };
  const med = (pts, img) => {
    const ch = [0, 1, 2].map((i) => pts.map(([x, y]) => px(img, x, y)[i]).sort((a, c) => a - c));
    return ch.map((v) => v[Math.floor(v.length / 2)]);
  };
  const worstIn = (img) => near.reduce((acc, [x, y]) => (lum255(px(img, x, y)) > lum255(acc) ? px(img, x, y) : acc), [0, 0, 0]);
  const g = med(glyph, ON);
  // ...and the SAME pixels with the freshness layer switched off. Without this
  // the residual is unattributable: the brightest thing beside a glyph is often
  // a lane or a neighbouring orb, and blaming freshness for it would be a
  // measurement that cannot tell the fix from the thing it did not touch.
  const gOff = med(glyph, OFF);
  return {
    symbol: s.symbol, glyphPx: glyph.length, nearPx: near.length,
    contrastVsGround: Number(contrastRatio(g, med(near, ON)).toFixed(2)),
    contrastVsWorstGround: Number(contrastRatio(g, worstIn(ON)).toFixed(2)),
    contrastVsGround_freshnessOff: Number(contrastRatio(gOff, med(near, OFF)).toFixed(2)),
    contrastVsWorstGround_freshnessOff: Number(contrastRatio(gOff, worstIn(OFF)).toFixed(2)),
    marksCrossingIt: crossedBy.get(s.symbol) ?? 0,
  };
}
const labels = labelled.map(labelReport).filter(Boolean);

// ---- (c) ramp as rendered, on UNCROSSED marks only ------------------------
function sampleAnnulus(m, img) {
  const best = [];
  for (let a = 0; a < 360; a += 4) {
    const th = (a * Math.PI) / 180;
    let top = null;
    for (let dr = -0.42 * m.r; dr <= 0.02 * m.r; dr += 0.6) {
      const X = Math.round(m.sx + Math.cos(th) * (m.r + dr));
      const Y = Math.round(m.sy + Math.sin(th) * (m.r + dr));
      if (!inFrame(X, Y, 1)) continue;
      const p = px(img, X, Y), L = lum255(p);
      if (top == null || L > top.L) top = { p, L };
    }
    if (top) best.push(top);
  }
  if (!best.length) return null;
  best.sort((a, c) => c.L - a.L);
  const third = best.slice(0, Math.max(1, Math.floor(best.length / 3)));
  return third[Math.floor(third.length / 2)];
}
const clean = marks.filter((m) => (crossedBy.get(m.symbol) ?? 0) === 0 && inFrame(m.sx, m.sy, 60));
const perStep = [[], [], [], [], []];
const darkPeaks = [];
for (const m of clean) {
  const s = sampleAnnulus(m, ON);
  if (!s) continue;
  if (m.priced) perStep[m.step].push(s); else darkPeaks.push(s);
}
const median = (a, f) => { const v = a.map(f).sort((x, y) => x - y); return v.length ? v[Math.floor(v.length / 2)] : null; };
const agg = (list) => list.length ? {
  n: list.length,
  rgb: [0, 1, 2].map((i) => Math.round(median(list, (m) => m.p[i]))),
  peakLum: Number(median(list, (m) => m.L).toFixed(1)),
} : null;
const rampRendered = perStep.map(agg);
const darkRendered = agg(darkPeaks);

const report = {
  tag: TAG, knobs: K, fullScale,
  camera: { zoom: Number((scale / fitScale).toFixed(3)), scale, canvas, calibration: calibrated },
  a_density: {
    marksInFrame: marks.length,
    crossingPairs: crossPairs,
    pricedPricedPairs: pricedPairs,
    marksCrossedByAtLeastOne: [...crossedBy.keys()].length,
    hardEdgePixels: { freshnessOn: edgeOn.strongEdgePixels, freshnessOff: edgeOff.strongEdgePixels, addedByFreshness: edgeOn.strongEdgePixels - edgeOff.strongEdgePixels },
    gradientSum: { freshnessOn: edgeOn.gradientSum, freshnessOff: edgeOff.gradientSum, addedByFreshness: edgeOn.gradientSum - edgeOff.gradientSum },
  },
  b_labels: labels,
  c_ramp: {
    sampledOnUncrossedMarks: clean.filter((m) => m.priced).length,
    steps: rampRendered.map((r, i) => r && ({ step: i, ...r })),
    adjacentDeltaE: [0, 1, 2, 3].map((i) => {
      const a = rampRendered[i], c = rampRendered[i + 1];
      return a && c ? Number(deltaE2000(a.rgb, c.rgb).toFixed(2)) : null;
    }),
  },
  e_invariants: {
    darkPeakLum: darkRendered?.peakLum ?? null,
    darkRgb: darkRendered?.rgb ?? null,
    pricedPeakLumByStep: rampRendered.map((r) => r?.peakLum ?? null),
    quietestPricedVsDark: darkRendered && rampRendered[4]
      ? Number((rampRendered[4].peakLum / darkRendered.peakLum).toFixed(2)) : null,
  },
  shots: { on: onPath, off: offPath },
};
writeFileSync(`${OUT}/${TAG}-report.json`, JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
b.close();
process.exit(0);
