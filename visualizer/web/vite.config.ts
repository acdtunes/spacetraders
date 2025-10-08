import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => {
  const env = mode === 'test' ? process.env : undefined;

  return {
    plugins: [react()],
    test: {
      environment: 'jsdom',
      setupFiles: ['./vitest.setup.ts'],
      pool: 'threads',
      poolOptions: {
        threads: {
          minThreads: 1,
          maxThreads: 1,
        },
      },
      maxConcurrency: 1,
      exclude: [
        'dist/**',
        'node_modules/**',
        'src/mocks/**',
      ],
    },
  };
});
