package grpc

// This file holds the [manufacturing] launch-config plumbing SHARED with the construction-supply
// drain. The goods_factory_coordinator's own working-capital/guard-knob resolver+injector was
// retired with the factory ops (sp-hoj8u); only the construction-side resolver below remains — it
// threads the unified gate-fill toggle into the drain. The per-supplyTask timeout knob was retired
// by sp-sxyx6 — the drain always uses its 30m const default now.
//
// resolveConstructionUnifiedGateFill threads the [manufacturing] unified_gate_fill toggle into the
// construction-supply drain (sp-vh1s): the drain's RunConstructionCoordinatorCommand carries only
// UnifiedGateFill (it derives its own construction site per-task), so this injects ONLY that rather
// than the full goods_factory config. Cleared then reinjected so config.yaml is the single live
// source of truth (sp-ts82): dropping the toggle reverts a recovered drain to OFF. Absent/false
// injects nothing → byte-identical to today.
func (s *DaemonServer) resolveConstructionUnifiedGateFill(config map[string]interface{}) {
	delete(config, "unified_gate_fill")
	if s.manufacturingConfig.UnifiedGateFill {
		config["unified_gate_fill"] = true
	}
}
