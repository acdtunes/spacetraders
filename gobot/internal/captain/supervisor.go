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

	// Wake-delivery backoff (sp-sk68 D1). recordWake is the ONLY writer of
	// last_session, so when gw.SendMail/Nudge fails persistently the cadence
	// stays "due" forever and the old code retried on every 30s tick with no
	// throttle, no progress, and no distinct signal. These in-memory (never
	// persisted) fields throttle repeated failed deliveries on an exponential
	// backoff and make the outage grep-able. A brand-new interrupt-class event
	// always bypasses the backoff so interrupt delivery is never regressed.
	lastDeliveryAttempt   time.Time
	deliveryFailures      int
	firstDeliveryFailure  time.Time
	lastAttemptInterrupts map[int64]bool // interrupt event ids present at the last attempt
	// Credit-gate satisfaction snapshot at the last delivery attempt (sp-sk68
	// D4). A CreditsAbove/Below bound newly satisfied since the last attempt is
	// a genuine edge that bypasses the delivery backoff; a still-satisfied
	// (level) bound must not, or a standing threshold would defeat the backoff
	// every tick against a dead channel.
	lastAttemptCreditsAbove bool
	lastAttemptCreditsBelow bool

	// Live agent-credit source (sp-sk68 D3). When wired, the wake gate and the
	// credits-crossing detector evaluate the SAME live agent-API credits the
	// captain sees via `player info`, not a divergent ledger reconstruction.
	agentCredits agentCreditsAPI
	playerToken  string
	// liveCreditsObserved records whether a live agent-API read has ever
	// succeeded. Once it has, a transient API error retains the last live value
	// instead of flipping the gate back to the divergent ledger reconstruction
	// (sp-sk68 D3).
	liveCreditsObserved bool

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
	s.lastCredits = st.LastCredits
}

// saveState persists current scheduling (cadence) state. Best-effort: a
// persistence failure must not stop the supervisor from doing its actual
// job. This writes only the supervisor-owned cadence fields (via
// saveCadenceState's read-merge-write), preserving whatever wake policy the
// captain has separately declared via `spacetraders captain wake set` — a
// full overwrite here would clobber that policy every time a session runs.
func (s *Supervisor) saveState() {
	st := supervisorState{
		LastSession:       s.lastSession,
		LastSurveyorNudge: s.lastSurveyorNudge,
		Renudges:          s.renudges,
		Escalated:         s.escalated,
		LastCredits:       s.lastCredits,
	}
	if err := saveCadenceState(s.statePath, st); err != nil {
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

	// Refresh live credits BEFORE the detectors and the wake gate (sp-sk68 D3,
	// D4) so both evaluate the SAME number the captain sees via `player info`.
	// The previous value is kept as the crossing baseline for the detector.
	prevCredits := s.lastCredits
	s.refreshCredits(ctx)

	// Synthetic events (state-derived): stale heartbeats, idle ships, credit crossings.
	dcfg := DetectorConfig{
		PlayerID:            s.cfg.PlayerID,
		ShipIdle:            time.Duration(s.cfg.ShipIdleMinutes) * time.Minute,
		StaleHeartbeat:      time.Duration(s.cfg.StaleHeartbeatMinutes) * time.Minute,
		CreditsThresholds:   s.cfg.CreditsThresholds,
		LastCredits:         prevCredits,
		CurrentCreditsValue: s.lastCredits,
		IncomeStall:         time.Duration(s.cfg.IncomeStallHours) * time.Hour,
		StreamDown:          time.Duration(s.cfg.StreamDownMinutes) * time.Minute,
		ExpectedStreams:     s.cfg.ExpectedStreams,
	}
	// Synthetic events are best-effort enrichment: a detector/DB error must not
	// abort the tick and skip cadence/interrupt/credits wake evaluation
	// (sp-sk68 D4). Log and continue.
	if err := RunDetectors(ctx, s.db, s.store, dcfg, now); err != nil {
		fmt.Printf("captain: detectors error (continuing to wake evaluation): %v\n", err)
	}

	events, err := s.store.FindUnprocessed(ctx, s.cfg.PlayerID, eventBatchLimit)
	if err != nil {
		return false, err
	}

	// Re-read the captain-declared wake policy fresh every tick (not cached
	// at construction) so `spacetraders captain wake set` takes effect on
	// the very next poll without a restart.
	policy, err := LoadWakePolicy(s.statePath)
	if err != nil {
		fmt.Printf("captain: wake policy unreadable, using defaults: %v\n", err)
		policy = WakePolicy{}
	}
	decision := evaluateWakeGate(wakeGateInput{
		Now:                    now,
		Events:                 events,
		Policy:                 policy,
		Credits:                s.lastCredits,
		LastSession:            s.lastSession,
		HeartbeatMinutes:       s.cfg.HeartbeatMinutes,
		MaxWakeIntervalMinutes: s.cfg.MaxWakeIntervalMinutes,
	})
	if !decision.ShouldWake {
		return false, nil
	}
	// Throttle repeated FAILED deliveries (sp-sk68 D1): once a wake delivery
	// has failed, back off exponentially instead of hammering the dead channel
	// every tick. A brand-new interrupt-class event bypasses the backoff so
	// interrupt delivery is never regressed.
	if s.deliveryThrottled(now, events, policy) {
		return false, nil
	}
	if s.sessionsInLastHour(now) >= s.cfg.MaxSessionsPerHour {
		fmt.Printf("captain: session cap reached (%d/h), %d events queued\n",
			s.cfg.MaxSessionsPerHour, len(events))
		return false, nil
	}
	s.rememberAttempt(events, policy)
	ran, err := s.bridgeWake(ctx, now, events)
	if err != nil {
		s.noteDeliveryFailure(now, err)
	}
	return ran, err
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
