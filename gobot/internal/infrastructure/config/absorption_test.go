package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// The keys config.yaml.example documents must land on the struct. Viper unmarshals
// non-strictly, so a mistyped key is dropped in silence and the operator tunes nothing.
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

// THE KILL SWITCH. An explicit false is the one way to charge every sink the full claim again,
// and it has to survive the "absent means on" resolution.
func TestLoadConfig_SinkDepthScalingDisablesOnExplicitFalse(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte(
		"absorption:\n  sink_depth_scaling:\n    enabled: false\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	policy := cfg.Absorption.ResolvedSinkDepthScaling()
	require.False(t, policy.Enabled)
	require.Equal(t, 1.0, policy.CrushScale(40), "a disabled prior charges every sink the full claim")
}

// An absent [absorption] section runs the shipped fit: the prior is in force with no config
// present, so a fleet nobody configured is not a fleet running a different model.
func TestResolvedSinkDepthScaling_AbsentSectionRunsTheShippedFit(t *testing.T) {
	got := AbsorptionConfig{}.ResolvedSinkDepthScaling()

	require.True(t, got.Enabled)
	require.Equal(t, absorption.DefaultThinListings, got.ThinListings)
	require.Equal(t, absorption.DefaultMinCrushScale, got.MinCrushScale)
	require.Less(t, got.CrushScale(40), 1.0)
}

// A captain's refit overrides both shape terms without a rebuild.
func TestResolvedSinkDepthScaling_ConfiguredShapeWins(t *testing.T) {
	got := AbsorptionConfig{SinkDepthScaling: SinkDepthScalingConfig{
		ThinListings: 5, MinCrushScale: 0.25,
	}}.ResolvedSinkDepthScaling()

	require.Equal(t, 5, got.ThinListings)
	require.Equal(t, 0.25, got.MinCrushScale)
	require.Equal(t, 1.0, got.CrushScale(5))
	require.Equal(t, 0.25, got.CrushScale(1000))
}

// min_crush_scale 1.0 flattens the prior to the uniform model through config alone, so the shape
// can be reverted on a live fleet without disabling the mechanism.
func TestResolvedSinkDepthScaling_UniformShapeIsExpressible(t *testing.T) {
	got := AbsorptionConfig{SinkDepthScaling: SinkDepthScalingConfig{
		MinCrushScale: 1.0,
	}}.ResolvedSinkDepthScaling()

	require.Equal(t, 1.0, got.CrushScale(1000))
}
