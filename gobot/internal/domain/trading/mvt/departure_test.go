package mvt

import (
	"testing"
	"time"
)

func TestYieldTracker_EWMAAndColdStart(t *testing.T) {
	tr := NewYieldTracker(8, 3) // alpha = 2/9
	t0 := time.Unix(1_000_000, 0)
	tr.Observe(100, 10, t0)
	if _, ok := tr.Estimate(); ok {
		t.Fatal("one sell must not yield an estimate at minSells=3")
	}
	tr.Observe(100, 10, t0.Add(time.Minute))
	tr.Observe(190, 10, t0.Add(2*time.Minute))
	est, ok := tr.Estimate()
	if !ok {
		t.Fatal("three sells must produce an estimate")
	}
	// ewma: 100 → 100 → 100 + 2/9*(190-100) = 120
	if est < 119.99 || est > 120.01 {
		t.Fatalf("estimate = %v, want 120", est)
	}
	if tr.Sells() != 3 {
		t.Fatalf("sells = %d", tr.Sells())
	}
}

func TestYieldTracker_CreditsPerSec(t *testing.T) {
	tr := NewYieldTracker(8, 1)
	tr.SetRateSpanFloor(0) // the arithmetic itself; the floor has its own test below
	t0 := time.Unix(1_000_000, 0)
	if tr.CreditsPerSec(t0) != 0 {
		t.Fatal("no observations → 0")
	}
	tr.Observe(50, 10, t0) // 500 credits
	if tr.CreditsPerSec(t0.Add(time.Minute)) != 0 {
		t.Fatal("a single observation has no rate")
	}
	tr.Observe(50, 10, t0.Add(100*time.Second)) // 1000 credits over 100 s
	if got := tr.CreditsPerSec(t0.Add(100 * time.Second)); got != 10 {
		t.Fatalf("rate = %v, want 10", got)
	}
	tr.Reset()
	if tr.Sells() != 0 || tr.CreditsPerSec(t0.Add(time.Hour)) != 0 {
		t.Fatal("reset must clear everything")
	}
}

func TestYieldTracker_CreditsPerSec_FloorFallsBackUntilSpanClears(t *testing.T) {
	tr := NewYieldTracker(8, 1)
	tr.SetRateSpanFloor(30 * time.Minute)
	t0 := time.Unix(1_000_000, 0)
	observe := func() {
		tr.Observe(50, 10, t0)
		tr.Observe(50, 10, t0.Add(100*time.Second)) // 1000 credits over 100 s
	}
	observe()
	if got := tr.CreditsPerSec(t0.Add(100 * time.Second)); got != 0 {
		t.Fatalf("rate = %v under a 30-minute floor, want 0 (the caller falls back to the fleet rate)", got)
	}
	if got := tr.CreditsPerSec(t0.Add(30 * time.Minute)); got != 1000.0/1800.0 {
		t.Fatalf("rate at the floor = %v, want %v", got, 1000.0/1800.0)
	}

	tr.Reset()
	observe()
	if got := tr.CreditsPerSec(t0.Add(100 * time.Second)); got != 0 {
		t.Fatalf("rate = %v after a reset, want 0: Reset must preserve the span floor", got)
	}

	tr.SetRateSpanFloor(0)
	if got := tr.CreditsPerSec(t0.Add(100 * time.Second)); got != 10 {
		t.Fatalf("rate = %v with the floor off, want 10", got)
	}
}

func TestDecide_Table(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	warm := func(perUnit float64) *YieldTracker {
		tr := NewYieldTracker(8, 3)
		for i := 0; i < 3; i++ {
			tr.Observe(perUnit, 10, t0.Add(time.Duration(i)*time.Minute))
		}
		return tr
	}
	cold := func() *YieldTracker {
		tr := NewYieldTracker(8, 3)
		tr.Observe(1, 10, t0)
		return tr
	}
	cases := []struct {
		name   string
		tr     *YieldTracker
		alt    float64
		hasAlt bool
		leave  bool
		reason string
	}{
		{"cold start cannot leave on yield", cold(), 1_000_000, true, false, ReasonColdStart},
		{"no alternative stays", warm(100), 0, false, false, ReasonNoAlternative},
		{"yield at or above alternative stays", warm(100), 100, true, false, ReasonStay},
		{"yield below alternative leaves", warm(100), 100.01, true, true, ReasonYieldBelow},
		{"negative alternative never wins", warm(-5), -1, true, true, ReasonYieldBelow},
	}
	for _, tc := range cases {
		d := Decide(tc.tr, tc.alt, tc.hasAlt)
		if d.Leave != tc.leave || d.Reason != tc.reason {
			t.Fatalf("%s: got %+v", tc.name, d)
		}
	}
}
