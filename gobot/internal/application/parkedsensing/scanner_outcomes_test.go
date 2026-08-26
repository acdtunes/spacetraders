package parkedsensing

// scanner_outcomes_test.go pins the rotation's own account of what its turns
// produced.
//
// The pacer's rate is an ALLOWANCE, not a throughput. Every turn it issues is
// separately admitted by the fleet-wide market-scan budget — the one shared
// allowance every market reader in the daemon draws from — and a declined turn
// costs no request and writes no data. So a rotation running at exactly the rate
// the cycle line reports can land almost nothing, and from the outside that is
// indistinguishable from a rotation that has stopped. These counters are what
// separate the two.

import (
	"context"
	"errors"
	"testing"
)

// One turn of each kind, tallied under its own name.
func TestScannerOutcomes_TallyTheThreeWaysATurnEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner *fakeScanRunner
		want   ScanOutcomes
	}{
		{
			name:   "landed: the market budget admitted the turn and data was written",
			runner: &fakeScanRunner{},
			want:   ScanOutcomes{Scanned: 1},
		},
		{
			name:   "declined: the fleet market-scan budget served it from the store",
			runner: &fakeScanRunner{declineAll: true},
			want:   ScanOutcomes{Declined: 1},
		},
		{
			name:   "failed: the turn errored and wrote nothing",
			runner: &fakeScanRunner{err: errors.New("api down")},
			want:   ScanOutcomes{Failed: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, _, _ := scanFixture(t, 2, tc.runner)
			sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.1)}, 1.0)

			takeAndLaunch(t, context.Background(), sc)
			sc.workers.Wait()

			if got := sc.TakeScanOutcomes(); got != tc.want {
				t.Fatalf("outcomes = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TakeScanOutcomes RESETS, so each cycle line describes the interval it covers
// rather than the life of the process. A tally that only accumulated would make
// every line report a lifetime average, which is exactly the number that cannot
// show a budget starting to decline.
func TestScannerOutcomes_TakingThemResetsTheTally(t *testing.T) {
	runner := &fakeScanRunner{declineAll: true}
	sc, _, _ := scanFixture(t, 2, runner)
	sc.SyncMembership([]SensingSlotView{dueMarket("X1-AA-M1", 0.1)}, 1.0)

	takeAndLaunch(t, context.Background(), sc)
	sc.workers.Wait()

	if got := sc.TakeScanOutcomes(); got.Declined != 1 {
		t.Fatalf("first take = %+v, want one decline", got)
	}
	if got := sc.TakeScanOutcomes(); got != (ScanOutcomes{}) {
		t.Fatalf("second take = %+v, want an empty tally — a line must not re-report a turn "+
			"the previous line already accounted for", got)
	}
}

// Total is every turn taken, which is the denominator the landed count is only
// meaningful against: 3 landed says nothing until it is 3 of 51.
func TestScannerOutcomes_TotalCountsEveryTurn(t *testing.T) {
	outcomes := ScanOutcomes{Scanned: 3, Declined: 47, Failed: 1}
	if got := outcomes.Total(); got != 51 {
		t.Fatalf("Total() = %d, want 51", got)
	}
}
