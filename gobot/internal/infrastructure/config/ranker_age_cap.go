package config

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TradingConfig holds the trade-ranker knobs read at daemon start. Its only member
// today is the activity-conditioned freshness table (sp-t5sh5); it exists as its own
// [trading] section so the ranker's tune knobs live under one intuitive key rather
// than being folded into the era-refit [trade_impact] coefficients.
type TradingConfig struct {
	// RankerAgeCapMinutes is the per-activity freshness cap (in MINUTES) the UNDIRECTED
	// lane ranker and the tour snapshot drop stale listings against. Minutes (not a raw
	// Duration) because the analyst's fit and a captain's retune are naturally expressed
	// in minutes. An absent section / zero field defers to the fitted armed default for
	// that activity (trading.DefaultRankerAgeCap*), so the ranker runs the analyst's
	// era3/4 fit unchanged out of the box.
	RankerAgeCapMinutes RankerAgeCapConfig `mapstructure:"ranker_age_cap_minutes"`
}

// RankerAgeCapConfig is the [trading] ranker_age_cap_minutes tune table: one freshness
// cap per market activity level, in minutes. A zero/absent field resolves to that
// activity's fitted default (RULINGS #5 — the caps are config, not a hardcoded 4-way
// constant, and the defaults are DEFINED ONCE in domain/trading).
type RankerAgeCapConfig struct {
	Weak       int `mapstructure:"weak"`
	Restricted int `mapstructure:"restricted"`
	Growing    int `mapstructure:"growing"`
	Strong     int `mapstructure:"strong"`
}

// Resolved converts the configured per-activity minutes into the domain freshness
// table, filling each unset (non-positive) knob from that activity's fitted default.
// The default NUMBERS come from trading.DefaultRankerAgeCap* — the single source both
// this resolver and RankerAgeCaps.For's zero-value defense read, so the table stays
// "defined once" no matter which path populates it.
func (c RankerAgeCapConfig) Resolved() trading.RankerAgeCaps {
	return trading.RankerAgeCaps{
		Weak:       minutesOrDefault(c.Weak, trading.DefaultRankerAgeCapWeak),
		Restricted: minutesOrDefault(c.Restricted, trading.DefaultRankerAgeCapRestricted),
		Growing:    minutesOrDefault(c.Growing, trading.DefaultRankerAgeCapGrowing),
		Strong:     minutesOrDefault(c.Strong, trading.DefaultRankerAgeCapStrong),
	}
}

// minutesOrDefault returns the configured minutes as a Duration when set (positive),
// else the fitted default duration.
func minutesOrDefault(minutes int, fitted time.Duration) time.Duration {
	if minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	return fitted
}
