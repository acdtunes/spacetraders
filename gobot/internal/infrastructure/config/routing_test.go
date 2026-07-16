package config

import (
	"testing"

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
