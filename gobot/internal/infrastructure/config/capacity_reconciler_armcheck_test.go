package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The CORRECT shape: the arm nested under the capacity_reconciler: section binds
// to the calibration and actually arms the trade-blind ADD gate.
func TestCapacityReconciler_NestedArmBinds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("capacity_reconciler:\n  contract_add_gate_trade_blind: true\n"), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.True(t, cfg.CapacityReconciler.ContractAddGateTradeBlind,
		"nested capacity_reconciler.contract_add_gate_trade_blind must arm the reconciler")
}

// ROOT CAUSE: the reconciler arm was written at config.yaml TOP LEVEL
// under the container-launch key name (capacity_contract_add_gate_trade_blind).
// viper binds the struct to capacity_reconciler.contract_add_gate_trade_blind, so
// the top-level key resolves to NOTHING — the arm silently no-ops, the reconciler
// runs OFF, and every contract depot is suppressed by the trade-inflated fleet
// average with zero signal. The loader must now REFUSE a misplaced knob loudly.
func TestCapacityReconciler_MisplacedTopLevelArm_FailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("capacity_contract_add_gate_trade_blind: true\n"), 0o644))

	_, err := LoadConfig(path)
	require.Error(t, err, "a top-level capacity_ reconciler knob must fail the load, not silently no-op")
	require.Contains(t, err.Error(), "capacity_contract_add_gate_trade_blind")
	require.Contains(t, err.Error(), "capacity_reconciler.contract_add_gate_trade_blind")
}

// The guard covers every reconciler knob, derived from the struct fields — not
// just the one that bit us — so any future knob inherits the fail-loud safety.
func TestCapacityReconciler_MisplacedNumericKnob_FailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("capacity_reserve_floor: 75000\n"), 0o644))

	_, err := LoadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "capacity_reserve_floor")
}

// A config with no reconciler section at all loads clean — the guard only fires
// on the specific misplacement, never on a legitimately-absent knob.
func TestCapacityReconciler_NoSection_LoadsClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("captain:\n  enabled: false\n"), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.False(t, cfg.CapacityReconciler.ContractAddGateTradeBlind)
}
