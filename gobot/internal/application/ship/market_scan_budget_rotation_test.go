package ship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
)

// market_scan_budget_rotation_test.go pins the ONE property the rotation bound
// has to have: it describes the rotation the fleet is running, and nothing else.
//
// The bound's two consumers pull the denominator in OPPOSITE directions, and that
// is what these tests exist to keep apart. PACING wants the smallest denominator it
// can defend, because under-counting the map only makes the budget spend LESS than
// its allowance and a rate guard that errs low is still a rate guard. FRESHNESS wants
// the largest, because under-counting the map shortens the bound, and a bound that is
// too short makes every consumer discard data the rotation perfectly well explains —
// which is the whole incident the derivation was built to end. One number cannot
// serve both, so the freshness face reports the CENSUS, and says it does not know
// rather than substituting a count that is not one.

// The since-boot tally is not a map size and must never be published as one.
//
// It counts the markets THIS PROCESS has been asked about, so it starts at zero on
// every restart and climbs from there. Feed it to the bound and the bound becomes a
// function of uptime: minutes wide in the window a restart opens, and widening as the
// process runs, with no relation whatever to how often the fleet actually re-reads a
// market. A consumer comparing against it refuses live data for as long as the tally
// is climbing — and a daemon that bounces to pick up a deploy re-opens that window
// every single time.
func TestRotationInputs_AnUncountedMapSaysUnknownRatherThanPublishTheSinceBootTally(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	// No census wired — every market here is one the gate has merely been ASKED about.
	for _, waypoint := range manyWaypoints(200) {
		b.Debit(budgetTestPlayerID, waypoint)
	}
	require.Equal(t, 200, b.Snapshot().MarketsKnown, "fixture must have a tally to be tempted by")

	_, marketsKnown := b.RotationInputs(context.Background())

	assert.Equal(t, 0, marketsKnown,
		"an uncounted map is UNKNOWN; publishing the tally invents a rotation the fleet is not running")
	assert.Equal(t, 75*time.Minute,
		marketscan.FreshnessCap(75*time.Minute, b.policy, marketsKnown),
		"and an unknown map must leave the consumer on its own floor, not on a fabricated bound")
}

// The bound has to move with the MAP and stay still under everything else.
//
// This is the property the whole derivation rests on, expressed without pinning a
// number: twice the map is twice the bound, because a fixed allowance spread over
// twice as many markets comes round half as often. And running longer is not the same
// as charting more, so a process that has simply been asked about more markets must
// report exactly the same bound.
func TestRotationInputs_TheBoundTracksTheMapAndNotTheProcessUptime(t *testing.T) {
	small, _ := newTestBudget(t, 0.35, 8)
	small.SetChartedMarketCounter(&stubCounter{counts: map[string]int{"X1-AA": 3000}})

	large, _ := newTestBudget(t, 0.35, 8)
	large.SetChartedMarketCounter(&stubCounter{counts: map[string]int{"X1-AA": 3000, "X1-BB": 3000}})

	// Compared with a nanosecond of slack: the bound is a float division rounded to a
	// Duration, so doubling it and doubling its input need not land on the same integer.
	assert.InDelta(t, float64(2*rotationBoundOf(t, small)), float64(rotationBoundOf(t, large)), 1,
		"twice the map must be twice the bound — that is what makes it a rotation bound")

	// Now take a fleet EARLY in an era, whose census is still small, and let it be
	// asked about more markets than the census carries — which is what a running
	// process does. That is uptime, not charting, and the bound must not notice.
	young, _ := newTestBudget(t, 0.35, 8)
	young.SetChartedMarketCounter(&stubCounter{counts: map[string]int{"X1-AA": 100}})
	before := rotationBoundOf(t, young)
	for _, waypoint := range manyWaypoints(500) {
		young.Debit(budgetTestPlayerID, waypoint)
	}

	assert.Equal(t, before, rotationBoundOf(t, young),
		"the bound moved because the process had been running, not because the map grew")
}

// A census that cannot be read is a census the fleet does not have, and the bound
// must degrade to "unknown" rather than to whatever the tally happens to hold. This is
// the failure that has no other symptom: the count is re-read on a timer and its error
// is not returned to anyone, so a permanently failing counter looks exactly like a tiny
// map from every consumer's side.
func TestRotationInputs_AFailingCensusIsUnknownRatherThanASmallMap(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)
	b.SetChartedMarketCounter(&stubCounter{err: errors.New("census unavailable")})
	for _, waypoint := range manyWaypoints(200) {
		b.Debit(budgetTestPlayerID, waypoint)
	}

	_, marketsKnown := b.RotationInputs(context.Background())

	assert.Equal(t, 0, marketsKnown, "a census that failed to read is not a map size of 200")
}

// PACING is the other direction and must be untouched: with no census the budget still
// paces against the markets it has been asked about, because a denominator that errs
// LOW only makes the allowance spend less than it is allowed to. Breaking this would
// unmeter the scanner, which is the defect the budget exists to prevent.
func TestAdmit_PacingStillFallsBackToTheTallyWhenNoCensusExists(t *testing.T) {
	b, _ := newTestBudget(t, 0.35, 8)

	for _, waypoint := range manyWaypoints(200) {
		b.Debit(budgetTestPlayerID, waypoint)
	}

	assert.Equal(t, 200, b.Snapshot().MarketsKnown,
		"pacing keeps its lower-bound denominator; only the freshness face reports UNKNOWN")
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
