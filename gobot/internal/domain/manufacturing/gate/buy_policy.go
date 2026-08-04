package gate

import (
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// DefaultBuyFloor and DefaultResumeFloor are the ARMED defaults. These are tunables,
	// not feature flags: the knob adjusts a value in a path that always runs, and an unset
	// knob resolves here rather than disabling the policy. There is no off state.
	DefaultBuyFloor    = shared.SupplyLevelModerate
	DefaultResumeFloor = shared.SupplyLevelHigh
)

// supplyLadder is the SCARCE..ABUNDANT ordering, used to raise a mis-set resume floor to
// the level above the buy floor. It mirrors shared.SupplyLevel.Order() and is asserted
// against it by the package tests, so the two orderings cannot drift.
var supplyLadder = []shared.SupplyLevel{
	shared.SupplyLevelScarce,
	shared.SupplyLevelLimited,
	shared.SupplyLevelModerate,
	shared.SupplyLevelHigh,
	shared.SupplyLevelAbundant,
}

// Decision is one buy/pause ruling, materialized. It exists because the failure this
// design corrects is that decisions lived only as control flow — an if that returned
// nil — so a declined operation and an idle one were indistinguishable. Every field an
// operator needs to act is here: what, where, what was observed, and what would change it.
type Decision struct {
	Good        string
	Factory     string
	Supply      shared.SupplyLevel
	Buy         bool
	Paused      bool
	BuyFloor    shared.SupplyLevel
	ResumeFloor shared.SupplyLevel
}

// LogLine renders the decision for the container log. Everything is in the MESSAGE, not
// in a metadata map: the container log renderer drops the map, so a decision that
// reported itself only in metadata would be exactly as invisible as one that said nothing.
func (d Decision) LogLine() string {
	if d.Paused {
		return fmt.Sprintf("Gate delivery PAUSED on %s at %s: supply %s is below the %s buy floor — resumes at %s",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.ResumeFloor)
	}
	return fmt.Sprintf("Gate delivery BUYING %s at %s: supply %s is at or above the %s buy floor",
		d.Good, d.Factory, d.Supply, d.BuyFloor)
}

// BuyPolicy is the supply-anchored buy/pause rule with hysteresis.
//
// Pause state is IN-MEMORY and per-process, following the worker-registry precedent. A
// restart re-derives it: an unpaused start re-pauses on its first low read, costing one
// tick and never a spend. Persisting it would add a write to the hot path and buy nothing.
//
// Price is deliberately NOT a gate here. The gate is a finite, high-ROI investment, and
// supply-anchoring already paces against our own market impact — sustained buying depletes
// supply, which trips the pause before the ask can ladder far.
type BuyPolicy struct {
	mu          sync.Mutex
	buyFloor    shared.SupplyLevel
	resumeFloor shared.SupplyLevel
	// paused is good -> paused. Absent means "never observed", which is NOT paused: an
	// unobserved fleet must never read as a paused one.
	paused map[string]bool
}

// NewBuyPolicy builds the policy from the live floors. Unset floors resolve to the armed
// defaults, and a resume floor that is not strictly above the buy floor is RAISED to the
// next level up — a zero-or-negative gap collapses the hysteresis back to the single
// threshold that chatters, which is the whole defect the second floor exists to prevent.
func NewBuyPolicy(buyFloor, resumeFloor shared.SupplyLevel) *BuyPolicy {
	if buyFloor.Order() == 0 {
		buyFloor = DefaultBuyFloor
	}
	if resumeFloor.Order() == 0 {
		resumeFloor = DefaultResumeFloor
	}
	if resumeFloor.Order() <= buyFloor.Order() {
		resumeFloor = nextLevelAbove(buyFloor)
	}
	return &BuyPolicy{buyFloor: buyFloor, resumeFloor: resumeFloor, paused: make(map[string]bool)}
}

// nextLevelAbove is the supply level one step above level, or level itself when it is
// already the top of the ladder (ABUNDANT, where no gap is expressible).
func nextLevelAbove(level shared.SupplyLevel) shared.SupplyLevel {
	for i, l := range supplyLadder {
		if l == level && i+1 < len(supplyLadder) {
			return supplyLadder[i+1]
		}
	}
	return level
}

// Floors reports the resolved floors in force — what the operator's knob actually became
// after defaulting and gap-raising, not what was passed in.
func (p *BuyPolicy) Floors() (buy, resume shared.SupplyLevel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buyFloor, p.resumeFloor
}

// Decide rules on one material at its terminal factory and RECORDS the ruling.
//
// The hysteresis is one-directional and lives entirely in this method: a material that is
// currently buying pauses the moment supply drops below the buy floor, and a material that
// is currently paused resumes only once supply reaches the RESUME floor — not merely the
// buy floor it fell through. Reading the resume side against the buy floor is the chatter
// bug: pause, one unit regenerates, resume, immediately deplete.
func (p *BuyPolicy) Decide(good, factory string, supply shared.SupplyLevel) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	buy := false
	if p.paused[good] {
		buy = supply.Order() >= p.resumeFloor.Order()
	} else {
		buy = supply.Order() >= p.buyFloor.Order()
	}
	p.paused[good] = !buy

	return Decision{
		Good:        good,
		Factory:     factory,
		Supply:      supply,
		Buy:         buy,
		Paused:      !buy,
		BuyFloor:    p.buyFloor,
		ResumeFloor: p.resumeFloor,
	}
}

// FleetPaused reports whether the DELIVERY FLEET is paused: only when EVERY gate material
// is paused, never when any one is.
//
// Because a hull fills greedily from any eligible material, delivery still has useful work
// while even one material is buyable; treating one pause as a fleet pause would move
// workers away from capacity delivery can still use. An empty list is not a paused fleet
// either — nothing was observed, and reporting "paused" would send an operator to tune a
// knob that changes nothing.
func (p *BuyPolicy) FleetPaused(goods []string) bool {
	if len(goods) == 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, good := range goods {
		if !p.paused[good] {
			return false
		}
	}
	return true
}
