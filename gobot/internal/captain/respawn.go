package captainsup

import (
	"context"
	"fmt"
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
		fmt.Printf("captain: WARNING wake-delivery channel unusable at startup (gc/bd): %v"+
			" — wakes and Admiral escalations will fail\n", err)
	}
}

// ensureCaptainAlive respawns the standing captain session when it is found
// dead, and mails the Admiral only when the respawn itself also fails. Probe
// and spawn errors are swallowed so a flaky city never crashes the tick loop.
func (s *Supervisor) ensureCaptainAlive(ctx context.Context) {
	alive, err := s.gw.SessionAlive(ctx, s.cfg.CaptainAgent)
	if err != nil {
		// Conservative: a probe error is NOT treated as "dead" (respawning on
		// a flaky probe could double-spawn a live captain). But it must not be
		// silent either — during the gc/bd outage every probe errored and a
		// genuinely dead captain would have gone un-respawned with zero signal
		// (sp-sk68 D5). The out-of-band liveness fix is out of tier-1 scope.
		fmt.Printf("captain: session-alive probe failed (skipping respawn check): %v\n", err)
		return
	}
	if alive {
		return
	}
	if err := s.gw.SpawnSession(ctx, s.cfg.CaptainAgent, s.cfg.CaptainAgent); err != nil {
		_ = s.gw.SendMail(ctx, s.cfg.AdmiralAlias, "captain respawn failed", err.Error())
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
