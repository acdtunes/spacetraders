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
	db    *gorm.DB
	store captain.EventStore
	ws    Workspace
	cfg   config.CaptainConfig

	// statePath is where scheduling state (lastSession, lastSurveyorNudge,
	// renudges, escalated) is durably persisted so a process restart never
	// re-treats an already-armed cadence as immediately due.
	statePath string

	lastSession   time.Time
	lastCredits   int
	sessionStarts []time.Time

	// Bridge engine (engine_mode: bridge): city adapters + wake bookkeeping.
	gw        cityGateway
	bc        beadsClient
	renudges  map[int64]int  // event id → re-nudge count
	escalated map[int64]bool // event id → Admiral already alerted

	// Watchkeeper universe-reset detector (Tier-3 kill-switch rail).
	status            serverStatusSource
	eras              openEraSource
	lastUniverseCheck time.Time

	lastSurveyorNudge time.Time
}

func NewSupervisor(db *gorm.DB, store captain.EventStore, ws Workspace, cfg config.CaptainConfig) (*Supervisor, error) {
	if cfg.EngineMode != "bridge" {
		return nil, fmt.Errorf("captain: unsupported engine_mode %q (only \"bridge\" is supported)", cfg.EngineMode)
	}
	s := &Supervisor{db: db, store: store, ws: ws, cfg: cfg, statePath: ws.StatePath()}
	s.restoreState(time.Now())
	return s, nil
}

// restoreState loads durable scheduling state from disk. A missing or
// unreadable file degrades to a fresh start rather than blocking supervisor
// construction: any cadence still at its zero value is armed one full
// interval out from now, so a restart (or a first-ever run) never fires an
// immediate wake or survey nudge.
func (s *Supervisor) restoreState(now time.Time) {
	st, err := loadSupervisorState(s.statePath)
	if err != nil {
		fmt.Printf("captain: supervisor state unreadable, starting fresh: %v\n", err)
		st = supervisorState{}
	}
	if st.LastSession.IsZero() {
		st.LastSession = now
	}
	if st.LastSurveyorNudge.IsZero() {
		st.LastSurveyorNudge = now
	}
	s.lastSession = st.LastSession
	s.lastSurveyorNudge = st.LastSurveyorNudge
	s.renudges = st.Renudges
	s.escalated = st.Escalated
}

// saveState persists current scheduling state. Best-effort: a persistence
// failure must not stop the supervisor from doing its actual job.
func (s *Supervisor) saveState() {
	st := supervisorState{
		LastSession:       s.lastSession,
		LastSurveyorNudge: s.lastSurveyorNudge,
		Renudges:          s.renudges,
		Escalated:         s.escalated,
	}
	if err := saveSupervisorState(s.statePath, st); err != nil {
		fmt.Printf("captain: supervisor state persist failed: %v\n", err)
	}
}

// Tick performs one supervisor iteration. Returns ran=true when a session was
// attempted (successfully or not).
func (s *Supervisor) Tick(ctx context.Context, now time.Time) (bool, error) {
	if s.ws.Disabled() {
		return false, nil
	}
	if s.status != nil {
		s.checkUniverseReset(ctx, now)
		if s.ws.Disabled() {
			return false, nil
		}
	}
	if s.gw != nil {
		s.ensureCaptainAlive(ctx)
		s.nudgeSurveyorOnCadence(ctx, now)
		s.requeueOrphanedPipelineBeads(ctx)
	}

	// Synthetic events (state-derived): stale heartbeats, idle ships, credit crossings.
	dcfg := DetectorConfig{
		PlayerID:          s.cfg.PlayerID,
		ShipIdle:          time.Duration(s.cfg.ShipIdleMinutes) * time.Minute,
		StaleHeartbeat:    time.Duration(s.cfg.StaleHeartbeatMinutes) * time.Minute,
		CreditsThresholds: s.cfg.CreditsThresholds,
		LastCredits:       s.lastCredits,
		IncomeStall:       time.Duration(s.cfg.IncomeStallHours) * time.Hour,
		StreamDown:        time.Duration(s.cfg.StreamDownMinutes) * time.Minute,
		ExpectedStreams:   s.cfg.ExpectedStreams,
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
	return s.bridgeWake(ctx, now, events)
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
