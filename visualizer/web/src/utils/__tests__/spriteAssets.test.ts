import { describe, it, expect } from 'vitest';
import { selectWaypointAsset, selectShipAssetByRole, waypointVisualRadius } from '../spriteAssets';

describe('selectWaypointAsset', () => {
  it('maps trait keywords to biome variants and is deterministic per symbol', () => {
    const a = selectWaypointAsset('X1-VB74-A1', 'PLANET', ['TEMPERATE', 'MARKETPLACE']);
    expect(a).toMatch(/waypoint-planet-temperate-\d\.png$/);
    expect(selectWaypointAsset('X1-VB74-A1', 'PLANET', ['TEMPERATE', 'MARKETPLACE'])).toBe(a);
  });

  it('accepts both plain-string traits (DB shape) and {symbol} traits (API shape)', () => {
    const fromStrings = selectWaypointAsset('W', 'PLANET', ['OCEAN']);
    const fromObjects = selectWaypointAsset('W', 'PLANET', [{ symbol: 'OCEAN' }]);
    expect(fromStrings).toBe(fromObjects);
    expect(fromStrings).toMatch(/planet-ocean/);
  });

  it('resolves types without traits', () => {
    expect(selectWaypointAsset('W', 'JUMP_GATE', [])).toMatch(/jumpgate/);
    expect(selectWaypointAsset('W', 'ASTEROID', [])).toMatch(/waypoint-asteroid-\d/);
    expect(selectWaypointAsset('W', 'GAS_GIANT', [])).toMatch(/jovian/);
    expect(selectWaypointAsset('W', 'MOON', [])).toMatch(/rocky/);
  });
});

describe('selectShipAssetByRole', () => {
  it('maps registration roles to hull art', () => {
    expect(selectShipAssetByRole('S', 'HAULER')).toMatch(/light-hauler/);
    expect(selectShipAssetByRole('S', 'COMMAND')).toMatch(/command-frigate/);
    expect(selectShipAssetByRole('S', 'EXCAVATOR')).toMatch(/mining-drone/);
    expect(selectShipAssetByRole('S', undefined)).toMatch(/command-frigate/);
  });
});

describe('waypointVisualRadius', () => {
  it('sizes the celestial hierarchy sensibly', () => {
    expect(waypointVisualRadius('GAS_GIANT')).toBeGreaterThan(waypointVisualRadius('PLANET'));
    expect(waypointVisualRadius('PLANET')).toBeGreaterThan(waypointVisualRadius('MOON'));
    expect(waypointVisualRadius('MOON')).toBeGreaterThan(waypointVisualRadius('ASTEROID'));
  });
});
