package captainsup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type RunnerFactory func(workDir string) SessionRunner

// Fixer drives the gated self-improvement pipeline (spec: Self-improvement).
type Fixer struct {
	ws      Workspace
	factory RunnerFactory
	cfg     config.CaptainConfig

	fixStarts []time.Time
}

func NewFixer(ws Workspace, factory RunnerFactory, cfg config.CaptainConfig) *Fixer {
	return &Fixer{ws: ws, factory: factory, cfg: cfg}
}

func (f *Fixer) fixesDisabled() bool {
	_, err := os.Stat(filepath.Join(f.ws.Dir(), "DISABLED_FIXES"))
	return err == nil
}

func (f *Fixer) startsInLastDay(now time.Time) int {
	cutoff := now.Add(-24 * time.Hour)
	kept := f.fixStarts[:0]
	for _, t := range f.fixStarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	f.fixStarts = kept
	return len(kept)
}

// ProcessOne handles at most one `status: new` report per call.
func (f *Fixer) ProcessOne(ctx context.Context, now time.Time) (bool, error) {
	if f.ws.Disabled() || f.fixesDisabled() {
		return false, nil
	}
	reports, err := ScanReports(filepath.Join(f.ws.Dir(), "reports", "bugs"))
	if err != nil {
		return false, err
	}
	var target *BugReport
	for i := range reports {
		if reports[i].Status == "new" {
			target = &reports[i]
			break
		}
	}
	if target == nil {
		return false, nil
	}

	budget := f.cfg.MaxFixesPerDay
	if target.Kind == "feature" {
		budget = f.cfg.MaxFeaturesPerDay
	}
	if f.startsInLastDay(now) >= budget {
		fmt.Printf("captain fixer: daily budget reached, %s stays queued\n", target.Slug)
		return false, nil
	}

	f.fixStarts = append(f.fixStarts, now)
	if err := SetReportStatus(target.Path, "in_progress"); err != nil {
		return true, err
	}

	prefix := "fix"
	if target.Kind == "feature" {
		prefix = "feat"
	}
	branch := fmt.Sprintf("captain/%s-%s", prefix, target.Slug)
	wt, err := CreateWorktree(f.cfg.RepoDir, branch)
	if err != nil {
		_ = SetReportStatus(target.Path, "new") // retryable
		return true, err
	}

	body, _ := os.ReadFile(target.Path)
	runner := f.factory(wt.Dir)
	timeout := time.Duration(f.cfg.FixSessionTimeoutMinutes) * time.Minute
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runner.Run(sctx, FixPrompt(*target, string(body))); err != nil {
		_ = SetReportStatus(target.Path, "gate_failed")
		fmt.Printf("captain fixer: session failed for %s: %v (branch %s kept)\n", target.Slug, err, branch)
		return true, nil
	}

	pass, gateOut := RunGate(wt.Dir, timeout)
	if !pass {
		_ = SetReportStatus(target.Path, "gate_failed")
		_ = os.WriteFile(target.Path+".gate.log", []byte(gateOut), 0o644)
		_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt) // remove worktree dir, KEEP branch
		fmt.Printf("captain fixer: gate FAILED for %s, branch %s left for human\n", target.Slug, branch)
		return true, nil
	}

	if target.Kind == "feature" {
		lines, err := DiffLines(f.cfg.RepoDir, branch)
		if err == nil && lines > f.cfg.MaxFeatureDiffLines {
			_ = SetReportStatus(target.Path, "awaiting_human")
			_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt)
			fmt.Printf("captain fixer: %s diff too large (%d > %d lines), left for human\n",
				target.Slug, lines, f.cfg.MaxFeatureDiffLines)
			return true, nil
		}
	}

	if !f.cfg.AutoMerge {
		_ = SetReportStatus(target.Path, "awaiting_human")
		_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt)
		fmt.Printf("captain fixer: gate PASSED for %s; propose-only mode, branch %s awaits review\n",
			target.Slug, branch)
		return true, nil
	}

	msg := fmt.Sprintf("%s(captain): %s\n\nAutomated by the captain fix pipeline. Report: %s",
		prefix, target.Title, filepath.Base(target.Path))
	if err := SquashMerge(f.cfg.RepoDir, branch, msg); err != nil {
		_ = SetReportStatus(target.Path, "awaiting_human")
		return true, fmt.Errorf("squash-merge %s: %w", branch, err)
	}
	_ = wt.Remove(f.cfg.RepoDir)
	_ = SetReportStatus(target.Path, "merged")

	fmt.Printf("captain fixer: %s merged; restarting daemon (%s)\n", target.Slug, f.cfg.RestartCmd)
	restart := exec.CommandContext(ctx, "sh", "-c", f.cfg.RestartCmd)
	restart.Dir = f.cfg.RepoDir
	if out, err := restart.CombinedOutput(); err != nil {
		return true, fmt.Errorf("restart failed: %w: %s", err, out)
	}
	return true, nil
}

// gitCleanWorktreeOnly removes the worktree directory but preserves the branch
// so a human can inspect the attempt.
func gitCleanWorktreeOnly(repoDir string, wt Worktree) error {
	_, err := gitRun(repoDir, "worktree", "remove", "--force", wt.Dir)
	return err
}

// FixPrompt is the dedicated fix-session prompt (spec: Self-improvement §2).
func FixPrompt(r BugReport, body string) string {
	return fmt.Sprintf(`You are an automated repair session for the SpaceTraders gobot, working in an
isolated git worktree on branch captain/fix-%s. Fix ONLY the bug described
below.

Rules (non-negotiable):
- TDD: write a failing test that reproduces the bug FIRST, then the minimal fix.
- Minimal diff. No refactoring, no drive-by changes, no new dependencies.
- Never create or edit files under migrations/. Never touch files outside this
  worktree.
- Commit your work with git when done (conventional commit message).
- The supervisor will run 'go build ./... && go test ./...' itself; if that
  fails, your branch is discarded to a human. Run the tests yourself before
  finishing.

## Bug report
%s`, r.Slug, body)
}
