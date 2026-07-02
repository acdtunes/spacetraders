package captainsup

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

const eventBatchLimit = 50

// Supervisor is pure plumbing: it decides WHEN a session runs, never WHAT
// the captain does (spec: Component 2).
type Supervisor struct {
	db     *gorm.DB
	store  captain.EventStore
	runner SessionRunner
	ws     Workspace
	cfg    config.CaptainConfig

	lastSession   time.Time
	lastCredits   int
	sessionStarts []time.Time
}

func NewSupervisor(db *gorm.DB, store captain.EventStore, runner SessionRunner, ws Workspace, cfg config.CaptainConfig) *Supervisor {
	return &Supervisor{db: db, store: store, runner: runner, ws: ws, cfg: cfg}
}

// Tick performs one supervisor iteration. Returns ran=true when a session was
// attempted (successfully or not).
func (s *Supervisor) Tick(ctx context.Context, now time.Time) (bool, error) {
	if s.ws.Disabled() {
		return false, nil
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
		return false, nil
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
		// Events stay unprocessed → retried next tick. Usage limit is normal.
		return true, err
	}

	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if err := s.store.MarkProcessed(ctx, ids, now); err != nil {
		return true, fmt.Errorf("mark processed: %w", err)
	}
	fmt.Printf("captain: session complete, %d events processed\n", len(ids))
	return true, nil
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
