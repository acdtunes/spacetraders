package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Behavior 5 (sp-p1ci): [ship_resync] interval/jitter map to durations, an
// unset (zero) knob defers to the documented default, and a negative jitter
// explicitly disables jitter. Config is the source of truth for the cadence
// (sp-ts82 live-config idiom); this pins the default fallbacks so an absent
// section still boots the resync at 1h +/-10min.
func TestResyncConfig_ResolvesIntervalAndJitterWithDefaults(t *testing.T) {
	cases := []struct {
		name         string
		cfg          ResyncConfig
		wantInterval time.Duration
		wantJitter   time.Duration
	}{
		{"absent -> defaults", ResyncConfig{}, DefaultResyncInterval, DefaultResyncJitter},
		{"explicit values", ResyncConfig{IntervalSeconds: 1800, JitterSeconds: 300}, 30 * time.Minute, 5 * time.Minute},
		{"negative jitter disables", ResyncConfig{IntervalSeconds: 600, JitterSeconds: -1}, 10 * time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantInterval, tc.cfg.ResolvedInterval())
			require.Equal(t, tc.wantJitter, tc.cfg.ResolvedJitter())
		})
	}
}
