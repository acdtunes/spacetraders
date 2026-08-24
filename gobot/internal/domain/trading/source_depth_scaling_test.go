package trading_test

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	depthSource = "WP-SOURCE"
	depthGood   = "ELECTRONICS"
	depthVolume = 20
)

// countingBreadth is a stub breadth reader that records how many times it was consulted, so a
// test can assert the read is not merely correct but not ISSUED at all on the paths that must
// stay free of it.
type countingBreadth struct {
	listings map[string]int
	reads    int
}

func (c *countingBreadth) ListingBreadth(_ context.Context, waypoint string) (int, bool) {
	c.reads++
	n, ok := c.listings[waypoint]
	return n, ok
}

// depthLedger is a ledger carrying `tranches` full trade-volumes of undecayed drain on the
// source-aggregate key — the state a buyer that has just been hammering an exporter leaves.
func depthLedger(t *testing.T, tranches int, now time.Time) (*trading.LaneCooldownLedger, trading.LaneKey) {
	t.Helper()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.SourceDrainKey(depthSource, depthGood)
	l.Accrue(key, tranches*depthVolume, depthVolume, now)
	return l, key
}

// The prior in force wherever no operator override replaces it, and it must be a usable shape: a
// zero shape resolves to the uniform prior and would make the default silently inert.
func TestDefaultSourceDepthScaling_IsTheShippedFitAndActive(t *testing.T) {
	policy := trading.DefaultSourceDepthScaling()

	if !policy.Enabled {
		t.Fatal("the prior applies with no config present")
	}
	if policy.ThinListings != trading.DefaultSourceThinListings || policy.MinDebtScale != trading.DefaultMinSourceDebtScale {
		t.Fatalf("shipped fit = %+v, want the documented shape terms", policy)
	}
	if policy.DebtScale(40) >= 1 {
		t.Fatal("a deep hub must be discounted under the default")
	}
}

// The clamp itself: full caution at the thin end, proportional relief above it, floored so no
// market is read as bottomless, and the uniform prior for breadth the model cannot use.
func TestSourceDepthScaling_DebtScale(t *testing.T) {
	shipped := trading.DefaultSourceDepthScaling()
	cases := []struct {
		name     string
		policy   trading.SourceDepthScaling
		listings int
		want     float64
	}{
		{"kill switch thrown", trading.SourceDepthScaling{ThinListings: 2, MinDebtScale: 0.2}, 20, 1},
		{"unreadable breadth keeps the uniform prior", shipped, 0, 1},
		{"negative breadth keeps the uniform prior", shipped, -3, 1},
		{"a single-listing source keeps full caution", shipped, 1, 1},
		{"a source at the thin threshold keeps full caution", shipped, 2, 1},
		{"breadth above the threshold discounts in proportion", shipped, 5, 0.4},
		{"a deep hub floors at the minimum", shipped, 20, 0.2},
		{"an arbitrarily broad market still carries the floor", shipped, 1000, 0.2},
		{"an ill-formed threshold keeps the uniform prior",
			trading.SourceDepthScaling{Enabled: true, MinDebtScale: 0.2}, 20, 1},
		{"an ill-formed floor keeps the uniform prior",
			trading.SourceDepthScaling{Enabled: true, ThinListings: 2}, 20, 1},
		{"a floor above the uniform prior keeps the uniform prior",
			trading.SourceDepthScaling{Enabled: true, ThinListings: 2, MinDebtScale: 1.5}, 20, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.DebtScale(tc.listings); got != tc.want {
				t.Fatalf("DebtScale(%d) = %v, want %v", tc.listings, got, tc.want)
			}
		})
	}
}

// The prior is monotone in breadth and never lets a source read MORE compressed than its raw
// debt, which is the property that keeps this from ever pacing harder than the raw model.
func TestSourceDepthScaling_IsMonotoneAndNeverAboveTheUniformPrior(t *testing.T) {
	policy := trading.DefaultSourceDepthScaling()
	prev := 1.0
	for listings := 1; listings <= 60; listings++ {
		got := policy.DebtScale(listings)
		if got > prev {
			t.Fatalf("breadth %d scaled to %v, above %v at breadth %d", listings, got, prev, listings-1)
		}
		if got <= 0 || got > 1 {
			t.Fatalf("breadth %d scaled to %v, outside (0,1]", listings, got)
		}
		prev = got
	}
}

// A source the cache confirms is broad prices its standing drain at the floored fraction, which
// is what keeps a hub under the pacing bound.
func TestLaneCooldownLedger_PacedDebtScalesADeepSource(t *testing.T) {
	now := time.Now()
	l, key := depthLedger(t, 3, now)
	l.SetSourceBreadthReader(&countingBreadth{listings: map[string]int{depthSource: 20}})

	raw := l.Debt(key, now)
	if raw <= l.TrancheDebt() {
		t.Fatalf("fixture is inert: raw debt %v is already under the bound %v", raw, l.TrancheDebt())
	}
	if got, want := l.PacedDebt(context.Background(), key, now), raw*0.2; got != want {
		t.Fatalf("PacedDebt = %v, want %v", got, want)
	}
	if paced := l.PacedDebt(context.Background(), key, now); paced > l.TrancheDebt() {
		t.Fatalf("a deep hub's paced debt %v still exceeds the bound %v — the prior does not reach the case it exists for", paced, l.TrancheDebt())
	}
}

// THE KILL SWITCH reaches the ledger: a disabled prior paces a confirmed hub at its full debt and
// costs no breadth query.
func TestLaneCooldownLedger_PacedDebtIsUnscaledUnderTheKillSwitch(t *testing.T) {
	now := time.Now()
	l, key := depthLedger(t, 3, now)
	reader := &countingBreadth{listings: map[string]int{depthSource: 20}}
	l.SetSourceDepthScaling(trading.SourceDepthScaling{ThinListings: 2, MinDebtScale: 0.2}, reader)

	if got, want := l.PacedDebt(context.Background(), key, now), l.Debt(key, now); got != want {
		t.Fatalf("PacedDebt = %v, want the unscaled %v", got, want)
	}
	if reader.reads != 0 {
		t.Fatalf("breadth read %d time(s) under the kill switch", reader.reads)
	}
}

// A thin source is the class the pacing protects, and it keeps its full debt.
func TestLaneCooldownLedger_PacedDebtKeepsFullCautionOnAThinSource(t *testing.T) {
	now := time.Now()
	for _, listings := range []int{1, 2} {
		l, key := depthLedger(t, 3, now)
		l.SetSourceBreadthReader(&countingBreadth{listings: map[string]int{depthSource: listings}})

		if got, want := l.PacedDebt(context.Background(), key, now), l.Debt(key, now); got != want {
			t.Fatalf("PacedDebt = %v at breadth %d, want the unscaled %v", got, listings, want)
		}
	}
}

// UNKNOWN BREADTH IS FULL CAUTION, and it is the same NUMBER, not merely the same decision: the
// consult compares this value against a fixed bound. An uncached market, a read error and a market
// nobody has scanned all arrive as "not ok".
func TestLaneCooldownLedger_PacedDebtKeepsFullCautionOnUnreadableBreadth(t *testing.T) {
	now := time.Now()
	l, key := depthLedger(t, 3, now)
	l.SetSourceBreadthReader(&countingBreadth{listings: map[string]int{}})

	if got, want := l.PacedDebt(context.Background(), key, now), l.Debt(key, now); got != want {
		t.Fatalf("PacedDebt = %v, want the unscaled %v — breadth we cannot read may not buy relief", got, want)
	}
}

// A ledger with no reader wired is the boot order this cannot depend on. It prices at the full
// debt rather than as a hub.
func TestLaneCooldownLedger_PacedDebtKeepsFullCautionWithNoReader(t *testing.T) {
	now := time.Now()
	l, key := depthLedger(t, 3, now)

	if got, want := l.PacedDebt(context.Background(), key, now), l.Debt(key, now); got != want {
		t.Fatalf("PacedDebt = %v, want the unscaled %v", got, want)
	}
}

// SOURCE-DRAIN KEYS ONLY. A full lane names two markets, so there is no single breadth to read —
// and the lane ranker reads those keys. Scaling one would silently re-rank the income engine.
func TestLaneCooldownLedger_PacedDebtLeavesFullLaneKeysUnscaled(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	lane := trading.LaneKey{Source: depthSource, Dest: "WP-SINK", Good: depthGood}
	l.Accrue(lane, 3*depthVolume, depthVolume, now)
	reader := &countingBreadth{listings: map[string]int{depthSource: 20}}
	l.SetSourceBreadthReader(reader)

	if got, want := l.PacedDebt(context.Background(), lane, now), l.Debt(lane, now); got != want {
		t.Fatalf("PacedDebt = %v, want the unscaled %v for a two-ended lane", got, want)
	}
	if reader.reads != 0 {
		t.Fatalf("breadth read %d time(s) for a two-ended lane, whose breadth is ambiguous", reader.reads)
	}
}

// Debt is the ranker's read and is left exactly as it is, wired reader or not.
func TestLaneCooldownLedger_TheRankersDebtReadIsUnscaled(t *testing.T) {
	now := time.Now()
	l, key := depthLedger(t, 3, now)
	raw := l.Debt(key, now)
	l.SetSourceBreadthReader(&countingBreadth{listings: map[string]int{depthSource: 20}})

	if got := l.Debt(key, now); got != raw {
		t.Fatalf("Debt = %v with a reader wired, want %v — the income-side read is out of this prior's scope", got, raw)
	}
}

// Scaling only ever shrinks a positive debt, so a source carrying none has nothing to price and
// must cost no query. Most sources in any leg are in exactly this state.
func TestLaneCooldownLedger_PacedDebtSkipsTheReadOnAnUndrainedSource(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	reader := &countingBreadth{listings: map[string]int{depthSource: 20}}
	l.SetSourceBreadthReader(reader)

	if got := l.PacedDebt(context.Background(), trading.SourceDrainKey(depthSource, depthGood), now); got != 0 {
		t.Fatalf("PacedDebt = %v on an untraded source, want 0", got)
	}
	if reader.reads != 0 {
		t.Fatalf("breadth read %d time(s) for a source carrying no debt", reader.reads)
	}
}
