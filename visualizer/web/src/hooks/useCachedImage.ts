import { useEffect, useState } from 'react';

const imageCache = new Map<string, HTMLImageElement | null>();

export const useCachedImage = (src: string | null): HTMLImageElement | null => {
  const [image, setImage] = useState<HTMLImageElement | null>(() => {
    if (!src || typeof window === 'undefined') return null;
    const cached = imageCache.get(src);
    return cached ?? null;
  });

  useEffect(() => {
    if (!src || typeof window === 'undefined') {
      setImage(null);
      return;
    }

    const cached = imageCache.get(src);
    if (cached !== undefined) {
      setImage(cached);
      return;
    }

    let cancelled = false;
    const img = new Image();
    img.src = src;
    img.onload = () => {
      if (cancelled) return;
      imageCache.set(src, img);
      setImage(img);
    };
    img.onerror = () => {
      if (cancelled) return;
      imageCache.set(src, null);
      setImage(null);
    };

    return () => {
      cancelled = true;
    };
  }, [src]);

  return image;
};
