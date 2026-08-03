# Era waypoint fixtures (sp-9suun)

`era{3,4,5}_waypoints.json` are REAL rows of the daemon's `waypoints` table for the three
measured home systems — era 3 `X1-VB74`, era 4 `X1-UM5`, era 5 `X1-KP23` — dumped verbatim
with:

```sql
SELECT jsonb_pretty(jsonb_agg(r ORDER BY r->>'symbol')) FROM (
  SELECT jsonb_build_object(
    'symbol', waypoint_symbol, 'type', type, 'x', x::float8, 'y', y::float8,
    'traits', COALESCE(NULLIF(traits,'')::jsonb, '[]'::jsonb), 'has_fuel', (has_fuel = 1)
  ) AS r FROM waypoints WHERE system_symbol = '<era home system>'
) s;
```

They carry ONLY durable charted facts (type, traits, coordinates, on-site fuel) — no market
scan data — because the era-invariant standby anchors must resolve from the charted template
alone, before any dock scan. The waypoint SYMBOLS in these files are fixture data and EXPECTED
OUTPUTS; production code never names one (waypoint numbering reshuffles every era).
