package config

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

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
	// SinkDepthScaling conditions the crush prior on the sink's listing breadth.
	// Absent/disabled runs the uniform prior, which is what every value here is
	// measured against.
	SinkDepthScaling SinkDepthScalingConfig `mapstructure:"sink_depth_scaling"`
}

// SinkDepthScalingConfig is the refit surface for the depth-conditioned crush prior.
// It ships DISABLED: arming it is a separate step, and the shape terms are per-era
// refits an operator retunes from observed sink behaviour.
type SinkDepthScalingConfig struct {
	// Enabled arms the prior. False (the default) is byte-identical to the uniform model.
	Enabled bool `mapstructure:"enabled"`
	// ThinListings is the breadth at or under which a sink keeps the full crush claim.
	// 0 → the shipped fit.
	ThinListings int `mapstructure:"thin_listings"`
	// MinCrushScale floors the discount a broad market may earn. 1.0 reproduces the
	// uniform prior with the feature armed — the documented revert. 0 → the shipped fit.
	MinCrushScale float64 `mapstructure:"min_crush_scale"`
}

// ResolvedSinkDepthScaling returns the ledger-facing policy, filling each unset shape
// term with its shipped fit. Arming without a shape must not resolve to a zero shape:
// that reads as the uniform prior and would make the arm silently inert.
func (c AbsorptionConfig) ResolvedSinkDepthScaling() absorption.SinkDepthScaling {
	policy := absorption.SinkDepthScaling{
		Enabled:       c.SinkDepthScaling.Enabled,
		ThinListings:  c.SinkDepthScaling.ThinListings,
		MinCrushScale: c.SinkDepthScaling.MinCrushScale,
	}
	if policy.ThinListings <= 0 {
		policy.ThinListings = absorption.DefaultThinListings
	}
	if policy.MinCrushScale <= 0 {
		policy.MinCrushScale = absorption.DefaultMinCrushScale
	}
	return policy
}
