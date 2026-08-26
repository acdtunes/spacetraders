package commands

// SensingTunableDefaults maps every LIVE-tunable parked-probe sensing knob to its
// documented default, sourced from the coordinator's own consts so the tune
// registry can never drift from the behavior (the SizerTunableDefaults
// discipline). goods_whitelist is deliberately absent: the int-only tune
// mechanism carries no strings, so the whitelist is operated through the
// [sensing] config.yaml section instead.
//
// The touring model's knobs — probe_budget, second_probe_threshold,
// purchase_cooldown_secs, max_spend_per_cycle, spend_window_secs,
// freshness_target_secs, depth_floor and discovery_declares_per_tick — are GONE
// from this map, which is what makes `tune probe_budget` fail as an unknown key
// rather than silently write a value nothing reads. The coordinator still
// tolerates them in a persisted config so an old container recovers (RULINGS
// #2); it simply ignores them.
func SensingTunableDefaults() map[string]int {
	return map[string]int{
		"tick_secs":    defaultSensingTickSeconds,
		"wait_low_ms":  defaultSensingWaitLowMs,
		"wait_high_ms": defaultSensingWaitHighMs,

		"probe_cap":                  defaultParkedProbeCap,
		"expansion_enabled":          defaultExpansionEnabled,
		"target_util_pct":            defaultTargetUtilPct,
		"min_scan_rate_milli":        defaultMinScanRateMilli,
		"expansion_min_budget_milli": defaultExpansionMinBudgetMilli,
		"value_clamp_r":              defaultValueClampR,
		"inflight_cap":               defaultInflightCap,
		"capital_multiplier_k_milli": defaultCapitalMultiplierKMilli,
		"capex_reserve_credits":      defaultCapexReserveCredits,
		"quartermaster_cadence_secs": defaultQuartermasterCadenceSecs,
		"surge_inflight_cap":         defaultSurgeInFlightCap,
		"coverage_reserve":           defaultCoverageReserve,

		// The probe-procurement pair, ARMED at these values with no config present.
		"procurement_walkaway_mult":        defaultWalkAwayMult,
		"procurement_jump_penalty_credits": defaultJumpPenaltyCredits,
		"chart_hull_cap":                   defaultChartHullCap,
		"chart_hull_2_at":                  defaultSecondChartHullAt,
		"chart_hull_3_at":                  defaultThirdChartHullAt,

		// The API client's limiter-pressure EWMA half-life. 30 mirrors the
		// client's own default (api.defaultLimiterPressureHalfLife); the
		// application layer cannot import the adapter, so the value is pinned
		// here and guarded by the tune-registry drift test.
		"pressure_half_life_secs": 30,
	}
}
