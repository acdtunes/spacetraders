package watchkeeper

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// TestPreflightWarnsWhenWakeChannelUnusable covers sp-sk68 D6: pid 20880
// started under an env lacking BD_REAL, so every gc call failed from tick one,
// yet startup printed a normal banner. A single cheap probe at startup makes
// an env-broken delivery substrate loud and distinct immediately, instead of
// discoverable only by reading generic per-tick errors.
func TestPreflightWarnsWhenWakeChannelUnusable(t *testing.T) {
	gw := &respawnGateway{aliveErr: errors.New("gc failed: bd-router: cannot find the real bd binary on PATH (set BD_REAL)")}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain"}, gw: gw}

	out := captureOutput(t, func() {
		sup.Preflight(context.Background())
	})

	require.Contains(t, out, "wake-delivery channel unusable",
		"a broken gc/bd delivery channel must be flagged loudly at startup")
}

func TestPreflightSilentWhenWakeChannelHealthy(t *testing.T) {
	gw := &respawnGateway{alive: map[string]bool{"captain": true}}
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain"}, gw: gw}

	out := captureOutput(t, func() {
		sup.Preflight(context.Background())
	})

	require.NotContains(t, out, "wake-delivery channel unusable",
		"a healthy channel must not print the startup warning")
}

// TestPreflightNoGatewayIsNoop guards the legacy/uninitialized path: a
// Supervisor without a city gateway wired must not panic on Preflight.
func TestPreflightNoGatewayIsNoop(t *testing.T) {
	sup := &Supervisor{cfg: config.CaptainConfig{CaptainAgent: "captain"}}
	out := captureOutput(t, func() {
		sup.Preflight(context.Background())
	})
	require.NotContains(t, out, "wake-delivery channel unusable")
}
