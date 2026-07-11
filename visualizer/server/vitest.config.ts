import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['routes/**/*.test.ts', 'utils/**/*.test.ts'],
  },
});
