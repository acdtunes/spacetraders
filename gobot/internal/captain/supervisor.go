package captainsup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

const (
	eventBatchLimit    = 50
	usageLimitBackoff  = 20 * time.Minute
	captainLogMaxBytes = 96 * 1024
)

// Supervisor is pure plumbing: it decides WHEN a session runs, never WHAT
// the captain does (spec: Component 2).
type Supervisor struct {
	db     *gorm.DB
	store  captain.EventStore
	runner SessionRunner
	ws     Workspace
	cfg    config.CaptainConfig

	lastSession      time.Time
	lastCredits      int
	sessionStarts    []time.Time
	limitBackoffTill time.Time

	fixer *Fixer // optional; nil in phase 1-2 deployments
}

// SetFixer enables the self-improvement pipeline (plan 2 of 2).
func (s *Supervisor) SetFixer(f *Fixer) { s.fixer = f }

func NewSupervisor(db *gorm.DB, store captain.EventStore, runner SessionRunner, ws Workspace, cfg config.CaptainConfig) *Supervisor {
	return &Supervisor{db: db, store: store, runner: runner, ws: ws, cfg: cfg}
}

// Tick performs one supervisor iteration. Returns ran=true when a session was
// attempted (successfully or not).
func (s *Supervisor) Tick(ctx context.Context, now time.Time) (bool, error) {
	if s.ws.Disabled() {
		return false, nil
	}
	if now.Before(s.limitBackoffTill) {
		return false, nil // quota window exhausted; events queue durably
	}

	// Synthetic events (state-derived): stale heartbeats, idle ships, credit crossings.
	dcfg := DetectorConfig{
		PlayerID:          s.cfg.PlayerID,
		ShipIdle:          time.Duration(s.cfg.ShipIdleMinutes) * time.Minute,
		StaleHeartbeat:    time.Duration(s.cfg.StaleHeartbeatMinutes) * time.Minute,
		CreditsThresholds: s.cfg.CreditsThresholds,
		LastCredits:       s.lastCredits,
	}
	if err := RunDetectors(ctx, s.db, s.store, dcfg, now); err != nil {
		return false, fmt.Errorf("detectors: %w", err)
	}
	if credits, err := CurrentCredits(ctx, s.db, s.cfg.PlayerID); err == nil {
		s.lastCredits = credits
	}

	events, err := s.store.FindUnprocessed(ctx, s.cfg.PlayerID, eventBatchLimit)
	if err != nil {
		return false, err
	}
	heartbeatDue := now.Sub(s.lastSession) >= time.Duration(s.cfg.HeartbeatMinutes)*time.Minute
	if len(events) == 0 && !heartbeatDue {
		return s.tickSecondary(ctx, now)
	}
	if s.sessionsInLastHour(now) >= s.cfg.MaxSessionsPerHour {
		fmt.Printf("captain: session cap reached (%d/h), %d events queued\n",
			s.cfg.MaxSessionsPerHour, len(events))
		return false, nil
	}

	prompt, err := ComposeSnapshot(ctx, s.db, s.ws, s.cfg.PlayerID, events, now)
	if err != nil {
		return false, err
	}

	s.sessionStarts = append(s.sessionStarts, now)
	s.lastSession = now
	fmt.Printf("captain: starting session (%d events, heartbeatDue=%v)\n", len(events), heartbeatDue)
	if err := s.runner.Run(ctx, prompt); err != nil {
		// Events stay unprocessed → retried later. Usage limit is a normal
		// state: back off instead of hammering a closed window every tick.
		if errors.Is(err, ErrUsageLimit) {
			s.limitBackoffTill = now.Add(usageLimitBackoff)
			fmt.Printf("captain: usage limit hit, backing off until %s\n",
				s.limitBackoffTill.Format("15:04:05"))
		}
		return true, err
	}

	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if err := s.store.MarkProcessed(ctx, ids, now); err != nil {
		return true, fmt.Errorf("mark processed: %w", err)
	}
	// A successful session has addressed any Admiral message; clear the inbox.
	_ = os.Remove(s.ws.InboxPath())
	// Keep memory files bounded; overflow goes to grep-able archives.
	_ = s.ws.TrimLog("captain-log.md", captainLogMaxBytes)
	// Make the captain's memory durable: best-effort auto-commit of its
	// workspace after each successful session (it cannot commit itself).
	commitCaptainState(s.ws.Dir())
	fmt.Printf("captain: session complete, %d events processed\n", len(ids))

	if s.fixer != nil {
		if _, err := s.fixer.ProcessOne(ctx, now); err != nil {
			fmt.Printf("captain fixer: %v\n", err)
		}
	}
	return true, nil
}

// tickSecondary runs when no strategy session is needed: meta-review first,
// then one fixer step. Meta-review respects the same hourly session cap.
func (s *Supervisor) tickSecondary(ctx context.Context, now time.Time) (bool, error) {
	ran := false
	if MetaReviewDue(s.ws, now) && s.sessionsInLastHour(now) < s.cfg.MaxSessionsPerHour {
		prompt, err := ComposeMetaReview(ctx, s.db, s.ws, s.cfg.PlayerID, now)
		if err != nil {
			return false, err
		}
		s.sessionStarts = append(s.sessionStarts, now)
		fmt.Println("captain: starting meta-review session")
		if err := s.runner.Run(ctx, prompt); err != nil {
			return true, err // marker not written -> retried next day-window tick
		}
		if err := MarkMetaReviewDone(s.ws, now); err != nil {
			return true, err
		}
		// The review consumed the friction queue; clear it.
		_ = os.Remove(s.ws.StatePath("friction.md"))
		ran = true
	}
	if s.fixer != nil {
		acted, err := s.fixer.ProcessOne(ctx, now)
		if err != nil {
			fmt.Printf("captain fixer: %v\n", err)
		}
		ran = ran || acted
	}
	return ran, nil
}

// Run loops Tick on the poll interval until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) error {
	interval := time.Duration(s.cfg.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.Tick(ctx, time.Now()); err != nil {
				fmt.Printf("captain: tick error: %v\n", err)
			}
		}
	}
}

func (s *Supervisor) sessionsInLastHour(now time.Time) int {
	cutoff := now.Add(-time.Hour)
	kept := s.sessionStarts[:0]
	for _, t := range s.sessionStarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.sessionStarts = kept
	return len(kept)
}

// commitCaptainState commits the captain workspace (state, reports) quietly.
// Best-effort: git trouble must never affect the session loop.
func commitCaptainState(wsDir string) {
	add := exec.Command("git", "-C", wsDir, "add", "state", "reports", "inbox.md")
	if err := add.Run(); err != nil {
		return
	}
	commit := exec.Command("git", "-C", wsDir, "commit", "-q", "-m",
		"chore(captain): session state (auto)")
	_ = commit.Run() // exits nonzero when nothing to commit; fine
}
