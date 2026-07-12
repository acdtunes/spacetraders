export interface MutationLogEntry {
  seq: number;
  call: string;
  detail?: Record<string, unknown>;
  at: string; // world-time (rfc3339) at which the mutation occurred
}

export function countCall(log: MutationLogEntry[], call: string): number {
  return log.filter((e) => e.call === call).length;
}

export function ticksOf(log: MutationLogEntry[], call: string): string[] {
  return log.filter((e) => e.call === call).map((e) => e.at);
}
