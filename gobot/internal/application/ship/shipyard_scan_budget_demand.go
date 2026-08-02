package ship

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardDemandTTL is how long a hull type stays "wanted" after something last
// shopped for it.
//
// Demand is inferred from the budget's own traffic rather than declared by the buy
// loops, so it needs a decay or a single historical purchase would pin a type
// wanted forever. An hour is long enough to span a buy campaign's gaps — an
// autosizer that prices a heavy, waits for treasury, and prices again — and short
// enough that a type the fleet has stopped buying stops claiming the allowance.
const yardDemandTTL = time.Hour

// yardTargetTTL is how long a yard stays "targeted" after a money guard priced a
// hull there. Shorter than the demand window because it names a specific counter:
// it keeps the rotation warm across the navigate-dock-verify-buy sequence, not
// across a campaign.
const yardTargetTTL = 15 * time.Minute

// NoteDemand records that something is shopping for this hull type, so every yard
// known to sell it rises in the rotation.
//
// Demand is INFERRED from the budget's own traffic rather than declared by the buy
// loops, and that is deliberate: every path that prices a hull already has to name
// the type it is pricing, so the signal is free and cannot drift out of sync with
// what the fleet is actually buying. A buy path added tomorrow contributes demand
// without knowing this budget exists.
func (b *YardScanBudget) NoteDemand(shipType string) {
	if shipType == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, known := b.demand[shipType]; !known {
		// A newly wanted type changes which yards count as valuable, so the cached
		// total is stale and the store picture must be rebuilt against the new set.
		b.aggregateStale = true
		b.catalogAt = time.Time{}
	}
	b.demand[shipType] = b.now()
}

// NoteTarget records that a money guard priced a hull at this yard — the fleet is
// buying HERE, not merely shopping.
//
// It does not exist to admit the guard read: that read is Earning-class and was
// never deniable. It exists so the ROTATION keeps a yard we are actively buying
// from fresh between guard reads, rather than letting it decay to the baseline the
// moment the buy loop looks away.
func (b *YardScanBudget) NoteTarget(waypoint string) {
	if waypoint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.target[waypoint] = b.now()
	b.aggregateStale = true
}

// Observe folds a freshly scanned yard's catalogue into its value estimate, so the
// next rotation weights it on what it is actually selling and whether that is
// priced, rather than on the optimistic prior it carried while unseen.
//
// A yard that turns out to sell nothing wanted drops to the baseline here, which
// is how the rotation's attention concentrates: every scan either confirms a yard
// is worth watching or retires it.
func (b *YardScanBudget) Observe(waypoint string, availabilities []shipyard.ShipTypeAvailability) {
	if waypoint == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	facts := yardscan.Facts{}
	heavy := false
	for _, a := range availabilities {
		// Recorded from the FULL listing rather than only from the wanted ones. A
		// heavy type is structurally wanted (see wantedLocked), so the two agree
		// today — but reading it off its own predicate is what keeps this fact true
		// if the demand window ever stops covering a heavy class, rather than
		// quietly downgrading the fleet's most expensive counters.
		if b.heavy.Contains(a.ShipType) {
			heavy = true
		}
		if !b.wantedLocked(a.ShipType) {
			continue
		}
		facts.SellsWanted = true
		if a.PurchasePrice > 0 {
			facts.Priced = true
		}
	}
	b.facts[waypoint] = facts
	// A fresh scan REPLACES the heavy reading rather than accumulating it: this is
	// the authoritative listing for the yard, and a counter that has stopped
	// stocking heavies must stop being ranked as one.
	if heavy {
		b.heavySeller[waypoint] = true
	} else {
		delete(b.heavySeller, waypoint)
	}
	b.seen[waypoint] = struct{}{}
	b.aggregateStale = true
}

// wantedLocked reports whether the fleet is shopping for this hull type: either it
// is structurally a heavy (always wanted) or something has priced it inside the
// demand window.
func (b *YardScanBudget) wantedLocked(shipType string) bool {
	if shipType == "" {
		return false
	}
	if b.heavy.Contains(shipType) {
		return true
	}
	at, ok := b.demand[shipType]
	return ok && b.now().Sub(at) < yardDemandTTL
}

// wantedTypesLocked is the current wanted set, for the store query that rebuilds
// the demand picture. Expired demand is dropped here rather than swept on a timer,
// so the map cannot outlive the window it is read through.
func (b *YardScanBudget) wantedTypesLocked() []string {
	now := b.now()
	seen := make(map[string]struct{})
	out := make([]string, 0, len(b.demand)+4)
	for _, t := range b.heavy.Members() {
		if _, dup := seen[t]; dup || t == "" {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for t, at := range b.demand {
		if now.Sub(at) >= yardDemandTTL {
			delete(b.demand, t)
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// targetedLocked reports whether a money guard priced at this yard inside the
// target window.
func (b *YardScanBudget) targetedLocked(waypoint string) bool {
	at, ok := b.target[waypoint]
	return ok && b.now().Sub(at) < yardTargetTTL
}
