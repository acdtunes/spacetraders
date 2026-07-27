package parkedsensing

// budget_ports.go is the sensing loop's window onto the shared API rate limiter:
// how much of the ceiling everyone else is already using, so the coordinator can
// size sensing as the RESIDUAL rather than as an appetite of its own.
//
// The adapter exists because the two facts it reports live on opposite sides of
// the architecture — the ceiling is the API client's, the observed spend is the
// metrics tracker's — and the application layer may import neither.

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

// budgetTracker is the live event tracker, narrowed to the one query the budget
// arithmetic needs. *metrics.APIBudgetTracker satisfies it, and a nil tracker
// answers zero rather than panicking — which reads as "nobody else is spending"
// and is the permissive direction, so the port is never wired nil in production.
type budgetTracker interface {
	NonSourceRate(window time.Duration, excluded ...apibudget.Source) float64
}

// BudgetRatePort reports the fleet's observed API spend, split the two ways the
// sensing budget needs it.
type BudgetRatePort struct {
	tracker budgetTracker
	ceiling float64
}

// NewBudgetRatePort wires the budget reads against the sustained rate-limiter
// ceiling (api.RateLimitPerSecond at the composition root).
func NewBudgetRatePort(tracker budgetTracker, ceilingReqPerSec float64) *BudgetRatePort {
	return &BudgetRatePort{tracker: tracker, ceiling: ceilingReqPerSec}
}

// CeilingReqPerSec is the hard sustained rate-limiter ceiling.
func (p *BudgetRatePort) CeilingReqPerSec() float64 { return p.ceiling }

// NonSensingRate is the req/s spent by every source that is neither scanning nor
// charting — trading, contracts, navigation, bootstrap, and anything untagged.
//
// It is what the utilization target is measured DOWN from, so the two sensing
// sources are excluded and everything else counts. Untagged calls count against
// us deliberately: an untagged path can only shrink the sensing budget, never
// inflate it.
func (p *BudgetRatePort) NonSensingRate(window time.Duration) float64 {
	if p.tracker == nil {
		return 0
	}
	return p.tracker.NonSourceRate(window, apibudget.SourceScanning, apibudget.SourceCharting)
}

// ChartingRate is the req/s spent on charting.
//
// It is derived by SUBTRACTION — every attempt, less every attempt that is not
// charting — rather than by enumerating the other sources. The enumeration would
// read more directly and would be wrong the moment a source is added to the
// taxonomy: a new source absent from the exclusion list would be silently
// counted as charting, inflating the number the pacer concedes and shrinking
// sensing for a reason nobody could see. The subtraction cannot drift, because
// it never names a source but the one it is measuring.
//
// UNTAGGED attempts cancel exactly. NonSourceRate counts them as non-excluded in
// both reads (they are only ever dropped by naming SourceUnspecified, which
// neither call here does), so they appear in the total and in the non-charting
// figure alike and subtract away — leaving precisely the charting-tagged rate,
// with no untagged residue leaking into it.
//
// The two reads are separate lock acquisitions, so an attempt recorded between
// them can make the difference fractionally negative. That is floor-clamped:
// charting spend is never below zero, and a sub-tick race must not be allowed to
// hand the pacer a bonus by reading as one.
func (p *BudgetRatePort) ChartingRate(window time.Duration) float64 {
	if p.tracker == nil {
		return 0
	}
	total := p.tracker.NonSourceRate(window)
	nonCharting := p.tracker.NonSourceRate(window, apibudget.SourceCharting)
	if charting := total - nonCharting; charting > 0 {
		return charting
	}
	return 0
}
