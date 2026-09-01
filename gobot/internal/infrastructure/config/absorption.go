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
	ExecutedHardCap time.Duration `mapstructure:"executed_hard_cap"`
	// ShadowFloorFraction is the fraction of one tranche of still-occupied depth below
	// which a recovering shadow stops blocking a new sell. 0 → the package default.
	ShadowFloorFraction float64 `mapstructure:"shadow_floor_fraction"`
	// PlannedTTLSlack pads a PLANNED hold's projected round-trip TTL — the backstop to
	// the dead-container reclaim, not the primary cleanup. 0 → 15m default.
	PlannedTTLSlack time.Duration `mapstructure:"planned_ttl_slack"`
	// BuyShadowLife bounds how long the fleet's own purchases keep occupying a SOURCE's
	// depth — ExecutedHardCap's buy-side counterpart. 0 → the package default.
	BuyShadowLife time.Duration `mapstructure:"buy_shadow_life"`
	// SinkDepthScaling conditions the crush prior on the sink's listing breadth. An absent
	// section runs the shipped fit.
	SinkDepthScaling SinkDepthScalingConfig `mapstructure:"sink_depth_scaling"`
}

// SinkDepthScalingConfig is the operator surface for the depth-conditioned crush prior: a kill
// switch plus the two shape terms, which are per-era refits retuned from observed sink behaviour.
type SinkDepthScalingConfig struct {
	// Enabled is the kill switch. ABSENT MEANS ON — the prior is in force with no config
	// present — and only an explicit false disables it, charging every sink the full claim.
	Enabled *bool `mapstructure:"enabled"`
	// ThinListings is the breadth at or under which a sink keeps the full crush claim.
	// 0 → the shipped fit.
	ThinListings int `mapstructure:"thin_listings"`
	// MinCrushScale floors the discount a broad market may earn. 1.0 flattens the prior to the
	// uniform model without disabling the mechanism. 0 → the shipped fit.
	MinCrushScale float64 `mapstructure:"min_crush_scale"`
}

// ResolvedSinkDepthScaling returns the ledger-facing policy, filling each unset shape term with
// its shipped fit. An unset shape must not resolve to a zero shape: that reads as the uniform
// prior and would make the default silently inert.
func (c AbsorptionConfig) ResolvedSinkDepthScaling() absorption.SinkDepthScaling {
	policy := absorption.SinkDepthScaling{
		Enabled:       c.SinkDepthScaling.Enabled == nil || *c.SinkDepthScaling.Enabled,
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
