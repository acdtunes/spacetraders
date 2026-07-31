// Layer scaffold for the nebula renderer. All draw content lives in named
// containers under a single `world` root; the camera transform (pan/zoom) is
// applied to `world` alone, so every layer inherits it for free. Later tasks
// attach content to these EXACT names — z-order is the array order below.
import { Container } from 'pixi.js';

export const LAYER_ORDER = [
  'backdrop',
  'auras',
  'currents',
  // Per-system market freshness (orbs.ts writes it; REGION band). It sits BELOW
  // `lanes` on purpose: freshness is context — how well we can see a market —
  // and the lanes and labels are the subject of this view. Carried above the
  // orbs it buried both (sp-9m0bd, Admiral-reported). It cannot live inside
  // `auras` either: that layer is GALAXY-only and galaxyBand clears it whole.
  'freshness',
  'lanes',
  'orbs',
  'ships',
  'labels',
  'fx',
] as const;

export type LayerName = (typeof LAYER_ORDER)[number];

export type Layers = Record<LayerName, Container> & {
  /** Camera-transformed root; every named layer is a child of this. */
  world: Container;
};

/**
 * Pointer callbacks the interactive layers (orbs, galaxyBand) forward to the
 * scene. Lives here — not in NebulaScene — so layer modules never import the
 * component. Builders take this as an optional param; absent (build tests,
 * screenshots) they attach no handlers.
 */
export interface PointerHooks {
  hover(kind: 'system' | 'lane' | 'current', key: string, at: { clientX: number; clientY: number }): void;
  hoverOut(): void;
  tapSystem(symbol: string): void;
}

export function createLayers(stage: Container): Layers {
  const world = new Container();
  world.label = 'world';
  stage.addChild(world);
  const named = {} as Record<LayerName, Container>;
  for (const name of LAYER_ORDER) {
    const layer = new Container();
    layer.label = name;
    world.addChild(layer);
    named[name] = layer;
  }
  return { ...named, world };
}
