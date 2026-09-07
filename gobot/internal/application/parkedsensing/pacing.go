package parkedsensing

import "github.com/andrescamacho/spacetraders-go/internal/domain/trading"

// pacing.go scales this engine's per-tick BURST budgets with the request budget nobody is
// queued on: each pass is bounded by a constant sized for a fleet AT its ceiling, where
// holding a burst down is right and off it is throughput thrown away. THE SCALING IS
// ONE-WAY — the base or more, never less — and moves PACING alone: no spend gate, buy floor
// or working-capital reserve is reachable from this file (RULINGS #4).

// ExpansionHeadroomMultiple is how many times its ceiling-era budget a pass may spend
// against a fully idle request budget, and the default for expansion_headroom_multiple.
//
// SIX, DERIVED FROM THE MAP: the gate read decides how fast new systems are found at all,
// and 3 reads per 30s tick discovers 360 an hour against ~9,900 gate-reachable ones; six
// times that at 6% saturation is 17 a tick, ~2,000 an hour — which charts the map inside
// one era rather than across several. MaxExpansionHeadroomMultiple is the knob's advertised
// maximum, clamped here too so a hand-written row cannot reach a multiple nobody sized.
const (
	ExpansionHeadroomMultiple    = 6
	MaxExpansionHeadroomMultiple = 20
)

// PacedBudget is one pass's per-tick budget scaled by the idle share of the request budget:
// base at a fully committed one, base*headroomMultiple at 0, linear between and truncated
// down — base * (1000 + (m-1)*(1000-permille)) / 1000. A NON-POSITIVE base IS THE CALLER'S
// OWN "use your documented default" SENTINEL, returned untouched, since resolving it here
// would put a second default in front of the pass's own; a non-positive headroomMultiple is
// the constant above, and permille clamps into range.
func PacedBudget(base, saturationPermille, headroomMultiple int) int {
	if base <= 0 {
		return base
	}
	if headroomMultiple < 1 {
		headroomMultiple = ExpansionHeadroomMultiple
	}
	if headroomMultiple > MaxExpansionHeadroomMultiple {
		headroomMultiple = MaxExpansionHeadroomMultiple
	}

	permille := saturationPermille
	if permille < 0 {
		permille = 0
	}
	if permille > trading.APISaturationPermilleMax {
		permille = trading.APISaturationPermilleMax
	}

	idle := trading.APISaturationPermilleMax - permille
	scaled := base * (trading.APISaturationPermilleMax + (headroomMultiple-1)*idle) / trading.APISaturationPermilleMax
	if scaled < base {
		// Unreachable above; stated because an edit that broke it would silently pace the
		// engine BELOW what it shipped, and nothing else would fail.
		return base
	}
	return scaled
}
