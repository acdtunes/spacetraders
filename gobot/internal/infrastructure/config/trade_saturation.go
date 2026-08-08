package config

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// TradeSaturationConfig is the [trading] trade_saturation table: the two knobs behind the growth
// wave's SWITCH-BACK cue, the point at which a fleet that has outgrown its reachable surface stops
// demanding heavies and grows in coverage instead (RULINGS #5 — the boundary is operational, so it
// is config rather than a constant, and the DEFAULT NUMBERS are defined once in domain/fleetgrowth).
//
// BOTH KNOBS ONLY EVER MOVE A BOUNDARY, never a money floor: the term they configure can only
// SUBTRACT heavy ticks, which is why they are tunable at all where the reserve floors are not.
type TradeSaturationConfig struct {
	// MarginPct is the fraction of the trade pool's hold, in percent, that the reachable surface
	// must EXCEED for the fleet to still count as capacity-short. Raising it declares saturation
	// earlier, against a smaller fleet; lowering it permits more heavies against the same surface.
	MarginPct int `mapstructure:"margin_pct"`

	// DwellSeconds is how long a CHANGED verdict must hold continuously before it is published —
	// the anti-thrash window against a depth reading that jitters across the boundary. SECONDS
	// rather than a raw Duration because a captain's retune of a window sized to a coordinator tick
	// is naturally expressed that way.
	DwellSeconds int `mapstructure:"dwell_seconds"`
}

// Resolved returns the margin and the dwell with each unset (non-positive) knob filled from its
// documented default. The reader defaults AGAIN on use rather than trusting this: the two values an
// unset knob carries — a 0% margin and a zero window — are precisely the two that switch the term
// off, so no single forgotten wiring may be able to produce them.
func (c TradeSaturationConfig) Resolved() (marginPct int, dwell time.Duration) {
	marginPct = c.MarginPct
	if marginPct <= 0 {
		marginPct = fleetgrowth.DefaultSaturationMarginPct
	}
	dwell = time.Duration(c.DwellSeconds) * time.Second
	if dwell <= 0 {
		dwell = fleetgrowth.DefaultSaturationDwell
	}
	return marginPct, dwell
}
