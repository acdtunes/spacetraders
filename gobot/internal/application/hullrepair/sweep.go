package hullrepair

import (
	"context"
	"fmt"
	"time"
)

const (
	// MaxAttempts bounds the passes that actually WRITE to one hull in one episode. The
	// repair is a spend, and a write that has been made and did not help will not help on
	// the next pass either; the budget exists to cover a write refused for its own
	// reasons, not to keep paying for a fix that does not apply.
	MaxAttempts = 3

	// RetryBackoff is the wait after a pass that changed nothing, doubling per attempt.
	RetryBackoff = 5 * time.Minute

	// MaxBackoff caps that doubling, so an episode is still re-checked often enough to
	// notice the hull recovering on its own.
	MaxBackoff = 30 * time.Minute

	// EscalateAfter bounds an episode that never reaches a write at all — a hull standing
	// where fuel cannot be bought, an API that stays down, a treasury that stays
	// unreadable. Past it the fleet needs an operator, not another quiet retry.
	EscalateAfter = 2 * time.Hour
)

// Sweeper drives every open episode through the repairer and holds the bounds. It is the
// unattended half: nothing here waits for an operator, and the only thing an operator ever
// has to do is deal with what escalated.
type Sweeper struct {
	ledger   Ledger
	repairer *Repairer
	report   Reporter
	now      func() time.Time
	logf     func(format string, args ...interface{})
}

// NewSweeper wires the sweep. A nil clock uses the wall clock.
func NewSweeper(ledger Ledger, repairer *Repairer, report Reporter, now func() time.Time, logf func(string, ...interface{})) *Sweeper {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Sweeper{ledger: ledger, repairer: repairer, report: report, now: now, logf: logf}
}

// Observe opens or refreshes an episode for a hull a fleet read could not deliver. It
// costs no API call, so every caller that learns a hull is unreadable can report it.
func (s *Sweeper) Observe(ctx context.Context, playerID int, symbol string) error {
	return s.ledger.Observe(ctx, playerID, symbol, s.now())
}

// Sweep runs one pass over every episode whose backoff has expired. It makes no API call
// at all when nothing is open, which is what lets it run on a short cadence.
func (s *Sweeper) Sweep(ctx context.Context, playerID int) error {
	due, err := s.ledger.Due(ctx, playerID, s.now())
	if err != nil {
		return fmt.Errorf("read the open unreadable-hull episodes: %w", err)
	}
	for _, rec := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.RepairOne(ctx, rec)
	}
	return nil
}

// RepairNow runs the same confirmed sequence against one named hull, ignoring the backoff
// and any escalation — an operator asking directly is the reason those exist. The money
// guard and the confirmation are NOT skipped, and a pass that writes still spends an
// attempt, so driving this in a loop cannot get around the bound.
func (s *Sweeper) RepairNow(ctx context.Context, playerID int, symbol string) (Result, error) {
	rec, found, err := s.ledger.Find(ctx, playerID, symbol)
	if err != nil {
		return Result{}, fmt.Errorf("read the repair episode for %s: %w", symbol, err)
	}
	if !found {
		now := s.now()
		rec = Record{PlayerID: playerID, ShipSymbol: symbol, FirstSeenAt: now, NextAttemptAt: now}
		if err := s.ledger.Observe(ctx, playerID, symbol, now); err != nil {
			return Result{}, fmt.Errorf("open a repair episode for %s: %w", symbol, err)
		}
	}
	rec.EscalatedAt = nil
	return s.RepairOne(ctx, rec), nil
}

// RepairOne runs and books one episode.
func (s *Sweeper) RepairOne(ctx context.Context, rec Record) Result {
	res := s.repairer.Repair(ctx, rec.PlayerID, rec.ShipSymbol)
	s.book(ctx, rec, res)
	return res
}

// book applies the outcome to the episode: closing it, spending an attempt, escalating, or
// merely rescheduling.
func (s *Sweeper) book(ctx context.Context, rec Record, res Result) {
	now := s.now()

	if res.Outcome.Resolved() {
		if err := s.ledger.Clear(ctx, rec.PlayerID, rec.ShipSymbol); err != nil {
			s.logf("WARNING [hull_repair_ledger] ship=%s: could not close the repair episode: %v", rec.ShipSymbol, err)
		}
		s.logf("INFO [hull_repair_%s] ship=%s: %s", res.Outcome, rec.ShipSymbol, res.Reason)
		return
	}

	if res.Outcome.SpentAttempt() {
		rec.Attempts++
	}
	rec.LastOutcome = string(res.Outcome)
	rec.LastReason = res.Reason
	rec.NextAttemptAt = now.Add(backoffFor(rec.Attempts))

	if reason, escalate := s.escalation(rec, res, now); escalate {
		rec.EscalatedAt = &now
		rec.LastReason = reason
		s.escalate(ctx, rec, reason)
		return
	}

	if err := s.ledger.Save(ctx, rec); err != nil {
		s.logf("WARNING [hull_repair_ledger] ship=%s: could not record the repair attempt: %v", rec.ShipSymbol, err)
	}
	s.logf("WARNING [hull_repair_deferred] ship=%s outcome=%s attempts=%d/%d retry_at=%s: %s",
		rec.ShipSymbol, res.Outcome, rec.Attempts, MaxAttempts, rec.NextAttemptAt.UTC().Format(time.RFC3339), res.Reason)
}

// escalation decides whether the episode has stopped being worth retrying, and says why.
func (s *Sweeper) escalation(rec Record, res Result, now time.Time) (string, bool) {
	switch {
	case res.Outcome.Terminal():
		return res.Reason, true
	case rec.Attempts >= MaxAttempts:
		return fmt.Sprintf("%d repair attempts have been spent without clearing the fault; the last said: %s", rec.Attempts, res.Reason), true
	case !rec.FirstSeenAt.IsZero() && now.Sub(rec.FirstSeenAt) >= EscalateAfter:
		return fmt.Sprintf("the hull has been unreadable since %s and the repair has never been able to run; the last attempt said: %s", rec.FirstSeenAt.UTC().Format(time.RFC3339), res.Reason), true
	}
	return "", false
}

// escalate stops the retries and makes the hull an operator's problem. The episode row
// stays: it is the durable record that this hull is present-but-unrepairable, and it
// clears the moment the hull reads again.
func (s *Sweeper) escalate(ctx context.Context, rec Record, reason string) {
	if err := s.ledger.Save(ctx, rec); err != nil {
		s.logf("WARNING [hull_repair_ledger] ship=%s: could not record the escalation: %v", rec.ShipSymbol, err)
	}
	if s.report != nil {
		s.report.Escalated(rec.ShipSymbol, rec.LastOutcome)
	}
	s.logf("ERROR [hull_repair_escalated] ship=%s outcome=%s attempts=%d: the automatic repair has given up and this hull needs an operator — %s",
		rec.ShipSymbol, rec.LastOutcome, rec.Attempts, reason)
}

// backoffFor is the wait before the next pass, doubling per spent attempt up to MaxBackoff.
func backoffFor(attempts int) time.Duration {
	wait := RetryBackoff
	for i := 0; i < attempts && wait < MaxBackoff; i++ {
		wait *= 2
	}
	if wait > MaxBackoff {
		return MaxBackoff
	}
	return wait
}
