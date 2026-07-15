import { create } from 'zustand';
import type { ContractOpsTopology, ContractOpsLive } from '../types/contractOps';
import { updateTransitMemory, type ShipMemory } from '../utils/transitMemory';
import { diffLiveForFx, type OpsFx } from '../utils/opsFx';

const FX_TTL_MS = 6000;

export const CONTRACT_OPS_PASSES = ['CONTRACT', 'TOPOLOGY', 'FLEET', 'FLOW'] as const;
const PASS_STORAGE_KEY = 'contractOps.pass';

function initialPass(): number {
  if (typeof window === 'undefined') return 3;
  const fromUrl = new URLSearchParams(window.location.search).get('pass');
  const candidate = fromUrl ?? window.localStorage.getItem(PASS_STORAGE_KEY);
  const n = candidate == null ? NaN : Number(candidate);
  return Number.isInteger(n) && n >= 0 && n <= 3 ? n : 3;
}

export interface ContractOpsState {
  topology: ContractOpsTopology | null;
  live: ContractOpsLive | null;
  // Client-side flight reconstruction — see utils/transitMemory.ts.
  memory: Map<string, ShipMemory>;
  // Transient map effects (ripples/bursts) minted by diffing consecutive polls.
  fx: OpsFx[];
  pass: number; // 0..3 — layers accrete, deck-style
  selectedShip: string | null;
  follow: boolean; // camera tracks the worker ship
  error: string | null;

  setTopology: (t: ContractOpsTopology) => void;
  setLive: (l: ContractOpsLive) => void;
  setPass: (pass: number) => void;
  selectShip: (symbol: string | null) => void;
  setFollow: (follow: boolean) => void;
  setError: (message: string | null) => void;
}

export const useContractOpsStore = create<ContractOpsState>((set) => ({
  topology: null,
  live: null,
  memory: new Map(),
  fx: [],
  pass: initialPass(),
  selectedShip: null,
  follow: false,
  error: null,

  setTopology: (topology) => set({ topology, error: null }),
  setLive: (live) =>
    set((state) => {
      const nowMs = Date.now();
      return {
        live,
        error: null,
        memory: updateTransitMemory(
          state.memory,
          live.ships.map((s) => ({
            symbol: s.symbol,
            navStatus: s.navStatus,
            waypoint: s.waypoint,
            x: s.x,
            y: s.y,
            arrivalTime: s.arrivalTime,
          })),
          nowMs,
        ),
        fx: [
          ...state.fx.filter((f) => nowMs - f.createdAtMs < FX_TTL_MS),
          ...diffLiveForFx(state.live, live, nowMs),
        ].slice(-40),
      };
    }),
  setPass: (pass) => {
    const clamped = Math.min(3, Math.max(0, Math.round(pass)));
    if (typeof window !== 'undefined') window.localStorage.setItem(PASS_STORAGE_KEY, String(clamped));
    set({ pass: clamped });
  },
  selectShip: (selectedShip) => set({ selectedShip }),
  setFollow: (follow) => set({ follow }),
  setError: (error) => set({ error }),
}));

// Dev-only debugging affordance (mirrors flowStore): drive the tab from the
// console / e2e. Stripped from production builds.
if (typeof window !== 'undefined' && import.meta.env.DEV) {
  (window as unknown as { __contractOpsStore?: typeof useContractOpsStore }).__contractOpsStore =
    useContractOpsStore;
}
