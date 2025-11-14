import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => {
  const env = mode === 'test' ? process.env : undefined;

  const backendProtocol = process.env.BACKEND_PROTOCOL || 'http';
  const backendHost = process.env.BACKEND_HOST || 'localhost';
  const backendPort = process.env.BACKEND_PORT || '4000';
  const frontendPort = parseInt(process.env.FRONTEND_PORT || '5173', 10);
  const frontendHost = process.env.FRONTEND_HOST || 'localhost';

  const backendTarget = `${backendProtocol}://${backendHost}:${backendPort}`;

  return {
    plugins: [react()],
    server: {
      port: frontendPort,
      host: frontendHost,
      proxy: {
        '/api': {
          target: backendTarget,
          changeOrigin: true,
          secure: false,
        },
      },
    },
    preview: {
      proxy: {
        '/api': {
          target: backendTarget,
          changeOrigin: true,
          secure: false,
        },
      },
    },
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
