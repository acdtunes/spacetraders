import { describe, it, expect } from 'vitest';
import { deriveOpsState, FLEET_IDLE_AFTER_MS, type OpsStateInput } from '../opsState';

const input = (overrides: Partial<OpsStateInput> = {}): OpsStateInput => ({
  connectionOk: true,
  lastEventAgeMs: 0,
  anyShipInTransit: false,
  ...overrides,
});

describe('deriveOpsState', () => {
  it("returns 'lost' whenever the connection is down, regardless of other signals", () => {
    expect(deriveOpsState(input({ connectionOk: false }))).toBe('lost');
    // A ship in transit or fresh events cannot rescue a dark backend.
    expect(
      deriveOpsState(input({ connectionOk: false, anyShipInTransit: true }))
    ).toBe('lost');
    expect(
      deriveOpsState(
        input({ connectionOk: false, lastEventAgeMs: FLEET_IDLE_AFTER_MS + 10_000 })
      )
    ).toBe('lost');
  });

  it("returns 'live' while any ship is in transit, even past the idle window", () => {
    expect(deriveOpsState(input({ anyShipInTransit: true }))).toBe('live');
    expect(
      deriveOpsState(
        input({ anyShipInTransit: true, lastEventAgeMs: FLEET_IDLE_AFTER_MS + 60_000 })
      )
    ).toBe('live');
  });

  it("returns 'live' when connected and events are fresh (within the idle window)", () => {
    expect(deriveOpsState(input({ lastEventAgeMs: 0 }))).toBe('live');
    expect(
      deriveOpsState(input({ lastEventAgeMs: FLEET_IDLE_AFTER_MS - 1 }))
    ).toBe('live');
    // Exactly at the boundary is still live — idle requires strictly greater.
    expect(deriveOpsState(input({ lastEventAgeMs: FLEET_IDLE_AFTER_MS }))).toBe('live');
  });

  it("returns 'idle' when connected, nothing in transit, and events are stale past the window", () => {
    expect(
      deriveOpsState(input({ lastEventAgeMs: FLEET_IDLE_AFTER_MS + 1 }))
    ).toBe('idle');
    // No events ever seen (age Infinity) reads as idle, not live.
    expect(
      deriveOpsState(input({ lastEventAgeMs: Number.POSITIVE_INFINITY }))
    ).toBe('idle');
  });
});
