import net from 'node:net';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../src/server.js';
import { runCli, TWIN_BASE_URL } from './helpers/run-cli.js';
import { startTestDaemon, stopTestDaemon } from './helpers/daemon.js';

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function tcpOpen(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ host, port }, () => { sock.destroy(); resolve(true); });
    sock.on('error', () => { sock.destroy(); resolve(false); });
    sock.setTimeout(500, () => { sock.destroy(); resolve(false); });
  });
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  // 1. Boot the twin in-process (fixtures loaded lazily by the store).
  const app: FastifyInstance = buildServer();
  await app.listen({ port: 8080, host: '127.0.0.1' });
  {
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      try { if ((await fetch(`${TWIN_BASE_URL}/`)).status === 200) break; } catch { /* not ready */ }
      await sleep(200);
    }
  }

  // 2. Ensure the test Postgres is reachable on :5433 (fail fast with a hint).
  if (!(await tcpOpen('localhost', 5433))) {
    await app.close();
    throw new Error('test Postgres not reachable on localhost:5433 — start it first: docker compose -f twin/docker-compose.test.yml up -d postgres-test');
  }

  // 3. Boot the isolated test daemon (AutoMigrate on first boot).
  await startTestDaemon();

  // 4. Seed TWINAGENT through the hardened CLI (idempotent: skip if an OPEN era exists).
  const reg = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
  if (reg.exitCode !== 0 && !/an OPEN era/.test(`${reg.stdout}\n${reg.stderr}`)) {
    await stopTestDaemon(); await app.close();
    throw new Error(`globalSetup seed failed (exit ${reg.exitCode}): ${reg.stderr}`);
  }

  // Teardown.
  return async () => {
    await stopTestDaemon();
    await app.close();
  };
}
