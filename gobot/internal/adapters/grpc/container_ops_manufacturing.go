package grpc

// This file holds the [manufacturing] launch-config plumbing SHARED with the construction-supply
// drain. The goods_factory_coordinator's own working-capital/guard-knob resolver+injector was
// retired with the factory ops (sp-hoj8u); only the construction-side resolver below remains — it
// threads the unified gate-fill toggle and the fabrication-efficiency feeding policy into the drain.

// resolveConstructionUnifiedGateFill threads the [manufacturing] unified_gate_fill toggle into
// the construction-supply drain (sp-vh1s): the drain's RunConstructionCoordinatorCommand carries only
// UnifiedGateFill (it derives its own construction site per-task), so this deliberately injects ONLY
// that toggle plus the shared feeding-efficiency policy rather than the full goods_factory config —
// leaving the drain's launch-config production_strategy untouched (a construction behavior unrelated
// to this plumbing). Cleared then reinjected so config.yaml is the single live source of truth
// (sp-ts82): dropping the toggle reverts a recovered drain to OFF. Absent/false injects nothing →
// byte-identical to today.
func (s *DaemonServer) resolveConstructionUnifiedGateFill(config map[string]interface{}) {
	delete(config, "unified_gate_fill")
	delete(config, "fabrication_efficiency")
	delete(config, "feed_saturation_max_units")
	delete(config, "feed_saturation_min_units")
	delete(config, "feed_non_responsive_goods")
	delete(config, "construction_supply_task_timeout_seconds")
	if s.manufacturingConfig.UnifiedGateFill {
		config["unified_gate_fill"] = true
	}
	// sp-ubwi: the per-supplyTask timeout, resolved fresh each build so a config edit + restart retunes a
	// recovered drain (sp-ts82). 0/absent injects nothing → the drain's raised 30m default.
	if s.manufacturingConfig.ConstructionSupplyTaskTimeoutSeconds > 0 {
		config["construction_supply_task_timeout_seconds"] = s.manufacturingConfig.ConstructionSupplyTaskTimeoutSeconds
	}
	// sp-to2v: the drain sources gate materials directly on the shared executor, so it gets the SAME
	// [manufacturing] feeding-efficiency policy (toggle + saturation coefficients + non-responsive set)
	// as a goods factory did, resolved fresh each build so a config edit + restart flips a recovered
	// drain (sp-ts82). Absent/false/0 injects nothing → the greedy byte-identical feeding.
	if s.manufacturingConfig.FabricationEfficiency {
		config["fabrication_efficiency"] = true
	}
	if s.manufacturingConfig.FeedSaturationMaxUnits != 0 {
		config["feed_saturation_max_units"] = s.manufacturingConfig.FeedSaturationMaxUnits
	}
	if s.manufacturingConfig.FeedSaturationMinUnits != 0 {
		config["feed_saturation_min_units"] = s.manufacturingConfig.FeedSaturationMinUnits
	}
	if len(s.manufacturingConfig.FeedNonResponsiveGoods) > 0 {
		config["feed_non_responsive_goods"] = s.manufacturingConfig.FeedNonResponsiveGoods
	}
}
