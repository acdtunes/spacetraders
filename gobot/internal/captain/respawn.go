package captainsup

import "context"

// beadsClient is the driven port the orphan-requeue path uses; *BeadsClient
// satisfies it. Kept narrow so tests can substitute a recording fake.
type beadsClient interface {
	ListInProgressPipeline(ctx context.Context) ([]PipelineBead, error)
	Reopen(ctx context.Context, id, reason string) error
}

// ensureCaptainAlive respawns the standing captain session when it is found
// dead, and mails the Admiral only when the respawn itself also fails. Probe
// and spawn errors are swallowed so a flaky city never crashes the tick loop.
func (s *Supervisor) ensureCaptainAlive(ctx context.Context) {
	alive, err := s.gw.SessionAlive(ctx, s.cfg.CaptainAgent)
	if err != nil || alive {
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
