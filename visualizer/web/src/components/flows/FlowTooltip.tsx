import { useFlowStore } from '../../store/flowStore';
import { NOIR, noirAlpha } from '../../theme/noir';
import { freshnessColor } from './freshness';
import { signedCompact } from './FillTicker';
import type { ScoutPostStatus } from '../../types/flows';

const money = (n: number) => Math.round(n).toLocaleString('en-US');

const POST_STATUS_COLOR: Record<ScoutPostStatus, string> = {
  manned: NOIR.good,
  relay: NOIR.warn,
  unmanned: NOIR.dim,
};

// Shared hover card for a galaxy system node or artery lane. Pure-presentational:
// reads the store's tooltip target and resolves everything else (freshness,
// activity, hull count, lane goods) from the already-polled slices — missing
// sections are simply omitted. Positioned FIXED at the pointer's client coords
// (the scene computes x/y from the stage-container rect, i.e. viewport space,
// so absolute-in-page would be offset by the nav bar).
export function FlowTooltip() {
  const tooltip = useFlowStore((s) => s.tooltip);
  const freshness = useFlowStore((s) => s.freshness);
  const lanes = useFlowStore((s) => s.lanes);
  const live = useFlowStore((s) => s.live);
  const topology = useFlowStore((s) => s.topology);
  if (!tooltip) return null;

  return (
    <div
      className="fixed z-20 pointer-events-none rounded-lg px-3 py-2 text-xs backdrop-blur"
      style={{
        left: tooltip.x + 14,
        top: tooltip.y + 14,
        background: `${NOIR.panel}E6`,
        border: `1px solid ${NOIR.nebulaCore}`,
        color: NOIR.ink,
        maxWidth: 260,
      }}
    >
      {tooltip.kind === 'system' ? (
        <SystemCard
          system={tooltip.key}
          isHome={topology?.homeSystem === tooltip.key}
          record={freshness?.systems.find((r) => r.system === tooltip.key) ?? null}
          activity={lanes?.systemActivity.find((a) => a.system === tooltip.key) ?? null}
          hulls={(live?.flows ?? []).filter((f) => f.shipNav?.systemSymbol === tooltip.key).length}
        />
      ) : (
        <LaneCard laneKey={tooltip.key} lanes={lanes?.systemLanes ?? []} />
      )}
    </div>
  );
}

function SystemCard({
  system,
  isHome,
  record,
  activity,
  hulls,
}: {
  system: string;
  isHome: boolean;
  record: { freshnessPct: number; scoutPost: { status: ScoutPostStatus; hull: string | null } | null } | null;
  activity: { realizedProfit: number; legCount: number } | null;
  hulls: number;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="font-mono" style={{ color: isHome ? NOIR.star : NOIR.accent }}>
        {isHome ? `★ ${system} · HOME` : system}
      </div>
      {record && (
        <div style={{ color: NOIR.muted }}>
          visibility{' '}
          <span style={{ color: freshnessColor(record.freshnessPct) }}>{Math.round(record.freshnessPct)}%</span>
        </div>
      )}
      {activity && (
        <div style={{ color: NOIR.muted }}>
          realized{' '}
          <span style={{ color: activity.realizedProfit >= 0 ? NOIR.good : NOIR.bad }}>
            {money(activity.realizedProfit)}
          </span>
          {` · ${activity.legCount} legs`}
        </div>
      )}
      <div style={{ color: NOIR.dim }}>{`${hulls} hull${hulls === 1 ? '' : 's'} in-system`}</div>
      {record?.scoutPost && (
        <div style={{ color: NOIR.muted }}>
          post:{' '}
          <span style={{ color: POST_STATUS_COLOR[record.scoutPost.status] }}>{record.scoutPost.status}</span>
          {record.scoutPost.hull ? ` (${record.scoutPost.hull})` : ''}
        </div>
      )}
    </div>
  );
}

function LaneCard({
  laneKey,
  lanes,
}: {
  laneKey: string;
  lanes: { from: string; to: string; realizedProfit: number; legCount: number; topGoods: { good: string; credits: number }[] }[];
}) {
  const [from, to] = laneKey.split('→');
  const lane = lanes.find((l) => l.from === from && l.to === to) ?? null;
  return (
    <div className="flex flex-col gap-0.5">
      <div className="font-mono" style={{ color: NOIR.accent }}>{`${from} → ${to}`}</div>
      {lane && (
        <>
          <div style={{ color: NOIR.muted }}>
            realized{' '}
            <span style={{ color: lane.realizedProfit >= 0 ? NOIR.good : NOIR.bad }}>{money(lane.realizedProfit)}</span>
            {` · ${lane.legCount} trips`}
          </div>
          {lane.topGoods.map((g) => (
            <div key={g.good} className="flex justify-between gap-3" style={{ color: noirAlpha(NOIR.ink, 0.85) }}>
              <span>{g.good}</span>
              <span style={{ color: g.credits >= 0 ? NOIR.good : NOIR.bad }}>{signedCompact(g.credits)}</span>
            </div>
          ))}
        </>
      )}
    </div>
  );
}
