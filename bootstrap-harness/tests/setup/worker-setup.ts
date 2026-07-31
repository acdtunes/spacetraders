// Per-fork setupFile (st-drm.22). vitest re-runs this for every test file, but the start-once
// guard lives in the imported worker-stack module (evaluated once per fork), so the twin boots a
// single time per worker and later files just await the already-resolved promise. No-op in
// single-stack mode (WORKER_ID === null).
import { ensureWorkerStackReady } from '../helpers/worker-stack';

await ensureWorkerStackReady();
