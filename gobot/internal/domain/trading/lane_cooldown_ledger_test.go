package trading_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

func newLedger() *trading.LaneCooldownLedger {
	// Explicit era-3 coefficients so the oracles below are hand-derived, not
	// dependent on the default-resolution path.
	return trading.NewLaneCooldownLedger(0.050, 0.015, 750*time.Minute)
}

var testLane = trading.LaneKey{Source: "X1-AA-1", Dest: "X1-BB-2", Good: "FUEL"}

// AC1b: trading one full tradeVolume (x=1) accrues debt = buyImpact+sellImpact = 0.065.
func TestCooldownLedger_AccruesCombinedImpactPerFullVolume(t *testing.T) {
	l := newLedger()
	now := time.Unix(0, 0)

	l.Accrue(testLane, 100, 100, now) // U=tv -> x=1

	got := l.Debt(testLane, now)
	if math.Abs(got-0.065) > 1e-9 {
		t.Fatalf("debt after one full-volume trade: got %.6f, want 0.065", got)
	}
}

// AC1b: debt accrues proportional to x=units/tv (a quarter-volume trade -> quarter debt).
func TestCooldownLedger_AccruesProportionalToUnitsOverVolume(t *testing.T) {
	l := newLedger()
	now := time.Unix(0, 0)

	l.Accrue(testLane, 25, 100, now) // x=0.25

	got := l.Debt(testLane, now)
	if math.Abs(got-0.065*0.25) > 1e-9 {
		t.Fatalf("debt after quarter-volume trade: got %.6f, want %.6f", got, 0.065*0.25)
	}
}

// AC1b: debt decays exp(-dt/tau); at the half-life dt=tau·ln2 exactly half remains.
func TestCooldownLedger_DecaysWithTauHalfLife(t *testing.T) {
	l := newLedger()
	tau := 750 * time.Minute
	start := time.Unix(0, 0)
	l.Accrue(testLane, 100, 100, start) // debt 0.065 at t=0
	initial := l.Debt(testLane, start)

	t.Run("half-life tau*ln2 leaves exactly half", func(t *testing.T) {
		halfLife := time.Duration(float64(tau) * math.Ln2)
		got := l.Debt(testLane, start.Add(halfLife))
		if math.Abs(got-initial/2) > 1e-6 {
			t.Fatalf("debt at half-life: got %.6f, want %.6f (half of %.6f)", got, initial/2, initial)
		}
	})

	t.Run("one tau leaves exp(-1)", func(t *testing.T) {
		got := l.Debt(testLane, start.Add(tau))
		want := initial * math.Exp(-1)
		if math.Abs(got-want) > 1e-6 {
			t.Fatalf("debt at t=tau: got %.6f, want %.6f", got, want)
		}
	})

	t.Run("reading never mutates the stored debt", func(t *testing.T) {
		// After the decayed reads above, an at-t=0 read must still return the full debt.
		if got := l.Debt(testLane, start); math.Abs(got-initial) > 1e-9 {
			t.Fatalf("Debt must be read-only; got %.6f after prior decayed reads, want %.6f", got, initial)
		}
	})
}

// The single-scalar store composes decay correctly: a second trade after some decay
// equals decaying the first forward then adding the second (multiplicative composition).
func TestCooldownLedger_ComposesDecayAcrossTrades(t *testing.T) {
	l := newLedger()
	tau := 750 * time.Minute
	start := time.Unix(0, 0)

	l.Accrue(testLane, 100, 100, start)          // 0.065 at t=0
	l.Accrue(testLane, 100, 100, start.Add(tau)) // decay 0.065 -> 0.065*e^-1, then +0.065

	want := 0.065*math.Exp(-1) + 0.065
	got := l.Debt(testLane, start.Add(tau))
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("composed debt: got %.6f, want %.6f", got, want)
	}
}

// A never-traded lane carries no debt (so a fresh lane is never spuriously penalized).
func TestCooldownLedger_UntrackedLaneHasZeroDebt(t *testing.T) {
	l := newLedger()
	if got := l.Debt(trading.LaneKey{Source: "X1-ZZ-9", Dest: "X1-YY-8", Good: "GOLD"}, time.Unix(0, 0)); got != 0 {
		t.Fatalf("untracked lane debt: got %.6f, want 0", got)
	}
}

// Fail-safe: a zero tradeVolume (or zero units) accrues nothing and never divides by zero.
func TestCooldownLedger_ZeroVolumeOrUnitsIsNoOp(t *testing.T) {
	l := newLedger()
	now := time.Unix(0, 0)
	l.Accrue(testLane, 100, 0, now) // tv=0
	l.Accrue(testLane, 0, 100, now) // units=0
	if got := l.Debt(testLane, now); got != 0 {
		t.Fatalf("zero-volume/zero-units accrual must be a no-op, got debt %.6f", got)
	}
}

// The ledger is SHARED across the fleet: concurrent hulls Accrue-ing the same lane land
// their full combined debt with no lost updates (mutex-guarded), and a rank read sees it.
func TestCooldownLedger_SharedAcrossConcurrentHulls(t *testing.T) {
	l := newLedger()
	now := time.Unix(0, 0)
	const hulls = 50

	var wg sync.WaitGroup
	for i := 0; i < hulls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Accrue(testLane, 100, 100, now) // each hull adds 0.065 at the same instant
		}()
	}
	wg.Wait()

	got := l.Debt(testLane, now)
	want := 0.065 * hulls
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("shared-ledger debt after %d concurrent hulls: got %.6f, want %.6f (lost update = race)", hulls, got, want)
	}
}

// Defaults resolve when the caller passes zeros (an un-refit config): the ledger still
// accrues the era-3 combined impact (0.065) and decays at the era-3 tau half-life.
func TestCooldownLedger_ZeroArgsResolveEra3Defaults(t *testing.T) {
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	start := time.Unix(0, 0)
	l.Accrue(testLane, 100, 100, start)
	if got := l.Debt(testLane, start); math.Abs(got-0.065) > 1e-9 {
		t.Fatalf("default-resolved combined impact: got %.6f, want 0.065", got)
	}
	defaultTau := trading.DefaultCooldownTau // via a var so the float->Duration conversion is runtime, not a constant
	halfLife := time.Duration(float64(defaultTau) * math.Ln2)
	if got := l.Debt(testLane, start.Add(halfLife)); math.Abs(got-0.065/2) > 1e-6 {
		t.Fatalf("default-resolved half-life decay: got %.6f, want %.6f", got, 0.065/2)
	}
}
