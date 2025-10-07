import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')

  const resolvePort = (value: string | undefined, fallback: number) => {
    const parsed = Number.parseInt(value ?? '', 10)
    return Number.isFinite(parsed) ? parsed : fallback
  }

  const frontendPort = resolvePort(env.FRONTEND_PORT, 5173)
  const frontendHost = env.FRONTEND_HOST || 'localhost'
  const backendPort = resolvePort(env.BACKEND_PORT, 4000)
  const backendHost = env.BACKEND_HOST || 'localhost'
  const backendProtocol = env.BACKEND_PROTOCOL || 'http'

  return {
    plugins: [react()],
    server: {
      host: frontendHost,
      port: frontendPort,
      proxy: {
        '/api': {
          target: `${backendProtocol}://${backendHost}:${backendPort}`,
          changeOrigin: true,
        },
      },
    },
  }
})
