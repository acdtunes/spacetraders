import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { CLI_BIN, GOBOT_DIR, REPO_ROOT, TWIN_ADMIN, TWIN_BASE_URL, TEST_DATABASE_URL } from '../helpers/run-cli';

describe('run-cli constants', () => {
  it('point at the canonical twin + gobot paths (foundation §5.1/§6)', () => {
    expect(TWIN_BASE_URL).toBe('http://127.0.0.1:8080/v2');
    expect(TWIN_ADMIN).toBe('http://127.0.0.1:8080/_twin');
    // Port-agnostic: the isolation signal is the DB NAME (spacetraders_test), never prod
    // (spacetraders). The host port is env-overridable (TWIN_TEST_DATABASE_URL); default :5434.
    expect(TEST_DATABASE_URL).toContain('/spacetraders_test');
    expect(TEST_DATABASE_URL).not.toContain('/spacetraders?');
    expect(GOBOT_DIR).toBe(path.join(REPO_ROOT, 'gobot'));
    expect(CLI_BIN).toBe(path.join(GOBOT_DIR, 'bin', 'spacetraders'));
  });
});
