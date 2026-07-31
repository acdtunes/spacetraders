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
	TourID     string // the tour_run container id — groups a tour's legs
	ShipSymbol string

	// Engine names WHICH execution path produced this leg, DECLARED by that path rather
	// than inferred from the row (sp-fzt09). It is the attribution column: every leg with
	// realized cargo says who made it, so a reader never has to recognise an engine by the
	// shape of its data.
	//
	// WHY IT EXISTS. A quarter of realized sell legs carry PlannedUnits=0 and a zero
	// PlannedUnitPrice, and an analysis that averaged those zeros into planned-vs-realized
	// concluded the solver used 36.7% of available market depth — a finding that was
	// withdrawn, because the zeros are liquidations that never had a plan. The engine
	// identity WAS recoverable, but only from LegIndex sentinels: one of them
	// (LiquidationLegIndexBase) was unexported in an application package and so invisible
	// to SQL, which left `planned_unit_price > 0` as the proxy every reader reached for.
	// That proxy is coincidence, not attribution — it happens to select solver legs today
	// and would silently mis-select the moment any engine records a basis.
	//
	// Read it in SQL as `WHERE engine = 'solver'` for planner accuracy. It is stamped at
	// the call site so a NEW execution path must name itself to compile, rather than
	// inheriting an engine from whatever LegIndex it happened to pass.
	Engine LegEngine

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

// LegEngine names an execution path that writes tour leg telemetry. The vocabulary is
// CLOSED — these three are every path that calls RecordLeg — and the values are the
// literals stored in the engine column, so a SQL reader and a Go reader agree by
// construction.
type LegEngine string

const (
	// LegEngineSolver is the tour planner's own plan leg: the solver chose the good, the
	// units and the expected price, so this population — and only this one — is evidence
	// about the market model.
	LegEngineSolver LegEngine = "solver"

	// LegEngineLookback is a look-back manifest buy at the reposition seam (sp-rd21). It
	// carries a plan basis, but a CACHED SourceAsk rather than the solver's projection,
	// and the buy is gated to a tolerance band around that very number — so it converges
	// on 0% error and must never be pooled with solver legs (sp-fpgl2).
	LegEngineLookback LegEngine = "lookback"

	// LegEngineLiquidation is a distress dump or exit sweep (sp-xfrfw): cargo sold to free
	// a hull, with no solver plan behind it. It deliberately records a ZERO basis rather
	// than inventing one, so it is a real trade that is correctly absent from every
	// planned-vs-realized measurement — and, before sp-fzt09, was indistinguishable in SQL
	// from a solver leg whose plan had failed to persist.
	LegEngineLiquidation LegEngine = "liquidation"
)

// LiquidationLegIndexBase is the LegIndex floor stamped on a liquidation sale. It lives in
// the domain for the same reason LookbackLegIndex does — it is read on both sides — and
// because the persistence layer needs it to attribute rows written before the engine column
// existed. run_tour_coordinator_distress.go aliases this rather than keeping a second copy.
const LiquidationLegIndexBase = 1_000_000

// EngineForLegIndex recovers the engine from a LegIndex sentinel.
//
// THIS IS FOR HISTORICAL ROWS ONLY — the one-time backfill of rows written before the engine
// column, and the test oracle that asserts each call site's DECLARED engine matches the index
// class it stamps. Live paths must declare their engine; a derived identity cannot see a path
// that has not yet chosen a sentinel, so a new engine defaulting through here would be
// misfiled as solver rather than failing loudly.
//
// The derivation is exact on the historical data it was written for: over 45,466 production
// rows the three classes partition without a single exception — every index at or above
// LiquidationLegIndexBase carried a zero basis and was a sell, every LookbackLegIndex row
// carried a basis and was a buy, and no plan-position leg ever lacked a plan.
func EngineForLegIndex(legIdx int) LegEngine {
	switch {
	case legIdx >= LiquidationLegIndexBase:
		return LegEngineLiquidation
	case legIdx == LookbackLegIndex:
		return LegEngineLookback
	default:
		return LegEngineSolver
	}
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
