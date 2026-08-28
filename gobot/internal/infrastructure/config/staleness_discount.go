package config

import "github.com/andrescamacho/spacetraders-go/internal/domain/trading"

// StalenessDiscountConfig is the [trading] staleness_discount tune table: how hard the
// lane ranker and the tour snapshot charge a quote for its age. The per-activity drift
// coefficients are a FIT, not knobs — refitting means re-running the estimation, not
// typing a number at 4am — so the one operational dial is how hard to charge the curve
// (RULINGS #5 as bounded).
type StalenessDiscountConfig struct {
	// ScalePct scales the whole fitted table: above 100 charges old quotes harder, below
	// 100 softer, zero/absent reverts to the fitted default.
	ScalePct int `mapstructure:"scale_pct"`

	// Disabled is the kill switch and the only route to a discount of zero. It defaults
	// off, so the discount ships ARMED (RULINGS #22).
	Disabled bool `mapstructure:"disabled"`
}

// Resolved converts the configured knobs into the domain discount, defaulting from
// trading.DefaultStalenessDiscountScalePct so the number is defined once.
func (c StalenessDiscountConfig) Resolved() trading.StalenessDiscount {
	scale := c.ScalePct
	if scale <= 0 {
		scale = trading.DefaultStalenessDiscountScalePct
	}
	return trading.StalenessDiscount{ScalePct: scale, Disabled: c.Disabled}
}
