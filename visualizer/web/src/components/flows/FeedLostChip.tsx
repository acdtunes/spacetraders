import { NOIR } from '../../theme/noir';
import { formatElapsed } from './feedLostElapsed';

interface Props {
  feedLost: boolean;
  lastPlanAt: string | null;
  nowMs: number;
}

// Mirrors the observatory SIGNAL LOST doctrine: when the daemon feed is dark the
// tab stays on PG residue and shows how stale the last known plan is.
export function FeedLostChip({ feedLost, lastPlanAt, nowMs }: Props) {
  if (!feedLost) return null;
  return (
    <div
      className="absolute top-4 right-4 px-3 py-1.5 rounded text-xs font-mono flex items-center gap-2"
      style={{ background: NOIR.panel, color: NOIR.bad, border: `1px solid ${NOIR.bad}` }}
      role="status"
    >
      <span className="inline-block w-2 h-2 rounded-full" style={{ background: NOIR.bad }} />
      FEED LOST · last plan {formatElapsed(lastPlanAt, nowMs)} ago
    </div>
  );
}
