// Live-map effects for Contract Ops: diff two consecutive /live polls into
// transient FX (ripples/bursts) anchored to waypoints. Pure, so the moment
// detection is unit-testable; the scene only animates what this emits.

import type { ContractOpsLive } from '../types/contractOps';

export type OpsFxKind = 'stocking' | 'withdrawal' | 'delivery' | 'fulfillment';

export interface OpsFx {
  id: string;
  kind: OpsFxKind;
  waypoint: string | null;
  text: string;
  createdAtMs: number;
}

const eventKey = (e: ContractOpsLive['events'][number]) =>
  `${e.kind}|${e.at}|${e.good ?? ''}|${e.units ?? ''}|${e.shipSymbol ?? ''}|${e.amount ?? ''}`;

export function diffLiveForFx(
  prev: ContractOpsLive | null,
  next: ContractOpsLive,
  nowMs: number,
): OpsFx[] {
  if (!prev) return []; // first poll is history, not news
  const fx: OpsFx[] = [];

  // Warehouse traffic: any stocking/withdrawal event we have not seen yet.
  const seen = new Set(prev.events.map(eventKey));
  for (const e of next.events) {
    const key = eventKey(e);
    if (seen.has(key)) continue;
    if (e.kind === 'stocking') {
      fx.push({ id: key, kind: 'stocking', waypoint: e.waypoint ?? null, text: `+${e.units} ${e.good}`, createdAtMs: nowMs });
    } else if (e.kind === 'withdrawal') {
      fx.push({ id: key, kind: 'withdrawal', waypoint: e.waypoint ?? null, text: `−${e.units} ${e.good}`, createdAtMs: nowMs });
    }
  }

  // Delivery progress on the same active contract.
  if (prev.contract && next.contract && prev.contract.id === next.contract.id) {
    for (const d of next.contract.deliveries) {
      const before = prev.contract.deliveries.find(
        (x) => x.tradeSymbol === d.tradeSymbol && x.destinationSymbol === d.destinationSymbol,
      );
      if (before && d.unitsFulfilled > before.unitsFulfilled) {
        fx.push({
          id: `dlv|${next.contract.id}|${d.tradeSymbol}|${d.unitsFulfilled}`,
          kind: 'delivery',
          waypoint: d.destinationSymbol,
          text: `▲ +${d.unitsFulfilled - before.unitsFulfilled} ${d.tradeSymbol} delivered`,
          createdAtMs: nowMs,
        });
      }
    }
  }

  // The active contract completed (its id now shows as lastFulfilled).
  if (
    prev.contract &&
    (!next.contract || next.contract.id !== prev.contract.id) &&
    next.lastFulfilled?.id === prev.contract.id
  ) {
    fx.push({
      id: `ful|${prev.contract.id}`,
      kind: 'fulfillment',
      waypoint: prev.contract.deliveries[0]?.destinationSymbol ?? null,
      text: `◆ CONTRACT FULFILLED +${next.lastFulfilled.payment.toLocaleString()} cr`,
      createdAtMs: nowMs,
    });
  }

  return fx;
}
