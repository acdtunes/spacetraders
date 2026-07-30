package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// sp-k4z5b: the debounced market-set re-cut's AGE trigger becomes a LIVE knob.
//
// It was named alongside the tour path's freshness caps in the incident sweep, but it is
// a different animal and is deliberately NOT rotation-derived: it measures how long a
// partition DRIFT has been pending, from the moment the drift was noticed, and a bigger
// charted map does not invalidate that. What it did share with them was being unreachable
// without a daemon bounce.
func TestScoutPostTune_MarketDriftMaxAgeIsLiveTunable(t *testing.T) {
	cmd := &RunScoutPostCoordinatorCommand{MarketDriftMaxAgeSecs: 900}

	// No live reader (or a failed read) → the tick runs on the launch command.
	require.Equal(t, 15*time.Minute, resolveMarketDriftMaxAge(cmd, nil))

	// A live snapshot is authoritative and applies with no restart.
	require.Equal(t, 2*time.Hour, resolveMarketDriftMaxAge(cmd,
		liveconfig.Snapshot{"market_drift_max_age_secs": 7200}))

	// `tune ... 0` reverts to the documented default rather than collapsing the debounce
	// to zero, which would re-cut every partitioned post on every single new market.
	require.Equal(t, defaultMarketDriftMaxAge, resolveMarketDriftMaxAge(cmd,
		liveconfig.Snapshot{"market_drift_max_age_secs": 0}))
	require.Equal(t, time.Hour, defaultMarketDriftMaxAge, "the documented default is one hour")

	// A launch command with nothing set also lands on the default.
	require.Equal(t, defaultMarketDriftMaxAge, resolveMarketDriftMaxAge(&RunScoutPostCoordinatorCommand{}, nil))

	// The persisted number is SECONDS on both sides of the JSON boundary (recovered
	// container config decodes as float64), so a tune survives a daemon bounce.
	require.Equal(t, 45*time.Minute, resolveMarketDriftMaxAge(cmd,
		liveconfig.Snapshot{"market_drift_max_age_secs": float64(2700)}))
}

// The registry's documented default is the const the coordinator actually falls back to.
func TestScoutPostTunableDefaults_CarryTheMarketDriftAge(t *testing.T) {
	defaults := ScoutPostTunableDefaults()
	require.Equal(t, int(defaultMarketDriftMaxAge.Seconds()), defaults["market_drift_max_age_secs"])
}
