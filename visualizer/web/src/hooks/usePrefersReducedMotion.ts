import { useEffect, useState } from 'react';

const QUERY = '(prefers-reduced-motion: reduce)';

const getInitial = (): boolean =>
  typeof window !== 'undefined' &&
  typeof window.matchMedia === 'function' &&
  window.matchMedia(QUERY).matches;

/**
 * Tracks the viewer's `prefers-reduced-motion` setting so canvas animations —
 * which a CSS media query cannot reach — can pin themselves to a steady state.
 * Falls back to "no reduction" where matchMedia is unavailable (SSR / jsdom).
 */
export const usePrefersReducedMotion = (): boolean => {
  const [reduced, setReduced] = useState<boolean>(getInitial);

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return;
    }
    const mql = window.matchMedia(QUERY);
    const onChange = () => setReduced(mql.matches);
    onChange();
    mql.addEventListener('change', onChange);
    return () => mql.removeEventListener('change', onChange);
  }, []);

  return reduced;
};
