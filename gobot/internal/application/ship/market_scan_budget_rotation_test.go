package ship

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// market_scan_budget_rotation_test.go pins the ONE property the rotation bound has
// to have: it describes the rotation the fleet is running, and nothing else.
//
// THE PROHIBITION THESE TESTS EXIST FOR IS ON UPTIME, and it outlives the count it
// was written about. A denominator that only ever accumulates — every market this
// process has been asked about since boot — is not a rotation size at all: it starts
// at zero on every restart and climbs from there, so a bound derived from it is
// minutes wide in the window a restart opens and widens as the process runs, with no
// relation whatever to how often the fleet actually re-reads a market. A daemon that
// bounces to pick up a deploy re-opens that window every single time.
//
// The bound is now derived from the markets under DEMAND — asked about inside
// demandWindow, and pruned when they stop — which is why it cannot be a function of
// uptime: running longer adds nothing that running longer does not also remove. The
// tests below drive that from both sides, because an accumulator would pass a test
// that only ever grows the rotation.
//
// The freshness face and the pacing denominator are now ONE number. They had to err
// in OPPOSITE directions while the denominator was a charting census — pacing wanting
// the smallest count it could defend, freshness the largest — but both leans were
// corrections for the same defect rather than a real difference in what the two
// consumers need. See RotationInputs for why sizing on the rotation dissolves it.

// An unasked budget has no rotation to describe, and must say so rather than invent
// one. That direction is deliberate: letting an empty rotation widen a fail-closed
// money guard is the fail-OPEN a freshness gate must never do.
func TestRotationInputs_ARotationNothingHasAskedAboutSaysUnknownRatherThanInventingOne(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	_, marketsKnown := b.RotationInputs(context.Background())

	assert.Equal(t, 0, marketsKnown,
		"an unasked budget is running no rotation, and publishing a number would invent one")
	// An arbitrary floor, to show the caller's own number comes back untouched — the
	// property is independent of what any particular consumer's floor happens to be.
	const callerFloor = 17 * time.Minute
	assert.Equal(t, callerFloor, marketscan.FreshnessCap(callerFloor, b.policy, marketsKnown),
		"an unknown rotation must leave the consumer on its own floor, not on a fabricated bound")
}

// The bound has to move with the ROTATION and stay still under everything else.
//
// This is the property the whole derivation rests on, expressed without pinning a
// number: twice the rotation is twice the bound, because a fixed allowance spread
// over twice as many markets comes round half as often.
func TestRotationInputs_TwiceTheRotationIsTwiceTheBound(t *testing.T) {
	small, _ := newTestBudget(t, 0.35, 8)
	for _, waypoint := range manyWaypoints(300) {
		small.Debit(budgetTestPlayerID, waypoint)
	}

	large, _ := newTestBudget(t, 0.35, 8)
	for _, waypoint := range manyWaypoints(600) {
		large.Debit(budgetTestPlayerID, waypoint)
	}

	// Compared with a nanosecond of slack: the bound is a float division rounded to a
	// Duration, so doubling it and doubling its input need not land on the same integer.
	assert.InDelta(t, float64(2*rotationBoundOf(t, small)), float64(rotationBoundOf(t, large)), 1,
		"twice the rotation must be twice the bound — that is what makes it a rotation bound")
}

// RUNNING LONGER IS NOT COVERING MORE. A process that has been up for hours, working
// the same markets over and over, must report the rotation it is running NOW rather
// than the union of everything it has ever been asked about. This is the exact defect
// the old since-boot tally had, and it is what a pruned demand set removes.
func TestRotationInputs_TheBoundTracksTheRotationAndNotTheProcessUptime(t *testing.T) {
	b, clock := newTestBudget(t, 0.35, 8)

	sweep := func(n int) {
		for _, waypoint := range manyWaypoints(n) {
			b.Debit(budgetTestPlayerID, waypoint)
		}
	}

	sweep(100)
	before := rotationBoundOf(t, b)

	// Six more passes over the SAME hundred markets, spanning several windows. An
	// accumulator would have grown; a rotation has not changed.
	for cycle := 0; cycle < 6; cycle++ {
		clock.advance(demandWindow / 2)
		sweep(100)
	}
	assert.Equal(t, before, rotationBoundOf(t, b),
		"the process ran longer, the rotation did not grow, and the bound must not have moved")

	// And it has to fall as well as hold, or "tracks the rotation" only means "never
	// shrinks" — which is the accumulator this test exists to rule out.
	clock.advance(demandWindow + time.Minute)
	sweep(10)
	assert.Less(t, rotationBoundOf(t, b), before,
		"a rotation the fleet stopped running must stop lengthening the bound")
}

// PACING keeps the same denominator, and that is the change from when the two faces
// disagreed: there is one rotation, so the number admission is paced against and the
// number a freshness consumer derives its cap from cannot drift apart.
func TestAdmit_PacingAndTheFreshnessFaceReadOneRotation(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	for _, waypoint := range manyWaypoints(200) {
		b.Debit(budgetTestPlayerID, waypoint)
	}

	_, marketsKnown := b.RotationInputs(context.Background())
	assert.Equal(t, 200, b.Snapshot().MarketsKnown, "pacing sees the rotation it is pacing")
	assert.Equal(t, b.Snapshot().MarketsKnown, marketsKnown, "and the freshness face sees the same one")
}

func rotationBoundOf(t *testing.T, b *ScanBudget) time.Duration {
	t.Helper()
	budget, marketsKnown := b.RotationInputs(context.Background())
	return marketscan.FreshnessCap(0, budget, marketsKnown)
}

func manyWaypoints(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "X1-TALLY-"+string(rune('A'+i/26))+string(rune('A'+i%26)))
	}
	return out
}
