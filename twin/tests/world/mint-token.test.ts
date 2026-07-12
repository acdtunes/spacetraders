import { describe, expect, it } from 'vitest';
import { mintToken } from '../../src/world/loader';

describe('mintToken', () => {
  it('mints the exact deterministic token for TWINAGENT', () => {
    expect(mintToken('TWINAGENT')).toBe(
      'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9' +
        '.eyJpZGVudGlmaWVyIjoiVFdJTkFHRU5UIiwidmVyc2lvbiI6InR3aW4ifQ' +
        '.dHdpbi1zaWduYXR1cmUuVFdJTkFHRU5U',
    );
  });
  it('is JWT-shaped: three non-empty base64url segments', () => {
    const parts = mintToken('TWINAGENT').split('.');
    expect(parts).toHaveLength(3);
    for (const p of parts) { expect(p.length).toBeGreaterThan(0); expect(p).toMatch(/^[A-Za-z0-9_-]+$/); }
  });
  it('is deterministic per symbol and distinct across symbols', () => {
    expect(mintToken('TWINAGENT')).toBe(mintToken('TWINAGENT'));
    expect(mintToken('TWINAGENT')).not.toBe(mintToken('OTHERAGENT'));
  });
  it('encodes the agent symbol as the payload identifier', () => {
    const payload = mintToken('TWINAGENT').split('.')[1];
    const json = Buffer.from(payload.replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8');
    expect(JSON.parse(json)).toEqual({ identifier: 'TWINAGENT', version: 'twin' });
  });
});
