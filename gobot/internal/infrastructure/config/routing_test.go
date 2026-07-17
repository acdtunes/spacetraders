package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sp-bcsu: chart-on-gate-arrival defaults ON when [routing] chart_gate_on_arrival is
// absent (a nil switch => chart every gate a hull lands on, so a market-swept frontier
// never strands hulls on empty gate_edges), while an explicit `false` is preserved as the
// reversibility off-switch. The *bool idiom is what lets an absent section default ON yet
// keep an operator's explicit off-switch — a plain bool could not tell "unset" from "false".
func TestRoutingChartGateOnArrivalDefault(t *testing.T) {
	t.Run("absent defaults ON", func(t *testing.T) {
		cfg := &Config{}
		SetDefaults(cfg)
		require.NotNil(t, cfg.Routing.ChartGateOnArrival, "an absent switch must be defaulted, not left nil")
		require.True(t, *cfg.Routing.ChartGateOnArrival, "chart_gate_on_arrival must default ON")
	})

	t.Run("explicit false preserved", func(t *testing.T) {
		off := false
		cfg := &Config{}
		cfg.Routing.ChartGateOnArrival = &off
		SetDefaults(cfg)
		require.NotNil(t, cfg.Routing.ChartGateOnArrival)
		require.False(t, *cfg.Routing.ChartGateOnArrival,
			"an explicit chart_gate_on_arrival:false must be preserved as the reversibility off-switch")
	})
}

// sp-jgcache: the gate topology-cache TTL and the doomed-call precondition are config knobs
// with safe defaults that round-trip. GateCacheTTL bounds how long a stored gate edge is
// trusted before a re-fetch (the near-static gate topology makes a full day a comfortable
// default); an explicit value is preserved so an operator can tighten/loosen it without a
// rebuild. SkipUnchartedGateFetch (the *bool idiom, exactly like chart_gate_on_arrival)
// defaults ON — skip the guaranteed-400 live read on an uncharted origin gate — while an
// explicit false is preserved as the staged-rollout off-switch.
func TestRoutingGateCacheDefaults(t *testing.T) {
	t.Run("GateCacheTTL absent defaults to 24h", func(t *testing.T) {
		cfg := &Config{}
		SetDefaults(cfg)
		require.Equal(t, 24*time.Hour, cfg.Routing.GateCacheTTL, "the topology cache TTL must default to a safe 24h")
	})

	t.Run("GateCacheTTL explicit value preserved", func(t *testing.T) {
		cfg := &Config{}
		cfg.Routing.GateCacheTTL = 6 * time.Hour
		SetDefaults(cfg)
		require.Equal(t, 6*time.Hour, cfg.Routing.GateCacheTTL, "an explicit gate_cache_ttl must round-trip unchanged")
	})

	t.Run("SkipUnchartedGateFetch absent defaults ON", func(t *testing.T) {
		cfg := &Config{}
		SetDefaults(cfg)
		require.NotNil(t, cfg.Routing.SkipUnchartedGateFetch, "an absent switch must be defaulted, not left nil")
		require.True(t, *cfg.Routing.SkipUnchartedGateFetch, "skip_uncharted_gate_fetch must default ON")
	})

	t.Run("SkipUnchartedGateFetch explicit false preserved", func(t *testing.T) {
		off := false
		cfg := &Config{}
		cfg.Routing.SkipUnchartedGateFetch = &off
		SetDefaults(cfg)
		require.NotNil(t, cfg.Routing.SkipUnchartedGateFetch)
		require.False(t, *cfg.Routing.SkipUnchartedGateFetch,
			"an explicit skip_uncharted_gate_fetch:false must be preserved as the staged-rollout off-switch")
	})
}
