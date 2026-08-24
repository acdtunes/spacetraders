package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The keys config.yaml.example documents must land on the struct. Viper unmarshals
// non-strictly, so a mistyped key is dropped in silence and the operator tunes nothing.
func TestLoadConfig_SourceDepthScalingKeysReachTheStruct(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte(
		"trade_impact:\n  source_depth_scaling:\n    enabled: true\n    thin_listings: 4\n    min_debt_scale: 0.35\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	policy := cfg.TradeImpact.ResolvedSourceDepthScaling()
	require.True(t, policy.Enabled)
	require.Equal(t, 4, policy.ThinListings)
	require.Equal(t, 0.35, policy.MinDebtScale)
}

// THE KILL SWITCH. An explicit false is the one way to pace every source at its full debt again,
// and it has to survive the "absent means on" resolution.
func TestLoadConfig_SourceDepthScalingDisablesOnExplicitFalse(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte(
		"trade_impact:\n  source_depth_scaling:\n    enabled: false\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	policy := cfg.TradeImpact.ResolvedSourceDepthScaling()
	require.False(t, policy.Enabled)
	require.Equal(t, 1.0, policy.DebtScale(40), "a disabled prior paces every source at its full debt")
}

// An absent [trade_impact] section runs the shipped fit: the prior is in force with no config
// present, so a fleet nobody configured is not a fleet pacing on a different model.
func TestResolvedSourceDepthScaling_AbsentSectionRunsTheShippedFit(t *testing.T) {
	got := TradeImpactConfig{}.ResolvedSourceDepthScaling()

	require.True(t, got.Enabled)
	require.Equal(t, trading.DefaultSourceThinListings, got.ThinListings)
	require.Equal(t, trading.DefaultMinSourceDebtScale, got.MinDebtScale)
	require.Less(t, got.DebtScale(40), 1.0)
}

// A captain's refit overrides both shape terms without a rebuild.
func TestResolvedSourceDepthScaling_ConfiguredShapeWins(t *testing.T) {
	got := TradeImpactConfig{SourceDepthScaling: SourceDepthScalingConfig{
		ThinListings: 4, MinDebtScale: 0.35,
	}}.ResolvedSourceDepthScaling()

	require.Equal(t, 1.0, got.DebtScale(4))
	require.Equal(t, 0.35, got.DebtScale(1000))
}

// min_debt_scale 1.0 flattens the prior to the uniform model through config alone, so the shape
// can be reverted on a live fleet without disabling the mechanism.
func TestResolvedSourceDepthScaling_UniformShapeIsExpressible(t *testing.T) {
	got := TradeImpactConfig{SourceDepthScaling: SourceDepthScalingConfig{
		MinDebtScale: 1.0,
	}}.ResolvedSourceDepthScaling()

	require.Equal(t, 1.0, got.DebtScale(1000))
}
