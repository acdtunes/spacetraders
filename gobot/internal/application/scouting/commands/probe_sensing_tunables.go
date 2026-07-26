package commands

// SensingTunableDefaults maps every LIVE-tunable probe-sensing knob to its
// documented default, sourced from the coordinator's own consts so the tune
// registry can never drift from the behavior (the SizerTunableDefaults
// discipline). goods_whitelist is deliberately absent: the int-only tune
// mechanism carries no strings, so the whitelist is operated through the
// [sensing] config.yaml section instead.
func SensingTunableDefaults() map[string]int {
	return map[string]int{
		"depth_floor":                 defaultSensingDepthFloor,
		"probe_budget":                defaultSensingProbeBudget,
		"second_probe_threshold":      defaultSensingSecondProbeThreshold,
		"purchase_cooldown_secs":      defaultSensingPurchaseCooldownSecs,
		"tick_secs":                   defaultSensingTickSeconds,
		"wait_low_ms":                 defaultSensingWaitLowMs,
		"wait_high_ms":                defaultSensingWaitHighMs,
		"freshness_target_secs":       defaultSensingFreshnessTargetSecs,
		"max_spend_per_cycle":         defaultSensingMaxSpend,
		"spend_window_secs":           defaultSensingSpendWindowSecs,
		"discovery_declares_per_tick": defaultSensingDiscoveryDeclares,
		// The API client's limiter-pressure EWMA half-life. 30 mirrors the
		// client's own default (api.defaultLimiterPressureHalfLife); the
		// application layer cannot import the adapter, so the value is pinned
		// here and guarded by the tune-registry drift test.
		"pressure_half_life_secs": 30,
	}
}
