package trading

import (
	"context"
	"time"
)

// TourLegTelemetry is one planned-vs-realized record for a single trade at a single
// leg of a multi-hop trade tour (sp-1ek0 P1b). The tour_run executor emits one per
// executed (or explicitly skipped) trade so the graduation-gate report can measure
// the median |planned−realized|/planned price error — the gate metric that proves
// the market model, not merely that the tour turned a profit. A skipped/degraded
// trade records RealizedUnits=0 with a zero RealizedAt.
//
// This is the domain-level DTO the executor and report speak; the persistence layer
// maps it to its own row model, keeping the application decoupled from GORM (the same
// dependency-inversion the coordinators already use for ship/market repositories).
type TourLegTelemetry struct {
	TourID        string // the tour_run container id — groups a tour's legs
	ShipSymbol    string
	LegIndex      int
	Waypoint      string
	Good          string
	IsBuy         bool
	PlannedUnits  int
	RealizedUnits int

	// PlannedUnitPrice is the plan basis the leg was expected to trade at, and ZERO means
	// "no basis" rather than "free". A distress liquidation has no solver plan behind it and
	// deliberately records zero rather than inventing a number — a fabricated basis would
	// silently corrupt planned-vs-realized, so the zero is correct and not an omission.
	//
	// EVERY reader must therefore exclude a non-positive basis. In SQL that means dividing
	// through NULLIF(planned_unit_price, 0). THE TRAP: a value-weighted mean written as the
	// natural SUM(...)/SUM(...) does NOT skip those rows — they contribute realized value to
	// the NUMERATOR while adding zero to the denominator, which inflated an hourly
	// value-weighted drift to 11-42% against a true sub-1% (sp-fpgl2). Zero-basis rows are
	// ~19% of legs, so this is not a rounding concern. Filter them in the WHERE clause; do
	// not rely on the division to drop them.
	PlannedUnitPrice int

	RealizedUnitPrice int

	// PlannedAt is when EXECUTION OF THIS LEG STARTED — not when the plan was made. The
	// name is a historical misnomer and the value is deliberate; do not "fix" it.
	//
	// The executor stamps it immediately before the trade, so it sits a median 2 seconds
	// before RealizedAt (p90 7s). PLAN-VS-REALIZED TIME IS THEREFORE NOT MEASURABLE FROM
	// THIS TABLE, and reading the two columns as a plan-to-execution latency yields a
	// tautology — sp-fpgl2 was raised on exactly that reading. Plan STALENESS would need a
	// separate solve timestamp, which nothing records today.
	//
	// WHY IT MUST KEEP MEANING EXECUTION-START. MedianTourRate takes min(PlannedAt) as a
	// tour's span START and divides realized net by that span (see tour_rate.go legGroup).
	// That rate is β, and run_tour_coordinator_rate_floor.senseRateFloor MAY RELOCATE a
	// hull earning below a fraction of it. Re-stamping this field with a true solve time
	// would move every span start earlier, deflate $/hr fleet-wide, and trip that trigger
	// on a measurement artifact; it would also collapse all of a plan's legs onto one
	// instant, destroying the incremental per-leg spread the window logic relies on (25 of
	// 27 live tours spread their PlannedAt by more than a minute). The freshness sizer and
	// ListByPlayer's window filter read it the same way.
	//
	// PlannedUnitPrice, by contrast, IS genuinely solve-time: it is the planner's
	// ExpectedUnitPrice, written once from the routing response and never re-priced. The
	// price comparison is honest even though the timestamps cannot support one.
	PlannedAt time.Time

	// RealizedAt is when the trade completed. A skipped/degraded trade leaves it zero.
	RealizedAt time.Time

	PlayerID int
}

// LookbackLegIndex is the sentinel LegIndex stamped on a look-back manifest buy (sp-rd21):
// an opportunistic pre-jump load at the reposition seam, not a solver-plan position.
//
// It lives on the domain DTO because it is now read on BOTH sides — the executor stamps it,
// and the graduation report classifies plan basis by it — and a second copy of the literal
// would be free to drift away from this one.
const LookbackLegIndex = -1

// IsLookbackManifestLeg reports whether this row's PlannedUnitPrice came from the look-back
// manifest's CACHED SourceAsk rather than the solver's own projection.
//
// The distinction decides whether a row is evidence about the market model. A look-back buy
// is gated to a tolerance band around the very ask it is compared against, so a fresh cache
// reproduces itself: these legs measured a median absolute price error of EXACTLY 0.000%
// over 1423 production rows while solver legs measured 0.518% (sp-fpgl2). Pooled, they
// report a number that describes neither population.
func (l TourLegTelemetry) IsLookbackManifestLeg() bool {
	return l.LegIndex == LookbackLegIndex
}

// TourTelemetryRepository persists per-leg tour telemetry and reads it back for the
// graduation report. Implemented by the persistence layer.
type TourTelemetryRepository interface {
	// RecordLeg persists one planned-vs-realized trade record.
	RecordLeg(ctx context.Context, leg TourLegTelemetry) error
	// ListByPlayer returns playerID's telemetry rows whose PlannedAt is at or after
	// since, in execution order (a zero since returns the full history).
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]TourLegTelemetry, error)
}
