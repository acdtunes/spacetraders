package watchkeeper

import (
	"context"
	"fmt"
	"time"
)

// beadsClient is the driven port the orphan-requeue path uses; *BeadsClient
// satisfies it. Kept narrow so tests can substitute a recording fake.
type beadsClient interface {
	ListInProgressPipeline(ctx context.Context) ([]PipelineBead, error)
	Reopen(ctx context.Context, id, reason string) error
}

// Preflight probes the wake-delivery channel once at startup so an
// env-broken gateway (e.g. a launch environment missing BD_REAL, which makes
// every gc/bd call fail) is visible from the first log line, distinct from
// the generic per-tick errors it would otherwise hide behind (sp-sk68 D6). It
// never blocks startup: the channel may recover, and the supervisor must run
// regardless so the D1 consecutive-failure counter can track recovery. The
// underlying env/plist repair is an ops fix outside this binary.
func (s *Supervisor) Preflight(ctx context.Context) {
	if s.gw == nil {
		return
	}
	if _, err := s.gw.SessionAlive(ctx, s.cfg.CaptainAgent); err != nil {
		fmt.Printf("watchkeeper: WARNING wake-delivery channel unusable at startup (gc/bd): %v"+
			" — wakes and Admiral escalations will fail\n", err)
	}
}

// ensureCaptainAlive MONITORS the standing captain session and ALERTS the
// Admiral when it is found dead — it never CREATES a session. City policy is
// "no auto-spawn: agents are invoked manually via acd run / acd prime"; a human
// relaunches the captain on the alert (Admiral ruling sp-qv71). A probe error
// is treated conservatively as "not dead" — a flaky probe (e.g. during the
// gc/bd outage where every probe errored, sp-sk68 D5) must never be read as a
// death — but it is still logged rather than swallowed. A genuinely dead
// captain is alerted loudly and durably via alertSessionDown.
func (s *Supervisor) ensureCaptainAlive(ctx context.Context, now time.Time) {
	alive, err := s.gw.SessionAlive(ctx, s.cfg.CaptainAgent)
	if err != nil {
		fmt.Printf("watchkeeper: session-alive probe failed (skipping liveness check): %v\n", err)
		return
	}
	if alive {
		return
	}
	s.alertSessionDown(ctx, now, s.cfg.CaptainAgent, "captain session down",
		"The standing captain session is not alive. City policy is no auto-spawn: "+
			"relaunch it manually (acd run / acd prime "+s.cfg.CaptainAgent+").")
}

// alertSessionDown announces that a standing session (captain, surveyor) was
// found dead. City policy forbids auto-spawn — the watchkeeper monitors and
// alerts; a human relaunches on the alert (Admiral ruling sp-qv71). It ALWAYS
// emits a grep-able local log line FIRST, so the outage is visible in the
// supervisor log even when the Admiral mail cannot be delivered (e.g. during a
// gc/bd outage), and it logs a mail-delivery failure too rather than swallowing
// it (the old respawn.go `_ =` swallow was part of the bug). Per-agent throttled
// to one alert per sessionDownAlertInterval so a session that stays dead across
// 30s polls does not spam the Admiral. The throttle stamp advances on every
// alert regardless of mail success: the local log is the guaranteed-durable
// signal, so a flapping mail channel must not turn into per-poll log/mail spam;
// the alert simply retries after the window.
func (s *Supervisor) alertSessionDown(ctx context.Context, now time.Time, agent, subject, body string) {
	if s.sessionDownAlerted == nil {
		s.sessionDownAlerted = map[string]time.Time{}
	}
	if last, ok := s.sessionDownAlerted[agent]; ok && now.Sub(last) < sessionDownAlertInterval {
		return
	}
	s.sessionDownAlerted[agent] = now
	fmt.Printf("watchkeeper: STANDING SESSION DOWN %q — %s (city policy: no auto-spawn; relaunch manually)\n",
		agent, subject)
	if err := s.gw.SendMail(ctx, s.cfg.AdmiralAlias, subject, body); err != nil {
		fmt.Printf("watchkeeper: Admiral down-alert mail FAILED for %q: %v\n", agent, err)
	}
}

// requeueOrphanedPipelineBeads reopens shipwright bug/feature beads whose
// claiming session has died, carrying the legacy fixer's at-least-once orphan
// recovery onto the beads pipeline. Idempotent — cheap to run every tick.
func (s *Supervisor) requeueOrphanedPipelineBeads(ctx context.Context) {
	if s.bc == nil {
		return
	}
	beads, err := s.bc.ListInProgressPipeline(ctx)
	if err != nil {
		return
	}
	for _, b := range beads {
		alive, err := s.gw.SessionAlive(ctx, b.Assignee)
		if err != nil || alive {
			continue
		}
		_ = s.bc.Reopen(ctx, b.ID, "session died")
	}
}
