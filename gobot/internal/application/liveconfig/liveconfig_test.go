package liveconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSnapshot_PositiveInt pins PositiveInt as the single primitive the per-tick
// fallback chain hangs on: a key is a live override ONLY when present, numeric,
// and positive. It must decode every numeric shape a container config can carry —
// native int/int64 on the fresh-launch path AND float64 on the JSON-recovery path
// (omitting one shape would silently zero a money knob).
func TestSnapshot_PositiveInt(t *testing.T) {
	cases := []struct {
		name   string
		snap   Snapshot
		key    string
		want   int
		wantOK bool
	}{
		{"native int", Snapshot{"max_spend_per_cycle": 500000}, "max_spend_per_cycle", 500000, true},
		{"native int64", Snapshot{"max_spend_per_cycle": int64(500000)}, "max_spend_per_cycle", 500000, true},
		{"json float64 roundtrip", Snapshot{"purchase_cooldown_secs": float64(60)}, "purchase_cooldown_secs", 60, true},
		{"zero is not an override", Snapshot{"purchase_cooldown_secs": 0}, "purchase_cooldown_secs", 0, false},
		{"negative is not an override", Snapshot{"max_probe_fleet": -1}, "max_probe_fleet", 0, false},
		{"absent key", Snapshot{"other": 1}, "max_probe_fleet", 0, false},
		{"wrong type is not an override", Snapshot{"max_probe_fleet": "40"}, "max_probe_fleet", 0, false},
		{"nil snapshot", nil, "max_probe_fleet", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.snap.PositiveInt(tc.key)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// PositiveIntOrZero is the resolve-time convenience: a live override when one is
// set, else 0 so the coordinator's existing "<= 0 → documented default" chain
// applies — the exact revert semantics of `tune <key> 0`.
func TestSnapshot_PositiveIntOrZero(t *testing.T) {
	snap := Snapshot{"set": 120, "zeroed": 0}
	require.Equal(t, 120, snap.PositiveIntOrZero("set"))
	require.Equal(t, 0, snap.PositiveIntOrZero("zeroed"), "a zeroed key falls to the default chain")
	require.Equal(t, 0, snap.PositiveIntOrZero("absent"), "an absent key falls to the default chain")
	require.Equal(t, 0, Snapshot(nil).PositiveIntOrZero("set"), "a nil snapshot falls to the default chain")
}
