// Colour maths for the freshness-encoding tests: composite a mark the way the
// renderer does, then measure perceptual distance.
//
// WHY THIS EXISTS. Two freshness encodings have now shipped defects that their
// unit tests could not see, both for the same reason: the tests compared hex
// literals, and a hex literal is not what a reader looks at. A mark is a tint
// at an alpha over a ground, and two different hexes can composite to the same
// pixel (sp-voyz7 shipped a ramp whose head WAS the orb halo's tint) while two
// hexes that differ by a comfortable RGB distance can still be indistinguishable
// on screen. Everything here takes the composite, not the constant.
//
// ΔE is CIEDE2000. ~2.3 is the just-noticeable difference for adjacent large
// uniform fields; small marks need considerably more, so the thresholds in the
// tests sit well above it.

/** 0xRRGGBB → [r,g,b]. */
export const rgb = (hex: number): [number, number, number] => [
  (hex >> 16) & 0xff,
  (hex >> 8) & 0xff,
  hex & 0xff,
];

const clamp255 = (v: number) => Math.max(0, Math.min(255, Math.round(v)));
const srgbToLinear = (v: number) => {
  const s = v / 255;
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
};
const linearToSrgb = (v: number) =>
  clamp255(255 * (v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055));

type RGB = [number, number, number];
const asRgb = (c: number | RGB): RGB => (typeof c === 'number' ? rgb(c) : c);

/** pixi blendMode 'normal': src at `alpha` over dst. */
export function normalOver(src: number | RGB, dst: number | RGB, alpha: number): RGB {
  const s = asRgb(src), d = asRgb(dst);
  return [0, 1, 2].map((i) => clamp255(s[i] * alpha + d[i] * (1 - alpha))) as RGB;
}

/** pixi blendMode 'screen': 1 - (1-dst)(1-src·alpha). */
export function screenOver(src: number | RGB, dst: number | RGB, alpha: number): RGB {
  const s = asRgb(src), d = asRgb(dst);
  return [0, 1, 2].map((i) =>
    clamp255((1 - (1 - d[i] / 255) * (1 - (s[i] / 255) * alpha)) * 255),
  ) as RGB;
}

export const relativeLuminance = (c: number | RGB): number => {
  const [r, g, b] = asRgb(c);
  return 0.2126 * srgbToLinear(r) + 0.7152 * srgbToLinear(g) + 0.0722 * srgbToLinear(b);
};

/** WCAG contrast ratio. */
export function contrastRatio(a: number | RGB, b: number | RGB): number {
  const l1 = relativeLuminance(a), l2 = relativeLuminance(b);
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1];
  return (hi + 0.05) / (lo + 0.05);
}

/** CIE L*a*b* (D65). */
export function rgbToLab(c: number | RGB): [number, number, number] {
  const [r, g, b] = asRgb(c).map(srgbToLinear);
  const X = (r * 0.4124564 + g * 0.3575761 + b * 0.1804375) / 0.95047;
  const Y = r * 0.2126729 + g * 0.7151522 + b * 0.0721750;
  const Z = (r * 0.0193339 + g * 0.1191920 + b * 0.9503041) / 1.08883;
  const f = (t: number) => (t > 216 / 24389 ? Math.cbrt(t) : (841 / 108) * t + 4 / 29);
  const fx = f(X), fy = f(Y), fz = f(Z);
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)];
}

const deg = (rad: number) => (rad * 180) / Math.PI;
const rad = (d: number) => (d * Math.PI) / 180;

/** CIEDE2000 colour difference. */
export function deltaE2000(c1: number | RGB, c2: number | RGB): number {
  const [L1, a1, b1] = rgbToLab(c1);
  const [L2, a2, b2] = rgbToLab(c2);
  const C1 = Math.hypot(a1, b1), C2 = Math.hypot(a2, b2);
  const Cb = (C1 + C2) / 2;
  const G = 0.5 * (1 - Math.sqrt(Cb ** 7 / (Cb ** 7 + 25 ** 7)));
  const a1p = (1 + G) * a1, a2p = (1 + G) * a2;
  const C1p = Math.hypot(a1p, b1), C2p = Math.hypot(a2p, b2);
  const hue = (x: number, y: number) => {
    if (x === 0 && y === 0) return 0;
    const d = deg(Math.atan2(y, x));
    return d >= 0 ? d : d + 360;
  };
  const h1p = hue(a1p, b1), h2p = hue(a2p, b2);
  const dLp = L2 - L1, dCp = C2p - C1p;
  let dhp = 0;
  if (C1p * C2p !== 0) {
    dhp = h2p - h1p;
    if (dhp > 180) dhp -= 360;
    else if (dhp < -180) dhp += 360;
  }
  const dHp = 2 * Math.sqrt(C1p * C2p) * Math.sin(rad(dhp) / 2);
  const Lbp = (L1 + L2) / 2, Cbp = (C1p + C2p) / 2;
  let hbp: number;
  if (C1p * C2p === 0) hbp = h1p + h2p;
  else {
    hbp = Math.abs(h1p - h2p) > 180 ? (h1p + h2p + 360) / 2 : (h1p + h2p) / 2;
    if (hbp >= 360) hbp -= 360;
  }
  const T =
    1 - 0.17 * Math.cos(rad(hbp - 30)) + 0.24 * Math.cos(rad(2 * hbp))
    + 0.32 * Math.cos(rad(3 * hbp + 6)) - 0.20 * Math.cos(rad(4 * hbp - 63));
  const dTheta = 30 * Math.exp(-(((hbp - 275) / 25) ** 2));
  const Rc = 2 * Math.sqrt(Cbp ** 7 / (Cbp ** 7 + 25 ** 7));
  const Sl = 1 + (0.015 * (Lbp - 50) ** 2) / Math.sqrt(20 + (Lbp - 50) ** 2);
  const Sc = 1 + 0.045 * Cbp;
  const Sh = 1 + 0.015 * Cbp * T;
  const Rt = -Math.sin(rad(2 * dTheta)) * Rc;
  return Math.sqrt(
    (dLp / Sl) ** 2 + (dCp / Sc) ** 2 + (dHp / Sh) ** 2 + Rt * (dCp / Sc) * (dHp / Sh),
  );
}

/** Dichromat simulation (Viénot, Brettel & Mollon 1999) in LMS. */
export function simulateCvd(
  c: number | RGB,
  kind: 'deuteranopia' | 'protanopia' | 'tritanopia',
): RGB {
  const [r, g, b] = asRgb(c).map(srgbToLinear);
  const L = 0.31399022 * r + 0.63951294 * g + 0.04649755 * b;
  const M = 0.15537241 * r + 0.75789446 * g + 0.08670142 * b;
  const S = 0.01775239 * r + 0.10944209 * g + 0.87256922 * b;
  let L2 = L, M2 = M, S2 = S;
  if (kind === 'protanopia') L2 = 2.02344 * M - 2.52581 * S;
  if (kind === 'deuteranopia') M2 = 0.494207 * L + 1.24827 * S;
  if (kind === 'tritanopia') S2 = -0.395913 * L + 0.801109 * M;
  return [
    linearToSrgb(5.47221206 * L2 - 4.6419601 * M2 + 0.16963708 * S2),
    linearToSrgb(-1.1252419 * L2 + 2.29317094 * M2 - 0.1678952 * S2),
    linearToSrgb(0.02980165 * L2 - 0.19318073 * M2 + 1.16364789 * S2),
  ];
}
