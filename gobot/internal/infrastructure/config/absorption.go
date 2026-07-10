package config

import "time"

// AbsorptionConfig holds the cross-engine market-absorption ledger knobs (sp-78ai,
// trade-analyst Q2 rulings). Every field is a flag (RULINGS #5) and every field is
// OPTIONAL: a zero value takes the code default, so an absent [absorption] section
// runs exactly the analyst-ruled defaults. The daemon resolves the ledger's recovery
// artifact from the existing routing.model_artifact_path, so no path lives here.
type AbsorptionConfig struct {
	// ExecutedHardCap bounds an EXECUTED recovery shadow's life regardless of decay.
	// Trade-analyst Q2: 12h (NOT 24h — 24h is >half the remaining era). 0 → 12h default.
	ExecutedHardCap time.Duration `mapstructure:"executed_hard_cap"`
	// ShadowFloorFraction is the fraction of one tranche of still-occupied depth below
	// which a recovering shadow stops blocking a new sell. Trade-analyst Q2: 0.5.
	// 0 → 0.5 default.
	ShadowFloorFraction float64 `mapstructure:"shadow_floor_fraction"`
	// PlannedTTLSlack pads a PLANNED hold's projected round-trip TTL — the backstop to
	// the dead-container reclaim, not the primary cleanup. 0 → 15m default.
	PlannedTTLSlack time.Duration `mapstructure:"planned_ttl_slack"`
	// IdleArbConsultDisabled kills the idle-arb skip:reserved consult (recording still
	// runs, so the ledger keeps serving other engines) — the operator escape hatch if
	// the consult ever over-skips. Default false (consult on).
	IdleArbConsultDisabled bool `mapstructure:"idle_arb_consult_disabled"`
}
