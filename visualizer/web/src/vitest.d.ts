// Brings the @testing-library/jest-dom matcher augmentation (`toBeInTheDocument`,
// `toBeEmptyDOMElement`, …) into the tsc program. The runtime registration lives
// in vitest.setup.ts, but that file sits outside the tsconfig `include: ["src"]`,
// so the `declare module 'vitest'` augmentation must be referenced from inside src.
import '@testing-library/jest-dom/vitest';
