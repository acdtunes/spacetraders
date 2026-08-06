package trading_test

// The trade engine's own compression debt is keyed by the FULL lane
// (source, dest, good), and no transaction row carries that pair — a purchase records where the
// buy happened, a sale where the sale happened. So unlike the source-drain keys, which the
// cooldownreplay package reconstructs from the purchase rows, a full-lane key exists nowhere but
// in memory and a restart forgets it outright (RULINGS #2 names cooldown clocks).

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// persistedDebt is one write-through the ledger handed its durable sink.
type persistedDebt struct {
	key  trading.LaneKey
	debt float64
	at   time.Time
}

// recordingStore is the durable sink standing in for the repository, plus the reload half: it
// replays what it captured into a FRESH ledger, which is what a daemon restart does.
type recordingStore struct {
	mu      sync.Mutex
	written []persistedDebt
}

func (s *recordingStore) persist(key trading.LaneKey, debt float64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, persistedDebt{key: key, debt: debt, at: at})
}

func (s *recordingStore) restoreInto(l *trading.LaneCooldownLedger) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	restored := 0
	for _, w := range s.written {
		if l.Restore(w.key, w.debt, w.at) {
			restored++
		}
	}
	return restored
}

func fullLane() trading.LaneKey {
	return trading.LaneKey{Source: "X1-KP46-A1", Dest: "X1-KP46-B2", Good: "IRON"}
}

// TestFullLaneDebtSurvivesARestart is the defect. A hull trades the lane, the daemon restarts, and
// the ranker must still see the compression the fleet just caused instead of re-entering a market
// it has only now drained.
func TestFullLaneDebtSurvivesARestart(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &recordingStore{}

	before := trading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
	before.SetDebtPersister(store.persist)
	before.Accrue(fullLane(), 30, 60, now)

	live := before.Debt(fullLane(), now)
	if live <= 0 {
		t.Fatalf("precondition: the in-memory ledger must carry the accrued debt, got %v", live)
	}

	// The restart: a brand-new ledger, seeded only from what the sink was told.
	after := trading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
	if restored := store.restoreInto(after); restored != 1 {
		t.Fatalf("the accrual was never handed to the durable sink, so a restart forgets it: restored %d of 1 lane", restored)
	}

	got := after.Debt(fullLane(), now)
	if math.Abs(got-live) > 1e-9 {
		t.Fatalf("restored debt must equal the debt that was accrued: got %v, want %v", got, live)
	}
}

// TestRestoredDebtKeepsDecayingFromWhenItWasAccrued pins that a restart is invisible to the decay.
// Restoring at boot time instead of at the accrual time would silently hand every entry a fresh
// tau, holding lanes down for hours longer than the model says.
func TestRestoredDebtKeepsDecayingFromWhenItWasAccrued(t *testing.T) {
	tau := 750 * time.Minute
	accruedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	bootAt := accruedAt.Add(tau)

	store := &recordingStore{}
	before := trading.NewLaneCooldownLedger(0.05, 0.015, tau)
	before.SetDebtPersister(store.persist)
	before.Accrue(fullLane(), 30, 60, accruedAt)
	wantAtBoot := before.Debt(fullLane(), bootAt)

	after := trading.NewLaneCooldownLedger(0.05, 0.015, tau)
	store.restoreInto(after)

	got := after.Debt(fullLane(), bootAt)
	if math.Abs(got-wantAtBoot) > 1e-9 {
		t.Fatalf("a restored entry must decay from its accrual time, not from boot: got %v, want %v", got, wantAtBoot)
	}
}

// TestSourceDrainKeysAreNotWrittenThrough guards the scope. The source-drain keys are the LIVE
// construction gate feed's spend guard and are reconstructed from the purchase rows at boot; a
// second durable copy here is one that can drift from the rows it duplicates.
func TestSourceDrainKeysAreNotWrittenThrough(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &recordingStore{}

	l := trading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
	l.SetDebtPersister(store.persist)
	l.Accrue(trading.SourceDrainKey("X1-KP46-A1", "IRON"), 30, 60, now)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.written) != 0 {
		t.Fatalf("a source-drain accrual must not be written through (it is reconstructed from the purchase rows): got %d write(s)", len(store.written))
	}
}

// TestRestoreRefusesAKeyThatAlreadyCarriesDebt is the double-count guard. It makes the reload
// structurally idempotent, so correctness does not rest on the reload beating every accrual.
func TestRestoreRefusesAKeyThatAlreadyCarriesDebt(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	l := trading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
	l.Accrue(fullLane(), 30, 60, now)
	live := l.Debt(fullLane(), now)

	if l.Restore(fullLane(), 99, now) {
		t.Fatal("Restore must refuse a key already carrying live debt")
	}
	if got := l.Debt(fullLane(), now); math.Abs(got-live) > 1e-9 {
		t.Fatalf("a refused Restore must leave the live debt untouched: got %v, want %v", got, live)
	}
}

// TestPersisterRunsOutsideTheLedgerLock pins the hazard the write-through introduces. The ledger
// mutex is shared with the LIVE construction gate feed's Debt reads, so a sink called while it is
// held puts a database write on that guard's hot path — and a sink that reads the ledger back
// deadlocks the daemon outright. This test hangs rather than fails if that regresses, which is why
// it runs the accrual under a timeout.
func TestPersisterRunsOutsideTheLedgerLock(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	l := trading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
	var readBack float64
	l.SetDebtPersister(func(key trading.LaneKey, debt float64, at time.Time) {
		readBack = l.Debt(key, at) // re-enters the ledger; deadlocks if the lock is still held
	})

	done := make(chan struct{})
	go func() {
		l.Accrue(fullLane(), 30, 60, now)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Accrue deadlocked: the debt persister is being called while the ledger lock is held")
	}
	if readBack <= 0 {
		t.Fatalf("the persister must see the entry it is being handed, got %v", readBack)
	}
}
