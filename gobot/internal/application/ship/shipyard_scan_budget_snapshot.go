package ship

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// YardBudgetSnapshot is a point-in-time read of the budget, for metrics and the
// operator-facing report.
type YardBudgetSnapshot struct {
	RateReqPerSec float64
	ValueClampR   int
	YardsKnown    int
	// YardsWanted is how many known yards sell something the fleet is shopping
	// for, and YardsUnpriced how many of those we hold no price for. The second
	// number IS the incident this budget was written for: it stood at 80 of 84
	// when the cadence floor was the only pacing, and a rotation that is working
	// drives it toward zero.
	YardsWanted     int
	YardsUnpriced   int
	TotalWeight     float64
	TokensAvailable float64
	// TypicalInterval is how long a baseline yard currently waits between reads —
	// the number that grows as the map grows while the rate stays put, and the
	// most direct read on whether the fixed-budget invariant holds.
	TypicalInterval time.Duration
	// WorstCaseStaleness is the anti-starvation bound at the current map size.
	WorstCaseStaleness time.Duration
	Admitted           uint64
	Declined           uint64
	// Forced counts admitted reads that found the bucket already empty: money
	// guards and starvation escapes. A persistently high count means the fixed
	// budget is smaller than the fleet's unavoidable pre-buy verification and
	// should be raised, rather than the guards being weakened (RULINGS #4).
	Forced uint64
	// YardsNeedingPresence is how many known yards sell something wanted, hold no
	// price, and therefore cannot be priced by any amount of reading.
	//
	// It is the honest denominator beside YardsUnpriced: the two are the same set,
	// but this name says why the rotation alone will never drive it down. A working
	// presence path drives it toward zero; a flat reading beside a rising
	// PresenceDeclined means the allowance is the binding constraint, and a flat
	// reading beside a flat PresenceIssued means no hull could be spared.
	YardsNeedingPresence int
	// PresenceIssued and PresenceDeclined count repositions started and refused
	// for want of allowance.
	PresenceIssued   uint64
	PresenceDeclined uint64
}

// Snapshot reports the budget's current state.
func (b *YardScanBudget) Snapshot() YardBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	totalWeight, yardsKnown := b.aggregateLocked()
	wanted, unpriced, needsPresence := 0, 0, 0
	for _, f := range b.facts {
		if yardscan.WantsPresence(f) {
			needsPresence++
		}
		if !f.SellsWanted {
			continue
		}
		wanted++
		if !f.Priced {
			unpriced++
		}
	}
	return YardBudgetSnapshot{
		RateReqPerSec:        b.policy.RateReqPerSec,
		ValueClampR:          b.policy.ValueClampR,
		YardsKnown:           yardsKnown,
		YardsWanted:          wanted,
		YardsUnpriced:        unpriced,
		TotalWeight:          totalWeight,
		TokensAvailable:      b.limiter.TokensAt(b.now()),
		TypicalInterval:      marketscan.Interval(b.policy, yardscan.Baseline, totalWeight),
		WorstCaseStaleness:   marketscan.MaxStaleness(b.policy, yardsKnown),
		Admitted:             b.admitted,
		Declined:             b.declined,
		Forced:               b.forced,
		YardsNeedingPresence: needsPresence,
		PresenceIssued:       b.presenceIssued,
		PresenceDeclined:     b.presenceDeclined,
	}
}

// RotationInputs reports the allowance and the live map size behind it — the two
// numbers marketscan.MaxStaleness needs to say how old a cached yard row may be
// before it is older than this rotation can explain. A nil budget reports a zero
// map, which callers answer with their own floor rather than with a widened cap.
func (b *YardScanBudget) RotationInputs(ctx context.Context) (marketscan.Budget, int) {
	if b == nil {
		return marketscan.Budget{}, 0
	}
	b.refreshChartedCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	_, yardsKnown := b.aggregateLocked()
	return b.policy, yardsKnown
}

// aggregateLocked returns the summed weight of every known yard and the map size,
// recomputing them only when an observation, a demand change or a new yard has
// invalidated the cache.
//
// Yards nothing is known about are counted at the optimistic prior, not at zero.
// Omitting them would understate the denominator and hand every known yard a
// shorter interval than the budget can fund — the map would be paced as though the
// yards nobody has opened did not have to be opened.
func (b *YardScanBudget) aggregateLocked() (totalWeight float64, yardsKnown int) {
	known := len(b.seen)
	if b.charted > known {
		known = b.charted
	}

	if !b.aggregateStale {
		return b.totalWeight, known
	}

	total := 0.0
	described := 0
	for waypoint, f := range b.facts {
		if described >= known {
			break
		}
		f.Targeted = b.targetedLocked(waypoint)
		total += yardscan.Weight(f, b.policy.ValueClampR)
		described++
	}
	if rest := known - described; rest > 0 {
		total += float64(rest) * yardscan.PriorWeight(b.policy.ValueClampR)
	}
	b.totalWeight = total
	b.aggregateStale = false

	return b.totalWeight, known
}

// weightLocked is one yard's current value weight, or the optimistic prior when
// nothing is known about it.
func (b *YardScanBudget) weightLocked(waypoint string, now time.Time) float64 {
	f, ok := b.facts[waypoint]
	if !ok {
		f = yardscan.Facts{Unknown: true}
	}
	if at, targeted := b.target[waypoint]; targeted && now.Sub(at) < yardTargetTTL {
		f.Targeted = true
	}
	return yardscan.Weight(f, b.policy.ValueClampR)
}
