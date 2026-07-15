import { useEffect, useRef, useState } from 'react';
import { NOIR, noirAlpha } from '../../theme/noir';
import type { ContractOpsLive, ContractPhase } from '../../types/contractOps';

const PHASES: ContractPhase[] = ['NEGOTIATE', 'ACCEPT', 'SOURCE', 'DELIVER', 'FULFILL'];

const cr = (n: number) => `${n.toLocaleString()} cr`;

// Ease a displayed number toward its target so money movements read as motion,
// not teleportation.
function useAnimatedNumber(target: number | null): number | null {
  const [display, setDisplay] = useState<number | null>(target);
  const fromRef = useRef<number | null>(target);
  useEffect(() => {
    const from = fromRef.current;
    if (target == null || from == null || from === target) {
      fromRef.current = target;
      setDisplay(target);
      return;
    }
    const t0 = performance.now();
    const DUR = 450;
    let raf = 0;
    const tick = (t: number) => {
      const u = Math.min(1, (t - t0) / DUR);
      setDisplay(Math.round(from + (target - from) * (1 - Math.pow(1 - u, 3))));
      if (u < 1) raf = requestAnimationFrame(tick);
      else fromRef.current = target;
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [target]);
  return display;
}

// Contracts fulfilled per hour over the trailing six — oldest on the left.
function ThroughputBars({ fulfillments, nowMs }: { fulfillments: string[]; nowMs: number }) {
  const buckets = new Array(6).fill(0) as number[];
  for (const iso of fulfillments) {
    const ageH = (nowMs - Date.parse(iso)) / 3_600_000;
    if (ageH >= 0 && ageH < 6) buckets[5 - Math.floor(ageH)] += 1;
  }
  const max = Math.max(1, ...buckets);
  return (
    <div className="flex items-end gap-1 h-6">
      {buckets.map((n, i) => (
        <div
          key={i}
          className="flex-1 rounded-sm"
          title={`${n} fulfilled · ${5 - i}–${6 - i}h ago`}
          style={{
            height: `${n > 0 ? Math.max(18, (n / max) * 100) : 8}%`,
            background: n > 0 ? noirAlpha(NOIR.accent, 0.45 + 0.55 * (n / max)) : noirAlpha(NOIR.dim, 0.25),
          }}
        />
      ))}
    </div>
  );
}

function heartbeatColor(heartbeatAt: string | undefined, nowMs: number): string {
  if (!heartbeatAt) return NOIR.dim;
  const age = (nowMs - Date.parse(heartbeatAt)) / 1000;
  if (age < 60) return NOIR.good;
  if (age < 180) return NOIR.warn;
  return NOIR.bad;
}

function relTime(iso: string, nowMs: number): string {
  const s = Math.max(0, Math.floor((nowMs - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}

export function ContractCard({ live, nowMs }: { live: ContractOpsLive | null; nowMs: number }) {
  if (!live) return null;
  const { contract, phase, cycle, worker, coordinator, pl, lastFulfilled } = live;
  const activeIdx = PHASES.indexOf(phase as (typeof PHASES)[number]);
  const projected =
    contract && pl ? contract.paymentOnFulfilled + pl.revenue - pl.cost : null;
  const projectedDisplay = useAnimatedNumber(projected);

  return (
    <div
      className="absolute top-4 left-4 rounded-lg p-4 w-[340px] max-w-[calc(100vw-2rem)]"
      style={{ background: noirAlpha(NOIR.panel, 0.94), border: `1px solid ${noirAlpha(NOIR.dim, 0.25)}` }}
    >
      <div className="flex items-center justify-between mb-3">
        <span className="text-[11px] tracking-[0.2em] font-mono" style={{ color: NOIR.muted }}>
          CONTRACT LOOP
        </span>
        <span className="flex items-center gap-3 text-[11px] font-mono" style={{ color: NOIR.muted }}>
          <span className="flex items-center gap-1" title={`coordinator heartbeat ${coordinator?.heartbeatAt ?? '—'}`}>
            <i className="w-2 h-2 rounded-full inline-block" style={{ background: heartbeatColor(coordinator?.heartbeatAt, nowMs) }} />
            COORD
          </span>
          <span className="flex items-center gap-1" title={`worker heartbeat ${worker?.heartbeatAt ?? '—'}`}>
            <i className="w-2 h-2 rounded-full inline-block" style={{ background: heartbeatColor(worker?.heartbeatAt, nowMs) }} />
            WORKER
          </span>
        </span>
      </div>

      {/* Phase strip — the loop's live pulse (the deadline countdown was
          deliberately rejected: this loop never runs near its deadlines). */}
      <div className="flex items-center gap-1 mb-3">
        {PHASES.map((p, i) => {
          const state = activeIdx < 0 ? 'idle' : i < activeIdx ? 'done' : i === activeIdx ? 'active' : 'future';
          return (
            <div key={p} className="flex-1">
              <div
                className={`h-1.5 rounded-full ${state === 'active' ? 'animate-pulse' : ''}`}
                style={{
                  background:
                    state === 'active' ? NOIR.accent : state === 'done' ? noirAlpha(NOIR.accent, 0.35) : noirAlpha(NOIR.dim, 0.3),
                }}
              />
              <div
                className="text-[8px] font-mono mt-1 tracking-wide text-center"
                style={{ color: state === 'active' ? NOIR.accent : NOIR.dim }}
              >
                {p}
              </div>
            </div>
          );
        })}
      </div>

      {contract ? (
        <>
          {contract.deliveries.map((d) => {
            const frac = d.unitsRequired > 0 ? Math.min(1, d.unitsFulfilled / d.unitsRequired) : 0;
            return (
              <div key={`${d.tradeSymbol}-${d.destinationSymbol}`} className="mb-3">
                <div className="flex justify-between items-baseline mb-1">
                  <span className="font-mono text-sm" style={{ color: NOIR.ink }}>{d.tradeSymbol}</span>
                  <span className="font-mono text-[11px]" style={{ color: NOIR.muted }}>
                    → {d.destinationSymbol} · {d.unitsFulfilled}/{d.unitsRequired}u
                  </span>
                </div>
                <div className="h-2 rounded-full overflow-hidden" style={{ background: noirAlpha(NOIR.dim, 0.25) }}>
                  <div
                    className="h-full rounded-full transition-all duration-700"
                    style={{ width: `${frac * 100}%`, background: frac >= 1 ? NOIR.good : NOIR.accent }}
                  />
                </div>
              </div>
            );
          })}
          <div className="grid grid-cols-3 gap-2 text-[11px] font-mono mb-1">
            <div>
              <div style={{ color: NOIR.dim }}>PAYOUT</div>
              <div style={{ color: NOIR.ink }}>{cr(contract.paymentOnFulfilled)}</div>
            </div>
            <div>
              <div style={{ color: NOIR.dim }}>COST</div>
              <div style={{ color: pl && pl.cost > 0 ? NOIR.warn : NOIR.ink }}>{cr(pl?.cost ?? 0)}</div>
            </div>
            <div>
              <div style={{ color: NOIR.dim }}>PROJECTED</div>
              <div style={{ color: projected != null && projected >= 0 ? NOIR.good : NOIR.bad }}>
                {projectedDisplay != null ? cr(projectedDisplay) : '—'}
              </div>
            </div>
          </div>
          <div className="text-[10px] font-mono" style={{ color: NOIR.dim }} title={`deadline ${contract.deadline}`}>
            {worker?.shipSymbol ? `worker ${worker.shipSymbol} · ` : ''}accepted {relTime(contract.lastUpdated, nowMs)}
          </div>
        </>
      ) : (
        <div className="text-sm mb-1" style={{ color: NOIR.muted }}>
          {phase === 'NEGOTIATE' ? 'Between contracts — negotiating the next one…' : 'No active contract. Coordinator idle.'}
          {lastFulfilled && (
            <div className="text-[11px] font-mono mt-1" style={{ color: NOIR.dim }}>
              last fulfilled +{lastFulfilled.payment.toLocaleString()} cr · {relTime(lastFulfilled.at, nowMs)}
            </div>
          )}
        </div>
      )}

      {live.recentFulfillments.length > 0 && (
        <div className="mt-2 pt-2" style={{ borderTop: `1px solid ${noirAlpha(NOIR.dim, 0.2)}` }}>
          <div className="flex justify-between text-[9px] font-mono mb-1 tracking-wider" style={{ color: NOIR.dim }}>
            <span>THROUGHPUT · LAST 6H</span>
            <span>hourly, oldest → newest</span>
          </div>
          <ThroughputBars fulfillments={live.recentFulfillments} nowMs={nowMs} />
        </div>
      )}

      <div className="flex justify-between mt-2 pt-2 text-[11px] font-mono" style={{ borderTop: `1px solid ${noirAlpha(NOIR.dim, 0.2)}` }}>
        <span style={{ color: NOIR.muted }}>
          <span style={{ color: NOIR.good }}>{cycle.fulfilledLastHour}</span> fulfilled / hr
        </span>
        <span style={{ color: NOIR.muted }}>
          avg cycle{' '}
          <span style={{ color: NOIR.ink }}>
            {cycle.avgCycleMinutes != null ? `${cycle.avgCycleMinutes.toFixed(1)} min` : '—'}
          </span>
        </span>
      </div>
    </div>
  );
}
