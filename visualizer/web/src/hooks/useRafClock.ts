import { useEffect, useState } from 'react';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

// A single animation clock for the Trade Flows tab: returns Date.now() advanced
// every animation frame, so hull interpolation and marching lane dashes glide
// smoothly BETWEEN the 5s data polls instead of jumping once per poll. Honors
// prefers-reduced-motion by falling back to a 1s tick — positions still track the
// feed, but nothing animates per frame. One clock feeds both the galaxy and the
// drilldown so the whole tab shares a consistent "now".
export function useRafClock(): number {
  const reduced = usePrefersReducedMotion();
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    if (reduced || typeof requestAnimationFrame !== 'function') {
      const id = setInterval(() => setNowMs(Date.now()), 1000);
      return () => clearInterval(id);
    }
    let raf = 0;
    const tick = () => {
      setNowMs(Date.now());
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [reduced]);

  return nowMs;
}
