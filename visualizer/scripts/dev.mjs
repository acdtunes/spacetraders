#!/usr/bin/env node

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const rootDir = resolve(__dirname, '..')
const serverDir = resolve(rootDir, 'server')
const webDir = resolve(rootDir, 'web')

const DEFAULTS = {
  frontendPort: 5173,
  backendPort: 4000,
  backendHost: 'localhost',
  backendProtocol: 'http',
  frontendHost: 'localhost',
}

const npmCommand = process.platform === 'win32' ? 'npm.cmd' : 'npm'

const processes = []
let shuttingDown = false

function printHelp() {
  console.log(`Usage: node scripts/dev.mjs [options]

Options:
  -f, --frontend-port <port>   Port for the frontend dev server (default: ${DEFAULTS.frontendPort})
  -b, --backend-port <port>    Port for the backend server (default: ${DEFAULTS.backendPort})
      --frontend-host <host>   Host for the frontend dev server (default: ${DEFAULTS.frontendHost})
      --backend-host <host>    Hostname used in the frontend proxy target (default: ${DEFAULTS.backendHost})
      --backend-protocol <p>   Protocol for the frontend proxy target (default: ${DEFAULTS.backendProtocol})
  -h, --help                   Show this help message

Environment variables FRONTEND_PORT, FRONTEND_HOST, BACKEND_PORT, BACKEND_HOST, BACKEND_PROTOCOL are also respected.
`)
}

function parsePort(value, fallback) {
  if (value === undefined || value === null || value === '') {
    return fallback
  }
  const parsed = Number.parseInt(String(value), 10)
  if (!Number.isInteger(parsed) || parsed <= 0 || parsed > 65535) {
    throw new Error(`Invalid port value: ${value}`)
  }
  return parsed
}

function parseArgs() {
  const args = process.argv.slice(2)
  const config = {
    frontendPort: parsePort(process.env.FRONTEND_PORT, DEFAULTS.frontendPort),
    backendPort: parsePort(process.env.BACKEND_PORT, DEFAULTS.backendPort),
    backendHost: process.env.BACKEND_HOST || DEFAULTS.backendHost,
    backendProtocol: process.env.BACKEND_PROTOCOL || DEFAULTS.backendProtocol,
    frontendHost: process.env.FRONTEND_HOST || DEFAULTS.frontendHost,
  }

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]
    switch (arg) {
      case '-f':
      case '--frontend-port':
        i += 1
        if (args[i] === undefined) throw new Error('Missing value for --frontend-port')
        config.frontendPort = parsePort(args[i], config.frontendPort)
        break
      case '-b':
      case '--backend-port':
        i += 1
        if (args[i] === undefined) throw new Error('Missing value for --backend-port')
        config.backendPort = parsePort(args[i], config.backendPort)
        break
      case '--frontend-host':
        i += 1
        if (args[i] === undefined) throw new Error('Missing value for --frontend-host')
        config.frontendHost = args[i]
        break
      case '--backend-host':
        i += 1
        if (args[i] === undefined) throw new Error('Missing value for --backend-host')
        config.backendHost = args[i]
        break
      case '--backend-protocol':
        i += 1
        if (args[i] === undefined) throw new Error('Missing value for --backend-protocol')
        config.backendProtocol = args[i]
        break
      case '-h':
      case '--help':
        printHelp()
        process.exit(0)
        break
      default:
        if (arg.startsWith('-')) {
          throw new Error(`Unknown option: ${arg}`)
        }
    }
  }

  const allowedProtocols = new Set(['http', 'https'])
  if (!allowedProtocols.has(config.backendProtocol)) {
    throw new Error(`Unsupported backend protocol: ${config.backendProtocol}`)
  }

  return config
}

function log(message) {
  console.log(`[dev] ${message}`)
}

function terminateChildren(signal = 'SIGTERM') {
  processes.forEach(({ child }) => {
    if (!child.killed) {
      child.kill(signal)
    }
  })
}

function shutdown({ code = 0, signal = 'SIGTERM' } = {}) {
  if (shuttingDown) return
  shuttingDown = true
  terminateChildren(signal)
  process.exitCode = code
}

function monitorProcess(name, child) {
  child.on('exit', (code, signal) => {
    if (shuttingDown) return
    if (signal) {
      return
    }
    if (code === 0) {
      log(`${name} process exited`)
      shutdown({ code: 0, signal: 'SIGTERM' })
    } else {
      console.error(`[dev] ${name} process exited with code ${code}`)
      shutdown({ code: code ?? 1, signal: 'SIGTERM' })
    }
  })
}

async function runCommand(name, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(npmCommand, args, {
      cwd: options.cwd,
      env: { ...process.env, ...options.env },
      stdio: 'inherit',
    })

    child.on('exit', (code, signal) => {
      if (signal || code !== 0) {
        reject(new Error(`${name} failed`))
      } else {
        resolve()
      }
    })
  })
}

async function main() {
  let config
  try {
    config = parseArgs()
  } catch (error) {
    console.error(`[dev] ${error.message}`)
    printHelp()
    process.exitCode = 1
    return
  }

  log(`Backend: ${config.backendProtocol}://${config.backendHost}:${config.backendPort}`)
  log(`Frontend: http://${config.frontendHost}:${config.frontendPort}`)

  try {
    log('Building backend...')
    await runCommand('backend build', ['run', 'build'], { cwd: serverDir })
  } catch (error) {
    console.error(`[dev] ${error.message}`)
    process.exitCode = 1
    return
  }

  const backendEnv = { PORT: String(config.backendPort) }
  const frontendEnv = {
    FRONTEND_PORT: String(config.frontendPort),
    FRONTEND_HOST: config.frontendHost,
    BACKEND_PORT: String(config.backendPort),
    BACKEND_HOST: config.backendHost,
    BACKEND_PROTOCOL: config.backendProtocol,
  }

  const backend = spawn(npmCommand, ['run', 'dev'], {
    cwd: serverDir,
    env: { ...process.env, ...backendEnv },
    stdio: 'inherit',
  })
  processes.push({ name: 'backend', child: backend })
  monitorProcess('backend', backend)

  const frontend = spawn(npmCommand, ['run', 'dev'], {
    cwd: webDir,
    env: { ...process.env, ...frontendEnv },
    stdio: 'inherit',
  })
  processes.push({ name: 'frontend', child: frontend })
  monitorProcess('frontend', frontend)

  log('Both servers are running. Press Ctrl+C to stop.')
}

process.on('SIGINT', () => {
  log('Received SIGINT, shutting down...')
  shutdown({ code: 130, signal: 'SIGINT' })
})

process.on('SIGTERM', () => {
  log('Received SIGTERM, shutting down...')
  shutdown({ code: 143, signal: 'SIGTERM' })
})

main().catch((error) => {
  console.error(`[dev] ${error.message}`)
  process.exitCode = 1
})
