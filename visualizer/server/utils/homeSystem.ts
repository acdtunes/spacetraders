// Derive the home SYSTEM symbol from an agent's headquarters WAYPOINT symbol.
// SpaceTraders symbols are hierarchical: "X1-KA42-A1" = sector X1, system X1-KA42,
// waypoint X1-KA42-A1. The home system is the first two dash segments.
// Returns null for anything malformed so the caller can OMIT the field rather
// than guess a home (the Admiral directive: never fabricate the marker).
export function homeSystemFromHeadquarters(
  headquarters: string | null | undefined,
): string | null {
  if (!headquarters || typeof headquarters !== 'string') return null;
  const parts = headquarters.split('-');
  if (parts.length < 2) return null;
  const [sector, system] = parts;
  if (!sector || !system) return null;
  return `${sector}-${system}`;
}
