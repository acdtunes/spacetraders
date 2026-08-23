package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// The keys config.yaml.example documents must land on the struct. Viper unmarshals
// non-strictly, so a mistyped key is dropped in silence and the operator arms nothing.
func TestLoadConfig_SinkDepthScalingKeysReachTheStruct(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte(
		"absorption:\n  sink_depth_scaling:\n    enabled: true\n    thin_listings: 5\n    min_crush_scale: 0.25\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	policy := cfg.Absorption.ResolvedSinkDepthScaling()
	require.True(t, policy.Enabled)
	require.Equal(t, 5, policy.ThinListings)
	require.Equal(t, 0.25, policy.MinCrushScale)
}

// An absent [absorption] section resolves to the DISABLED sink-depth prior, so a
// daemon that never heard of the knob keeps the uniform crush model.
func TestResolvedSinkDepthScaling_AbsentSectionIsDisabled(t *testing.T) {
	got := AbsorptionConfig{}.ResolvedSinkDepthScaling()

	require.False(t, got.Enabled)
	require.Equal(t, 1.0, got.CrushScale(40), "a disabled policy charges every sink the full claim")
}

// Arming with no shape given takes the shipped fit rather than a zero shape, which
// would resolve to the uniform prior and make the arm silently inert.
func TestResolvedSinkDepthScaling_ArmedWithoutShapeTakesTheDefaults(t *testing.T) {
	got := AbsorptionConfig{SinkDepthScaling: SinkDepthScalingConfig{Enabled: true}}.ResolvedSinkDepthScaling()

	require.True(t, got.Enabled)
	require.Equal(t, absorption.DefaultThinListings, got.ThinListings)
	require.Equal(t, absorption.DefaultMinCrushScale, got.MinCrushScale)
	require.Less(t, got.CrushScale(40), 1.0)
}

// A captain's refit overrides both shape terms without a rebuild.
func TestResolvedSinkDepthScaling_ConfiguredShapeWins(t *testing.T) {
	got := AbsorptionConfig{SinkDepthScaling: SinkDepthScalingConfig{
		Enabled: true, ThinListings: 5, MinCrushScale: 0.25,
	}}.ResolvedSinkDepthScaling()

	require.Equal(t, 5, got.ThinListings)
	require.Equal(t, 0.25, got.MinCrushScale)
	require.Equal(t, 1.0, got.CrushScale(5))
	require.Equal(t, 0.25, got.CrushScale(1000))
}

// The documented fallback: min_crush_scale 1.0 reproduces the uniform model through
// config alone, so the refit can be reverted on a live fleet without a rebuild.
func TestResolvedSinkDepthScaling_UniformFallbackIsExpressible(t *testing.T) {
	got := AbsorptionConfig{SinkDepthScaling: SinkDepthScalingConfig{
		Enabled: true, MinCrushScale: 1.0,
	}}.ResolvedSinkDepthScaling()

	require.Equal(t, 1.0, got.CrushScale(1000))
}
