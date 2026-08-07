package queries

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
)

// laneSurfaceRig points the daemon's growth collector at a private registry for one test and hands
// back a reader of the published series. Nothing here asserts through the reader's return value:
// the subject is what an OPERATOR can see, and a term computed but never published is invisible.
func laneSurfaceRig(t *testing.T) func(component string) float64 {
	t.Helper()
	priorRegistry := metrics.Registry
	t.Cleanup(func() {
		metrics.Registry = priorRegistry
		metrics.SetGlobalFleetGrowthCollector(nil)
	})

	registry := prometheus.NewRegistry()
	metrics.Registry = registry
	collector := metrics.NewFleetGrowthMetricsCollector()
	require.NoError(t, collector.Register())
	metrics.SetGlobalFleetGrowthCollector(collector)

	return func(component string) float64 {
		t.Helper()
		return laneSurfaceComponent(t, registry, component)
	}
}

func laneSurfaceComponent(t *testing.T, registry *prometheus.Registry, component string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "spacetraders_daemon_growth_lane_surface" {
			continue
		}
		for _, m := range family.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "component" && l.GetValue() == component {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("no growth_lane_surface series for component %q", component)
	return 0
}

// THE TERMS MUST SURVIVE THE HANDOFF. The census computes them and the unserved reader is the only
// writer of the gauge, so a term the reader forgets to carry is dead on arrival — computed, tested
// one layer down, and still invisible to the operator reading the surface.
func TestUnservedLaneCount_PublishesTheCensusTerms(t *testing.T) {
	component := laneSurfaceRig(t)
	lanes := &fakeLaneCounter{
		census:   LaneCensus{Profitable: 12, CrossSystem: 7, AbsorbableUnits: 264, RejectedUnreachable: 21, RejectedJumpCost: 5},
		readable: true,
	}

	r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(2)), lanes)
	_, readable, err := r.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)

	for _, tc := range []struct {
		component string
		want      float64
	}{
		{"unserved", 10},
		{"profitable", 12},
		{"trade_pool", 2},
		{"systems_scanned", 1},
		{"cross_system", 7},
		{"absorbable_units", 264},
		{"rejected_unreachable", 21},
		{"rejected_jump_cost", 5},
		{"readable", 1},
	} {
		require.Equal(t, tc.want, component(tc.component), "component %q", tc.component)
	}
}

// An unreadable surface publishes ZERO terms, not the last readable tick's. A term standing beside
// readable=0 reads as an explanation of a count nobody took — the stale-number blindness the parts
// were split out to remove, coming back through the terms.
func TestUnservedLaneCount_ABlindReadLeavesNoTermStanding(t *testing.T) {
	component := laneSurfaceRig(t)
	lanes := &fakeLaneCounter{
		census:   LaneCensus{Profitable: 12, CrossSystem: 7, AbsorbableUnits: 264, RejectedUnreachable: 21, RejectedJumpCost: 5},
		readable: true,
	}
	r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(2)), lanes)
	_, _, err := r.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)

	lanes.readable = false
	_, readable, err := r.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, readable)

	require.Zero(t, component("readable"))
	for _, name := range []string{"profitable", "cross_system", "absorbable_units", "rejected_unreachable", "rejected_jump_cost"} {
		require.Zero(t, component(name), "component %q survived a blind read", name)
	}
}
