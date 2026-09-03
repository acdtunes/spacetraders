package config

// Round-trip pin on the two keys an operator edits to retune (or disarm) the per-tranche
// money guards' read reuse. A typo in a mapstructure tag ships a knob that reads as
// configurable and moves nothing; a plain int would also collapse "absent" and
// "explicitly 0" — which mean ARMED and DISARMED — into the same value, so the pointer
// is the load-bearing part and this exercises the REAL viper pipeline.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
)

func loadCargoGuards(t *testing.T, yaml string) CargoGuardsConfig {
	t.Helper()
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")
	require.NoError(t, err)
	return cfg.CargoGuards
}

func TestLoadConfig_CargoGuardsReuse_RoundTrips(t *testing.T) {
	cases := []struct {
		name         string
		yaml         string
		wantHeadroom int
		wantMaxAge   int // seconds
	}{
		{
			name:         "absent section is the armed default",
			yaml:         "refuel:\n  threshold: 0.9\n",
			wantHeadroom: shipCargo.DefaultGuardReuseHeadroomPct,
			wantMaxAge:   int(shipCargo.DefaultGuardReuseMaxAge.Seconds()),
		},
		{
			name:         "an explicit zero headroom disarms",
			yaml:         "cargo_guards:\n  reuse_headroom_pct: 0\n",
			wantHeadroom: 0,
			wantMaxAge:   int(shipCargo.DefaultGuardReuseMaxAge.Seconds()),
		},
		{
			name:         "a negative headroom disarms",
			yaml:         "cargo_guards:\n  reuse_headroom_pct: -1\n",
			wantHeadroom: 0,
			wantMaxAge:   int(shipCargo.DefaultGuardReuseMaxAge.Seconds()),
		},
		{
			name:         "an explicit zero max age disarms rather than re-arming the default",
			yaml:         "cargo_guards:\n  reuse_max_age_secs: 0\n",
			wantHeadroom: 0,
			wantMaxAge:   int(shipCargo.DefaultGuardReuseMaxAge.Seconds()),
		},
		{
			name:         "both keys reach the handler",
			yaml:         "cargo_guards:\n  reuse_headroom_pct: 15\n  reuse_max_age_secs: 60\n",
			wantHeadroom: 15,
			wantMaxAge:   60,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guards := loadCargoGuards(t, tc.yaml)

			headroom, maxAge := shipCargo.ResolveGuardReuse(guards.ReuseHeadroomPct, guards.ReuseMaxAgeSecs)

			require.Equal(t, tc.wantHeadroom, headroom,
				"cargo_guards.reuse_headroom_pct must reach the cargo handler, or the reuse cannot be retuned or disarmed by editing config.yaml + restarting")
			require.Equal(t, tc.wantMaxAge, int(maxAge.Seconds()))
		})
	}
}
