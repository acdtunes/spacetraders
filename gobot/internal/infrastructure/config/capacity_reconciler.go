package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/viper"
)

// CapacityReconcilerConfig holds the capacity reconciler's calibration knobs
// (epic st-7zk; spec 2026-07-15-capacity-reconciler-design.md, "Calibration
// params (config, not code)"). It nests under the top-level
// [capacity_reconciler] section and is injected into the
// capacity_reconciler_coordinator container's launch config on every build —
// creation AND restart recovery, via resolveCapacityReconcilerConfig — so a
// captain retunes the engine by editing config.yaml and restarting, with NO
// code redeploy (the sp-ts82 live-config pattern, RULINGS #2/#5).
//
// Every knob follows the codebase idiom: a zero value means "unset" and
// defers to the coordinator's documented protective default (resolved in
// capacity.DefaultCalibration — per-decision cap 25%, reserve floor 50k,
// surplus fraction 0.25, payback horizon 24h, tick 300s, approval threshold 0
// = every capital action needs approval). Out-of-range explicit values are
// rejected at config load (tags below) AND at coordinator launch
// (Calibration.Validate) — fail loud, never clamp silently.
type CapacityReconcilerConfig struct {
	// DryRun runs the reconciler observe-only: SENSE/PLAN/DIFF/GOVERN execute
	// normally (all read-only) but CONVERGE actuates nothing and files no
	// proposal — it LOGS what it WOULD do each tick instead. The recommended
	// first-start posture (watch a live cycle before arming). Also settable
	// per-launch via the CLI --dry-run flag; either source arms observe-only.
	// NOT dark-shipping — every skipped decision is logged loudly. 0/absent →
	// false (armed). Resolved LIVE on every build like the calibration knobs.
	DryRun bool `mapstructure:"dry_run"`

	// ReserveFloor is the HARD treasury floor in credits — the capex governor
	// never spends below it. 0/absent → 50000 (the immutable reserve floor).
	ReserveFloor int64 `mapstructure:"reserve_floor" validate:"omitempty,min=0"`

	// SurplusFraction is f in the surplus-fraction drain: deployable capex
	// per cycle = f × (treasury − floor). 0/absent → 0.25.
	SurplusFraction float64 `mapstructure:"surplus_fraction" validate:"omitempty,min=0,max=1"`

	// PerDecisionCapPct caps one capital decision at this percent of the
	// deployable budget. 0/absent → 25 (spec default).
	PerDecisionCapPct int `mapstructure:"per_decision_cap_pct" validate:"omitempty,min=1,max=100"`

	// ROIPaybackHorizonHours is the window a capital item must pay itself
	// back within. 0/absent → 24.
	ROIPaybackHorizonHours float64 `mapstructure:"roi_payback_horizon_hours" validate:"omitempty,gt=0"`

	// AddThresholdPerHullCrHr is the per-hull credits/hr floor a capacity add
	// must keep the fleet above. 0/absent → no explicit floor (the
	// raise-the-average absorption ceiling still applies).
	AddThresholdPerHullCrHr float64 `mapstructure:"add_threshold_per_hull_cr_hr" validate:"omitempty,min=0"`

	// StockerCapacityBudget is the per-hub stocker-capacity budget (units)
	// the planner selects buffer goods under. 0/absent → planner default.
	StockerCapacityBudget int `mapstructure:"stocker_capacity_budget" validate:"omitempty,min=0"`

	// ContractAddGateTradeBlind gates a NEW contract depot on the contract op's
	// own economics (universal per-hull floor + the depot's cycle-time-
	// compression ROI) instead of the trade-contaminated fleet-wide per-hull
	// average. false/absent → OFF = byte-identical (add gate keeps benchmarking
	// against the fleet average). Set true to arm.
	ContractAddGateTradeBlind bool `mapstructure:"contract_add_gate_trade_blind"`

	// TickIntervalSecs is the reconcile cadence. 0/absent → 300.
	TickIntervalSecs int `mapstructure:"tick_interval_secs" validate:"omitempty,min=5"`

	// ApprovalThreshold is the capex-proposal approval threshold in credits:
	// a tier-4 action costing at least this files a proposal instead of
	// auto-executing. 0/absent → 0 = EVERY tier-4 action requires approval
	// (tiered autonomy v1).
	ApprovalThreshold int64 `mapstructure:"approval_threshold" validate:"omitempty,min=0"`
}

// misplacedTopLevelReconcilerKeys are the config.yaml TOP-LEVEL keys that are
// really [capacity_reconciler] knobs written at the wrong nesting. Each knob's
// container-launch key is "capacity_" + its section field name, so a captain who
// writes that container-key name at the top level (instead of nesting it under
// capacity_reconciler:) produces a key viper binds to NOTHING — the knob
// silently no-ops. Derived by reflection over the struct's mapstructure tags so
// the set can never drift from the fields it guards.
func misplacedTopLevelReconcilerKeys() []string {
	t := reflect.TypeOf(CapacityReconcilerConfig{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("mapstructure"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, "capacity_"+tag)
	}
	return keys
}

// checkMisplacedCapacityReconcilerKeys FAILS the config load when a reconciler
// knob sits at the config.yaml top level under its container-launch key name
// instead of nested under [capacity_reconciler]. viper silently drops such a key,
// so the knob no-ops with ZERO signal — an armed gate that never arms. Fail-loud
// at load beats a silent disarm the operator only discovers by watching live
// telemetry.
func checkMisplacedCapacityReconcilerKeys(v *viper.Viper) error {
	present := make(map[string]bool)
	for _, k := range v.AllKeys() {
		present[k] = true
	}
	var misplaced []string
	for _, key := range misplacedTopLevelReconcilerKeys() {
		if present[key] {
			nested := "capacity_reconciler." + strings.TrimPrefix(key, "capacity_")
			misplaced = append(misplaced, fmt.Sprintf("%q (nest it as %q)", key, nested))
		}
	}
	if len(misplaced) == 0 {
		return nil
	}
	sort.Strings(misplaced)
	return fmt.Errorf("capacity_reconciler knob(s) at config top level bind to nothing and silently no-op — move them under the capacity_reconciler: section: %s",
		strings.Join(misplaced, "; "))
}
