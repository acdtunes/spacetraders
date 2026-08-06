package trading_test

import (
	"math"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	decaySource = "X1-KP46-H51"
	decayGood   = "IRON"
)

// tierResolver answers one activity for every market.
func tierResolver(activity string, known bool) func(string, string) (string, bool) {
	return func(string, string) (string, bool) { return activity, known }
}

// drained returns a ledger carrying two full tranches on the source key — past the bound, so the
// bound is actually consulted rather than trivially clear.
func drained(t *testing.T, activity string, known bool, at time.Time) (*trading.LaneCooldownLedger, trading.LaneKey) {
	t.Helper()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	l.SetActivityResolver(tierResolver(activity, known))
	key := trading.SourceDrainKey(decaySource, decayGood)
	l.Accrue(key, 120, 60, at)
	if l.Debt(key, at) <= l.TrancheDebt() {
		t.Fatalf("fixture does not saturate past the bound: debt %v vs %v", l.Debt(key, at), l.TrancheDebt())
	}
	return l, key
}

// THE MEASURED CASE. A STRONG market recovers several times faster, so its debt must fall below the
// bound in a fraction of the time — the source is released for reuse once it has actually recovered.
func TestDecay_StrongMarketReleasesTheBoundFarSooner(t *testing.T) {
	at := time.Now()
	strong, strongKey := drained(t, trading.ActivityStrong, true, at)
	restricted, restrictedKey := drained(t, "RESTRICTED", true, at)

	after := at.Add(3 * time.Hour)
	strongDebt := strong.Debt(strongKey, after)
	restrictedDebt := restricted.Debt(restrictedKey, after)

	if strongDebt >= restrictedDebt {
		t.Fatalf("STRONG debt %v must fall faster than RESTRICTED %v", strongDebt, restrictedDebt)
	}
	if strongDebt > strong.TrancheDebt() {
		t.Fatalf("a STRONG market must clear the bound within 3h, debt %v vs bound %v", strongDebt, strong.TrancheDebt())
	}
	if restrictedDebt <= restricted.TrancheDebt() {
		t.Fatalf("a RESTRICTED market must NOT have cleared the bound at 3h (debt %v, bound %v) — otherwise this proves no difference", restrictedDebt, restricted.TrancheDebt())
	}
}

// UNKNOWN IS THE SLOW RATE. Every tier that was not measured fast — including one that cannot be
// read at all — decays exactly as it did before, so nothing is released early on a guess.
func TestDecay_UnmeasuredAndUnreadableTiersKeepTheSlowRate(t *testing.T) {
	at := time.Now()
	after := at.Add(3 * time.Hour)

	baseline := trading.NewLaneCooldownLedger(0, 0, 0) // no resolver at all
	baseKey := trading.SourceDrainKey(decaySource, decayGood)
	baseline.Accrue(baseKey, 120, 60, at)
	want := baseline.Debt(baseKey, after)

	for _, tc := range []struct {
		name     string
		activity string
		known    bool
	}{
		{"restricted", "RESTRICTED", true},
		{"weak", "WEAK", true},
		{"growing", "GROWING", true},
		{"empty", "", true},
		{"unreadable", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, key := drained(t, tc.activity, tc.known, at)
			if got := l.Debt(key, after); math.Abs(got-want) > 1e-12 {
				t.Fatalf("debt %v, want the unconditioned %v — an unmeasured tier must not decay faster", got, want)
			}
		})
	}
}

// THE TRADE RANKER IS UNTOUCHED. A full lane names TWO markets and the recovery rates were fitted
// per market, so there is no measurement of what it decays at — it keeps tau even when the resolver
// reports STRONG.
func TestDecay_AFullLaneKeepsTheSlowRateEvenUnderStrong(t *testing.T) {
	at := time.Now()
	after := at.Add(3 * time.Hour)

	conditioned := trading.NewLaneCooldownLedger(0, 0, 0)
	conditioned.SetActivityResolver(tierResolver(trading.ActivityStrong, true))
	baseline := trading.NewLaneCooldownLedger(0, 0, 0)

	lane := trading.LaneKey{Source: decaySource, Dest: "X1-KP46-F45", Good: decayGood}
	conditioned.Accrue(lane, 120, 60, at)
	baseline.Accrue(lane, 120, 60, at)

	if got, want := conditioned.Debt(lane, after), baseline.Debt(lane, after); math.Abs(got-want) > 1e-12 {
		t.Fatalf("full-lane debt %v vs unconditioned %v — conditioning a two-ended key re-ranks trade on a rate nothing measured", got, want)
	}
}

// RULINGS #4 DIRECTION. Conditioning may only ever make debt SMALLER (release sooner), never larger
// — nothing may arm the guard harder than it is today.
func TestDecay_ConditioningNeverArmsHarderThanToday(t *testing.T) {
	at := time.Now()
	baseline := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.SourceDrainKey(decaySource, decayGood)
	baseline.Accrue(key, 120, 60, at)

	for _, activity := range []string{trading.ActivityStrong, "RESTRICTED", "WEAK", "GROWING", ""} {
		l, k := drained(t, activity, true, at)
		for _, dt := range []time.Duration{time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour} {
			after := at.Add(dt)
			if got, base := l.Debt(k, after), baseline.Debt(key, after); got > base+1e-12 {
				t.Fatalf("%s at %v: debt %v exceeds the unconditioned %v — conditioning must only release sooner", activity, dt, got, base)
			}
		}
	}
}
