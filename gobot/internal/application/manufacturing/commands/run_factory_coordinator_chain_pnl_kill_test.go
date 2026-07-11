package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-rh2z (analyst redesign C2). The chain P&L kill-switch auto-pauses a chain whose realized
// P&L/hr has fallen below the kill threshold over the rolling window — the self-pruning the
// realization side lacked. These pins drive the coordinator's verdict + episode state machine
// directly with a fake ledger reader, decoupled from the DB and full Handle().

// fakeChainPnLReader is an injectable ledger reader: it returns canned raw aggregates (or an
// error, to prove fail-open). It records the window `since` it was asked for so the default
// window resolution can be pinned.
type fakeChainPnLReader struct {
	raw       manufacturing.ChainPnLRaw
	err       error
	calls     int
	lastSince time.Time
}

func (f *fakeChainPnLReader) ReadRealizedPnL(ctx context.Context, playerID int, since time.Time) (manufacturing.ChainPnLRaw, error) {
	f.calls++
	f.lastSince = since
	return f.raw, f.err
}

// gatherCounterValue reads the value of a {good}-labelled counter series from a registry.
func gatherCounterValue(t *testing.T, reg *prometheus.Registry, name, good string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "good" && l.GetValue() == good {
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// singleGoodRaw builds a one-good ledger fixture for the coordinator's TargetGood.
func singleGoodRaw(factoryCost, factorySell, tourNet, refuelPool int) manufacturing.ChainPnLRaw {
	return manufacturing.ChainPnLRaw{
		Goods:      []manufacturing.ChainGoodFlow{{Good: testOutputGood, FactoryCost: factoryCost, FactorySell: factorySell, TourNet: tourNet}},
		RefuelPool: refuelPool,
	}
}

func killTestHandler(t *testing.T, reader mfgServices.ChainPnLReader) (*RunFactoryCoordinatorHandler, *RunFactoryCoordinatorCommand) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	handler := newFactoryHandlerWithClock(t, clock)
	handler.SetChainPnLReader(reader)
	cmd := &RunFactoryCoordinatorCommand{
		PlayerID:     1,
		TargetGood:   testOutputGood,
		SystemSymbol: testSystem,
		ContainerID:  testContainerID,
		// leave kill config zero -> defaults resolve (threshold 30000/hr, window 6h)
	}
	return handler, cmd
}

// A chain realizing below the kill threshold over the window is KILLED, with the number in
// the verdict.
func TestChainPnLKill_BelowThreshold_Kills(t *testing.T) {
	// net = 50000 sell − 300000 input = −250000 over 6h = −41,667/hr < 30,000/hr.
	reader := &fakeChainPnLReader{raw: singleGoodRaw(-300000, 50000, 0, 0)}
	handler, cmd := killTestHandler(t, reader)

	v := handler.evaluateChainPnLKill(context.Background(), cmd)

	if !v.Killed {
		t.Fatalf("expected kill for a chain at −41,667/hr, got verdict %+v", v)
	}
	if v.Reason != chainPnLBelowThreshold {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLBelowThreshold)
	}
	if v.Result.NetPerHour >= 0 {
		t.Errorf("expected a negative net/hr, got %v", v.Result.NetPerHour)
	}
	if v.Threshold != defaultChainPnLKillThresholdPerHour {
		t.Errorf("threshold = %d, want default %d", v.Threshold, defaultChainPnLKillThresholdPerHour)
	}
	// Default window resolution: since must be 6h before the mock now.
	wantSince := handler.clock.Now().Add(-defaultChainPnLWindowHours * time.Hour)
	if !reader.lastSince.Equal(wantSince) {
		t.Errorf("window since = %v, want %v (default %dh)", reader.lastSince, wantSince, defaultChainPnLWindowHours)
	}
}

// A chain realizing ABOVE the threshold proceeds (no kill).
func TestChainPnLKill_AboveThreshold_Proceeds(t *testing.T) {
	// net = 250000 sell − 10000 input = 240000 over 6h = 40,000/hr > 30,000/hr.
	reader := &fakeChainPnLReader{raw: singleGoodRaw(-10000, 250000, 0, 0)}
	handler, cmd := killTestHandler(t, reader)

	v := handler.evaluateChainPnLKill(context.Background(), cmd)

	if v.Killed {
		t.Fatalf("expected no kill for a chain at +40,000/hr, got %+v", v)
	}
	if v.Reason != chainPnLProceed {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLProceed)
	}
}

// FAIL OPEN #1 — an unwired reader disables the kill-switch (the optional-port contract).
func TestChainPnLKill_ReaderUnwired_FailsOpen(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	handler := newFactoryHandlerWithClock(t, clock) // no SetChainPnLReader
	cmd := &RunFactoryCoordinatorCommand{PlayerID: 1, TargetGood: testOutputGood, ContainerID: testContainerID}

	v := handler.evaluateChainPnLKill(context.Background(), cmd)

	if v.Killed {
		t.Fatalf("an unwired reader must never kill (fail open), got %+v", v)
	}
	if v.Reason != chainPnLReaderUnwired {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLReaderUnwired)
	}
}

// FAIL OPEN #2 — an unreadable ledger must NOT kill (RULINGS #4 distinction: the kill-switch
// can only STOP spend, so an accounting outage must not halt production — unlike the pre-spend
// guards which fail CLOSED). It logs a WARNING so the blind guard is visible.
func TestChainPnLKill_UnreadableLedger_FailsOpenWithWarning(t *testing.T) {
	reader := &fakeChainPnLReader{err: errors.New("db down")}
	handler, cmd := killTestHandler(t, reader)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	v := handler.evaluateChainPnLKill(ctx, cmd)

	if v.Killed {
		t.Fatalf("an unreadable ledger must fail OPEN (no kill), got %+v", v)
	}
	if v.Reason != chainPnLUnreadable {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLUnreadable)
	}
	warned := false
	for _, e := range logger.snapshot() {
		if e.level == "WARNING" && strings.Contains(strings.ToLower(e.message), "p&l") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected a WARNING that the P&L read failed (fail-open), got %+v", logger.snapshot())
	}
}

// The emergency disable flag skips the kill-switch entirely (RULINGS #5 off-switch).
func TestChainPnLKill_Disabled_FailsOpen(t *testing.T) {
	reader := &fakeChainPnLReader{raw: singleGoodRaw(-300000, 0, 0, 0)} // deeply underwater
	handler, cmd := killTestHandler(t, reader)
	cmd.ChainPnLKillDisabled = true

	v := handler.evaluateChainPnLKill(context.Background(), cmd)

	if v.Killed {
		t.Fatalf("disabled kill-switch must never kill, got %+v", v)
	}
	if v.Reason != chainPnLDisabled {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLDisabled)
	}
	if reader.calls != 0 {
		t.Errorf("disabled kill-switch must not even read the ledger, got %d calls", reader.calls)
	}
}

// A chain with input spend but ZERO realized output has no P&L signal yet (realization lags
// production) — the kill-switch fails open on it, protecting a pre-realization young chain
// from churn.
func TestChainPnLKill_NoRealization_FailsOpen(t *testing.T) {
	reader := &fakeChainPnLReader{raw: singleGoodRaw(-100000, 0, 0, 0)} // bought inputs, nothing realized
	handler, cmd := killTestHandler(t, reader)

	v := handler.evaluateChainPnLKill(context.Background(), cmd)

	if v.Killed {
		t.Fatalf("a pre-realization chain must fail open, got %+v", v)
	}
	if v.Reason != chainPnLNoRealization {
		t.Errorf("reason = %q, want %q", v.Reason, chainPnLNoRealization)
	}
}

// Episode semantics + RESUME: the kill counter increments once per episode (running→paused),
// not on every re-check; and when P&L recovers the chain resumes and a later dip is a fresh
// episode. Drives the real counter to prove once-per-episode.
func TestChainPnLKill_EpisodeDedupAndResume(t *testing.T) {
	prev := metrics.Registry
	prevGlobal := metrics.GetGlobalChainPnLCollector()
	t.Cleanup(func() {
		metrics.Registry = prev
		metrics.SetGlobalChainPnLCollector(prevGlobal)
	})
	metrics.Registry = prometheus.NewRegistry()
	collector := metrics.NewChainPnLMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("register: %v", err)
	}
	metrics.SetGlobalChainPnLCollector(collector)

	reader := &fakeChainPnLReader{}
	handler, cmd := killTestHandler(t, reader)
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	killVerdict := chainPnLKillVerdict{Killed: true, Reason: chainPnLBelowThreshold, Good: testOutputGood}
	runVerdict := chainPnLKillVerdict{Killed: false, Reason: chainPnLProceed, Good: testOutputGood}

	// Enter the killed episode: first record emits (transition), second is deduped.
	if !handler.recordChainPnLKill(ctx, cmd, killVerdict) {
		t.Errorf("first kill must be a state transition (emit)")
	}
	if handler.recordChainPnLKill(ctx, cmd, killVerdict) {
		t.Errorf("second consecutive kill must be deduped (no re-emit)")
	}
	// Recover: clearing a killed chain is a transition (resume logged).
	if !handler.clearChainPnLKill(ctx, cmd, runVerdict) {
		t.Errorf("clearing a killed chain must be a state transition (resume)")
	}
	if handler.clearChainPnLKill(ctx, cmd, runVerdict) {
		t.Errorf("clearing an already-running chain must be a no-op")
	}
	// A later dip is a fresh episode.
	if !handler.recordChainPnLKill(ctx, cmd, killVerdict) {
		t.Errorf("a dip after recovery must be a new kill episode (emit)")
	}

	got, ok := gatherCounterValue(t, metrics.Registry, "spacetraders_daemon_chain_pnl_kills_total", testOutputGood)
	if !ok {
		t.Fatalf("kill counter series not found")
	}
	if got != 2 {
		t.Errorf("kill counter = %v, want 2 (two episodes, re-checks deduped)", got)
	}
}
