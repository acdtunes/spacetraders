package gate

import (
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// DefaultBuyFloor and DefaultResumeFloor are the ARMED defaults. These are tunables,
	// not feature flags: the knob adjusts a value in a path that always runs, and an unset
	// knob resolves here rather than disabling the policy. There is no off state.
	DefaultBuyFloor    = shared.SupplyLevelModerate
	DefaultResumeFloor = shared.SupplyLevelHigh
)

// StuckPauseTimeout bounds how long a pause may hold while supply sits AT OR ABOVE the buy floor
// but below the resume floor — the gap the fleet can otherwise never climb out of (sp-c9wuu).
//
// WHY A TIMEOUT RATHER THAN A DERIVED GAP. The bead offers both. A gap derived from each market's
// observed recovery rate needs a regeneration model this system does not have, would be wrong
// exactly when it is coldest (a market we have never watched recover), and would STILL need a
// backstop for the case where the estimate is too optimistic. The timeout is that backstop, so it
// is the necessary half; the rate model is the optional extra. It also bounds the harm directly:
// the defect is that PROGRESS STOPPED, and a timeout is a bound on stopped progress.
//
// WHY SIXTY MINUTES, DERIVED. The pause exists to damp the buy-deplete-pause cycle, so it must
// outlast one full cycle or it releases before the leg that depleted the market has even finished —
// which is the chatter it was built to prevent. One cycle is bounded by
// constructionSupplyTaskDefaultTimeout (30m), the drain's own limit on a single
// claim->source->deliver->record task, chosen to never cut a legitimate in-system round trip. Two
// of those is the floor for safety: one to outlast the depleting leg, a second to observe whether
// supply is genuinely climbing rather than momentarily bouncing.
//
// Against the incident it is 60 minutes versus the 450 the fleet actually lost — the stall is
// bounded at ~13% of what it cost, while every oscillation faster than an hour is still damped.
//
// It is NOT a round number chosen for comfort: change constructionSupplyTaskDefaultTimeout and this
// should move with it, because it is two of those.
const StuckPauseTimeout = 60 * time.Minute

// SuspectedStuckAfter is when a pause held at the buy floor starts REPORTING itself as suspected
// stuck — half the release timeout, i.e. one supply-task lifetime.
//
// IT MUST BE STRICTLY BELOW StuckPauseTimeout OR IT IS UNREACHABLE, which is how this was first
// written: escalating at the same threshold that releases means the flag is computed on a decision
// that has already resumed, so it can never be observed and the "counter" criterion 3 asks for
// would always read zero. A check that cannot fire is the failure mode this bead is about.
//
// The half-window is also defence in depth, and that is the real reason for the gap rather than
// merely making the field reachable: the escalation does NOT depend on the release working. If the
// timeout is ever mis-tuned, disabled, or broken by a later change, the fleet still spends thirty
// minutes shouting that a pause has plateaued before anything releases it — which is exactly the
// signal that was missing for seven and a half hours.
const SuspectedStuckAfter = StuckPauseTimeout / 2

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
	// PausedFor is how long the current pause has held at ANY supply level. Distinct from
	// HeldAtBuyFloor: a market below the buy floor accumulates this and not that.
	PausedFor time.Duration
	// HeldAtBuyFloor is how long supply has sat AT OR ABOVE the buy floor while still paused. It is
	// the quantity the deadlock is made of, and it was not previously observable at all: the pause
	// logged the same reassuring line whether it had held for one tick or seven hours.
	HeldAtBuyFloor time.Duration
	// SuspectedStuck marks a pause that has held at the buy floor past StuckPauseTimeout. It is what
	// makes a stuck pause DISTINGUISHABLE from healthy patience — the observability half of sp-c9wuu,
	// and arguably the more important half: a permanently-stuck state that logs a reassuring reason
	// every tick actively argues for the wrong conclusion, and did, twice, to the Admiral.
	SuspectedStuck bool
	// ResumedByTimeout marks the resume that the timeout, rather than the resume floor, produced.
	ResumedByTimeout bool
}

// LogLine renders the decision for the container log. Everything is in the MESSAGE, not
// in a metadata map: the container log renderer drops the map, so a decision that
// reported itself only in metadata would be exactly as invisible as one that said nothing.
func (d Decision) LogLine() string {
	if d.Paused && d.SuspectedStuck && d.Supply.Order() < d.BuyFloor.Order() {
		// BELOW the buy floor: waiting is CORRECT policy, so this is a visibility line and not an
		// accusation. It says the gate is not moving and names the lever, because the automatic
		// release deliberately does not cross the buy floor (sp-vrnjx).
		return fmt.Sprintf("Gate delivery PAUSE SUSPECTED STUCK on %s at %s: supply %s has been BELOW the %s buy floor for %s, so buying is correctly withheld and the gate is making no progress on this material. Nothing will release this automatically — the market must recover, or an operator must lower the buy floor",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.PausedFor.Round(time.Minute))
	}
	if d.Paused && d.SuspectedStuck {
		return fmt.Sprintf("Gate delivery PAUSE SUSPECTED STUCK on %s at %s: supply %s has been AT OR ABOVE the %s buy floor for %s but has never reached the %s resume floor — this is no longer patience, the market has plateaued inside the hysteresis gap and delivery is making no progress",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.HeldAtBuyFloor.Round(time.Minute), d.ResumeFloor)
	}
	// AT OR ABOVE THE BUY FLOOR BUT STILL PAUSED. This case had no wording of its own and fell to
	// the generic line below, which then said something FALSE — "supply LIMITED is below the LIMITED
	// buy floor". That sentence is a large part of why the deadlock read as healthy: it described a
	// market that had recovered to the configured buy level as one that was still too thin.
	if d.Paused && d.Supply.Order() >= d.BuyFloor.Order() {
		return fmt.Sprintf("Gate delivery PAUSED on %s at %s: supply %s has recovered to the %s buy floor and held %s, but resumes only at %s",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.HeldAtBuyFloor.Round(time.Minute), d.ResumeFloor)
	}
	if d.Paused {
		return fmt.Sprintf("Gate delivery PAUSED on %s at %s: supply %s is below the %s buy floor — resumes at %s",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.ResumeFloor)
	}
	if d.ResumedByTimeout {
		return fmt.Sprintf("Gate delivery RESUMING %s at %s by TIMEOUT: supply %s held at or above the %s buy floor for %s without reaching the %s resume floor, so the hysteresis gap is being overridden rather than stalling delivery further",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.HeldAtBuyFloor.Round(time.Minute), d.ResumeFloor)
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
	clock       shared.Clock
	// paused is good -> paused. Absent means "never observed", which is NOT paused: an
	// unobserved fleet must never read as a paused one.
	paused map[string]bool
	// heldAtBuyFloorSince is good -> when supply FIRST reached the buy floor during the current
	// pause, zero when it is not currently there.
	//
	// THE CLOCK STARTS ON RECOVERY, NOT ON THE PAUSE, and that is the whole of the anti-chatter
	// guarantee (criterion 4). A market that keeps dipping back below the buy floor keeps resetting
	// this to zero, so it can never accumulate an hour and can never be released early — the
	// timeout is unreachable for exactly the fast oscillation the hysteresis exists to damp. Only a
	// market that has genuinely climbed to a level we are configured to buy at, and HELD it, can
	// spend the timeout down.
	heldAtBuyFloorSince map[string]time.Time
	// pausedSince is good -> when the CURRENT pause began, at ANY supply level.
	//
	// IT ANCHORS ON THE PAUSE, NOT ON REACHING THE BUY FLOOR, and that difference is the whole of
	// sp-vrnjx. heldAtBuyFloorSince above only runs while supply is AT OR ABOVE the buy floor, so a
	// market sitting strictly BELOW it never anchored anything: F45 FAB_MATS sat SCARCE against a
	// LIMITED floor for three and a half hours with suspected_stuck and resumed_by_timeout both
	// reading exactly zero. The pause was not merely unbounded, it was INVISIBLE.
	//
	// It drives the ESCALATION ONLY, never the release. See SuspectedStuck.
	pausedSince map[string]time.Time
}

// NewBuyPolicy builds the policy from the live floors. Unset floors resolve to the armed
// defaults, and a resume floor that is not strictly above the buy floor is RAISED to the
// next level up — a zero-or-negative gap collapses the hysteresis back to the single
// threshold that chatters, which is the whole defect the second floor exists to prevent.
func NewBuyPolicy(buyFloor, resumeFloor shared.SupplyLevel) *BuyPolicy {
	return NewBuyPolicyWithClock(buyFloor, resumeFloor, nil)
}

// NewBuyPolicyWithClock is NewBuyPolicy with an injectable clock, so the stuck-pause timeout can be
// exercised without sleeping. A nil clock resolves to the real one, matching the DI convention the
// rest of the codebase uses for optional clocks.
func NewBuyPolicyWithClock(buyFloor, resumeFloor shared.SupplyLevel, clock shared.Clock) *BuyPolicy {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if buyFloor.Order() == 0 {
		buyFloor = DefaultBuyFloor
	}
	if resumeFloor.Order() == 0 {
		resumeFloor = DefaultResumeFloor
	}
	if resumeFloor.Order() <= buyFloor.Order() {
		resumeFloor = nextLevelAbove(buyFloor)
	}
	return &BuyPolicy{
		buyFloor:            buyFloor,
		resumeFloor:         resumeFloor,
		clock:               clock,
		paused:              make(map[string]bool),
		heldAtBuyFloorSince: make(map[string]time.Time),
		pausedSince:         make(map[string]time.Time),
	}
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

	now := p.clock.Now()
	atBuyFloor := supply.Order() >= p.buyFloor.Order()

	// THE HELD-AT-BUY-FLOOR CLOCK. It runs only while paused AND at or above the buy floor, and is
	// cleared the instant either stops being true. Every reset is a market that dipped again, which
	// is precisely the case the timeout must NOT rescue.
	if !p.paused[good] || !atBuyFloor {
		delete(p.heldAtBuyFloorSince, good)
	} else if _, running := p.heldAtBuyFloorSince[good]; !running {
		p.heldAtBuyFloorSince[good] = now
	}

	var held time.Duration
	if since, running := p.heldAtBuyFloorSince[good]; running {
		held = now.Sub(since)
	}

	buy := false
	resumedByTimeout := false
	if p.paused[good] {
		buy = supply.Order() >= p.resumeFloor.Order()
		// THE STUCK-PAUSE RELEASE. Supply is at a level the operator configured us to BUY at, and
		// has held there past the timeout, but the resume floor sits further up the ladder than this
		// market recovers. Waiting longer is not patience; it is the 7.5-hour deadlock.
		if !buy && held >= StuckPauseTimeout {
			buy = true
			resumedByTimeout = true
		}
	} else {
		buy = atBuyFloor
	}
	p.paused[good] = !buy
	if buy {
		// A resumed material starts its next pause with fresh clocks.
		delete(p.heldAtBuyFloorSince, good)
		delete(p.pausedSince, good)
	}

	// THE PAUSE CLOCK, ANCHORED AFTER THE RULING RATHER THAN BEFORE IT.
	//
	// Read before p.paused is updated it would anchor a TICK LATE — the tick that begins a pause
	// still sees the old "not paused" state, so the clock started on the SECOND tick and every
	// duration was short by one interval. Anchoring here means a pause is zero seconds old on the
	// tick it starts, which is what it is.
	if !p.paused[good] {
		delete(p.pausedSince, good)
	} else if _, running := p.pausedSince[good]; !running {
		p.pausedSince[good] = now
	}
	var pausedFor time.Duration
	if since, running := p.pausedSince[good]; running {
		pausedFor = now.Sub(since)
	}

	return Decision{
		Good:           good,
		Factory:        factory,
		Supply:         supply,
		Buy:            buy,
		Paused:         !buy,
		BuyFloor:       p.buyFloor,
		ResumeFloor:    p.resumeFloor,
		PausedFor:      pausedFor,
		HeldAtBuyFloor: held,
		// ESCALATION IS ANCHORED ON THE PAUSE; THE RELEASE ABOVE IS NOT (sp-vrnjx).
		//
		// Keyed on `held` this could not fire below the buy floor, which is how a three-and-a-half
		// hour pause produced zero of both signals. Keyed on pausedFor it fires at ANY supply level,
		// because "the fleet has been paused an hour and the gate has not moved" is worth saying
		// regardless of why.
		//
		// A FLAPPING MARKET DOES accumulate this, deliberately. Criterion 4's anti-chatter property
		// protects the RELEASE — a spend decision, which must not be triggered by a market bouncing
		// across the floor — and the release is still anchored on heldAtBuyFloorSince, which resets
		// on every dip. A market that has flapped for an hour while the gate stands still is exactly
		// something an operator should be told about; suppressing that would rebuild the invisible
		// pause one level down.
		SuspectedStuck:   !buy && pausedFor >= SuspectedStuckAfter,
		ResumedByTimeout: resumedByTimeout,
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
