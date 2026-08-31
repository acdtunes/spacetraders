package hullrepair

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// memLedger is the episode store held in memory, with the same semantics as the durable one.
type memLedger struct {
	rows map[string]Record
}

func newMemLedger() *memLedger { return &memLedger{rows: map[string]Record{}} }

func (l *memLedger) Observe(_ context.Context, playerID int, symbol string, at time.Time) error {
	if _, ok := l.rows[symbol]; ok {
		return nil
	}
	l.rows[symbol] = Record{PlayerID: playerID, ShipSymbol: symbol, FirstSeenAt: at, NextAttemptAt: at}
	return nil
}

func (l *memLedger) Due(_ context.Context, playerID int, at time.Time) ([]Record, error) {
	var out []Record
	for _, r := range l.rows {
		if r.PlayerID == playerID && r.EscalatedAt == nil && !r.NextAttemptAt.After(at) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (l *memLedger) Save(_ context.Context, rec Record) error {
	l.rows[rec.ShipSymbol] = rec
	return nil
}

func (l *memLedger) Clear(_ context.Context, _ int, symbol string) error {
	delete(l.rows, symbol)
	return nil
}

func (l *memLedger) Find(_ context.Context, _ int, symbol string) (Record, bool, error) {
	rec, ok := l.rows[symbol]
	return rec, ok, nil
}

type recordingReporter struct {
	attempts  []Outcome
	escalated []string
}

func (r *recordingReporter) Attempted(_ string, o Outcome) { r.attempts = append(r.attempts, o) }
func (r *recordingReporter) Escalated(symbol, _ string)    { r.escalated = append(r.escalated, symbol) }

type sweepFixture struct {
	harness  *harness
	ledger   *memLedger
	reporter *recordingReporter
	sweeper  *Sweeper
	clock    time.Time
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	f := &sweepFixture{
		harness:  newHarness(),
		ledger:   newMemLedger(),
		reporter: &recordingReporter{},
		clock:    time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	repairer := NewRepairer(f.harness.probe, f.harness.writer, f.harness.market, f.harness.treasury,
		f.harness.tanks, f.harness.refresher, f.reporter)
	f.sweeper = NewSweeper(f.ledger, repairer, f.reporter, func() time.Time { return f.clock }, nil)
	return f
}

func (f *sweepFixture) advance(d time.Duration) { f.clock = f.clock.Add(d) }

func (f *sweepFixture) sweep(t *testing.T) {
	t.Helper()
	require.NoError(t, f.sweeper.Sweep(context.Background(), 10))
}

// Nothing open means nothing probed, which is what lets the sweep run on a short cadence.
func TestSweepIsFreeWhileNoHullIsUnreadable(t *testing.T) {
	f := newSweepFixture(t)

	f.sweep(t)

	require.Zero(t, f.harness.probe.compositeN, "an empty ledger must cost no API call at all")
	require.Zero(t, f.harness.writer.wrote())
}

func TestRepairedHullClosesItsEpisode(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = []Verdict{ReadRefusedServer, ReadOK}
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	f.sweep(t)

	require.Empty(t, f.ledger.rows, "a hull that reads again must leave no open episode behind")
	require.Equal(t, []Outcome{OutcomeRepaired}, f.reporter.attempts)
}

// The attempt bound: a repair that keeps failing on its own terms is capped, then handed
// to an operator rather than retried forever.
func TestAttemptsAreCappedThenEscalated(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil // every composite read refuses
	f.harness.writer.refuelErr = errors.New("API error (status 400)")
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	for i := 0; i < MaxAttempts; i++ {
		f.sweep(t)
		f.advance(MaxBackoff)
	}

	require.Equal(t, MaxAttempts, f.harness.writer.refuelled, "the bound is on writes, and it must bind")
	rec := f.ledger.rows["SHIP-1"]
	require.NotNil(t, rec.EscalatedAt, "a hull that will not recover must be surfaced, not retried")
	require.Equal(t, []string{"SHIP-1"}, f.reporter.escalated)

	// And it stays surfaced: an escalated episode is never picked up again.
	f.advance(24 * time.Hour)
	f.sweep(t)
	require.Equal(t, MaxAttempts, f.harness.writer.refuelled, "an escalated hull must never be retried automatically")
}

// A fill that landed and did not help is terminal on the first pass — retrying a spend
// already proven not to apply is pure waste.
func TestAWriteThatProvesTheFieldIsNotFuelEscalatesAtOnce(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	f.sweep(t)

	require.Equal(t, 1, f.harness.writer.refuelled)
	rec := f.ledger.rows["SHIP-1"]
	require.NotNil(t, rec.EscalatedAt)
	require.Equal(t, string(OutcomeNotFuel), rec.LastOutcome)
	require.Equal(t, []string{"SHIP-1"}, f.reporter.escalated)
}

// An outage must not eat the attempt budget: when the API comes back the repair still has
// every attempt it started with.
func TestAnOutageSpendsNoAttempts(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	f.harness.probe.parts = Subresources{Refused: []string{"nav", "cargo", "cooldown", "mounts", "modules"}}
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	for i := 0; i < MaxAttempts+2; i++ {
		f.sweep(t)
		f.advance(RetryBackoff)
	}

	rec := f.ledger.rows["SHIP-1"]
	require.Zero(t, rec.Attempts, "an outage establishes nothing about this hull and must not consume its budget")
	require.Nil(t, rec.EscalatedAt, "and inside the stall deadline it is not given up on either")
	require.Zero(t, f.harness.writer.wrote())

	// The budget is intact when the API comes back, so the first real chance still repairs.
	f.harness.probe.composite = []Verdict{ReadRefusedServer, ReadOK}
	f.harness.probe.compositeN = 0
	f.harness.probe.parts = orbitingHull()
	f.advance(MaxBackoff)
	f.sweep(t)
	require.Empty(t, f.ledger.rows)
}

// An episode that can never reach a write still ends: past the stall deadline it becomes
// an operator's problem instead of a silent retry loop.
func TestAnEpisodeThatNeverReachesAWriteEscalatesOnTheStallDeadline(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	f.harness.market = fakeMarket{sells: false}
	f.sweeper = NewSweeper(f.ledger,
		NewRepairer(f.harness.probe, f.harness.writer, f.harness.market, f.harness.treasury,
			f.harness.tanks, f.harness.refresher, f.reporter),
		f.reporter, func() time.Time { return f.clock }, nil)
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	f.sweep(t)
	require.Nil(t, f.ledger.rows["SHIP-1"].EscalatedAt, "a blocked pass alone is not a reason to give up")

	f.advance(EscalateAfter)
	f.sweep(t)

	rec := f.ledger.rows["SHIP-1"]
	require.NotNil(t, rec.EscalatedAt, "an episode the repair can never run must still reach an operator")
	require.Zero(t, f.harness.writer.wrote())
}

// Re-observing an open episode must not reset the bound that stops the loop.
func TestReObservingAnOpenEpisodeKeepsItsBounds(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	f.harness.writer.refuelErr = errors.New("API error (status 400)")
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	f.sweep(t)
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	require.Equal(t, 1, f.ledger.rows["SHIP-1"].Attempts)
}

// Backoff holds a hull back between passes rather than re-probing it every tick.
func TestBackoffDefersTheNextPass(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	f.harness.writer.refuelErr = errors.New("API error (status 400)")
	require.NoError(t, f.sweeper.Observe(context.Background(), 10, "SHIP-1"))

	f.sweep(t)
	f.advance(RetryBackoff / 2)
	f.sweep(t)

	require.Equal(t, 1, f.harness.writer.refuelled, "a deferred episode must not be worked again inside its backoff")

	f.advance(MaxBackoff)
	f.sweep(t)
	require.Equal(t, 2, f.harness.writer.refuelled)
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	require.Equal(t, RetryBackoff, backoffFor(0))
	require.Equal(t, 2*RetryBackoff, backoffFor(1))
	require.Equal(t, MaxBackoff, backoffFor(50))
}

// The operator verb runs the same confirmed sequence, and still spends an attempt when it
// writes, so driving it in a loop cannot get around the bound.
func TestManualRepairOpensAnEpisodeAndIsStillBounded(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = nil
	f.harness.writer.refuelErr = errors.New("API error (status 400)")

	res, err := f.sweeper.RepairNow(context.Background(), 10, "SHIP-1")

	require.NoError(t, err)
	require.Equal(t, OutcomeWriteFailed, res.Outcome, res.Reason)
	require.Equal(t, 1, f.ledger.rows["SHIP-1"].Attempts)
}

func TestManualRepairOnAHealthyHullWritesNothing(t *testing.T) {
	f := newSweepFixture(t)
	f.harness.probe.composite = []Verdict{ReadOK}

	res, err := f.sweeper.RepairNow(context.Background(), 10, "SHIP-1")

	require.NoError(t, err)
	require.Equal(t, OutcomeAlreadyHealthy, res.Outcome)
	require.Zero(t, f.harness.writer.wrote())
	require.Empty(t, f.ledger.rows)
}
