import { start } from './server.js';

start().catch((err) => {
  console.error('twin failed to start:', err);
  process.exit(1);
});
