package contract

import (
	"context"
	"errors"
	"testing"
)

// fakeTreasuryReader serves a fixed live-treasury snapshot (or a read error) to the
// dispatcher's working-capital reserve gate, and counts reads so a per-pass (not
// per-leg) read can be asserted.
type fakeTreasuryReader struct {
	credits int64
	err     error
	calls   int
}

func (f *fakeTreasuryReader) LiveTreasury(context.Context) (int64, error) {
	f.calls++
	return f.credits, f.err
}

// TestIdleArb_WorkingCapitalReserveGate proves the dispatcher's working-capital
// reserve gate (sp-zq635 §4a): the idle-arb dispatcher launches up to
// (idle-reserve) concurrent legs per pass, each capped at MaxSpendPerLeg but with no
// shared spend ledger — so N legs could each individually clear the arb run's own 50k
// floor yet COLLECTIVELY drain treasury below it. The gate accounts for the pass's
// cumulative committed leg-spend and HOLDS the rest of the pass once one more leg would
// breach common.EffectiveReserveFloor. Ample treasury is byte-identical; an unreadable
// treasury fails CLOSED; an unwired reader leaves the gate inert (pre-sp-zq635 behavior).
//
// The two-sink harness gives the two surplus hulls DISTINCT sinks so the lane mutex
// never caps the count — isolating the reserve gate as the only thing that can.
func TestIdleArb_WorkingCapitalReserveGate(t *testing.T) {
	cases := []struct {
		name         string
		wireReader   bool
		credits      int64
		readErr      error
		wantLaunched int
	}{
		{
			name:         "ample treasury launches both legs (byte-identical)",
			wireReader:   true,
			credits:      10_000_000,
			wantLaunched: 2,
		},
		{
			name:         "below floor: no leg launches (a single 100k leg would breach the 50k reserve)",
			wireReader:   true,
			credits:      120_000, // 120k - 100k = 20k < 50k floor
			wantLaunched: 0,
		},
		{
			name:         "partial: funds one leg, holds the second before the cumulative spend breaches",
			wireReader:   true,
			credits:      200_000, // leg1: 200k-100k=100k ok; leg2: 200k-200k=0 < 50k -> HOLD
			wantLaunched: 1,
		},
		{
			name:         "fail-closed: an unreadable treasury holds every leg",
			wireReader:   true,
			readErr:      errors.New("agent read failed"),
			wantLaunched: 0,
		},
		{
			name:         "inert when not wired: byte-identical, both legs launch even below floor",
			wireReader:   false,
			credits:      120_000,
			wantLaunched: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, _, launcher := idleArbTwoSinkHarness(t, 3, IdleArbConfig{ReserveHulls: 1, MaxSpendPerLeg: 100_000})
			var reader *fakeTreasuryReader
			if tc.wireReader {
				reader = &fakeTreasuryReader{credits: tc.credits, err: tc.readErr}
				dispatcher.SetTreasuryReader(reader)
			}

			launched := dispatcher.DispatchOnce(context.Background())

			if launched != tc.wantLaunched || len(launcher.launches) != tc.wantLaunched {
				t.Fatalf("launched = %d (recorded %d), want %d", launched, len(launcher.launches), tc.wantLaunched)
			}
			// The gate reads treasury ONCE per pass, not once per leg.
			if tc.wireReader && reader.calls != 1 {
				t.Errorf("expected exactly one per-pass treasury read, got %d", reader.calls)
			}
		})
	}
}
