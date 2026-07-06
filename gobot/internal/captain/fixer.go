package captainsup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GateResult is the outcome of gating and (optionally) merging a worktree.
type GateResult struct {
	GatePassed bool
	Stale      bool
	Merged     bool
	Log        string
}

// GateAndMerge runs the gate in worktreeDir, refuses to merge a failing or
// stale-based branch, and squash-merges into repoDir only when merge is
// requested and the branch is clean. It wraps the existing gate exactly.
func GateAndMerge(repoDir, worktreeDir, branch, commitMsg string, timeout time.Duration, merge bool) (GateResult, error) {
	var mergeErr error
	result := gateAndMergeWith(
		RunGate,
		func(b string) (bool, error) { return !BranchContainsMain(repoDir, b), nil },
		func(rd, b, m string) error {
			if err := SquashMerge(rd, b, m); err != nil {
				mergeErr = err
				return err
			}
			return nil
		},
		repoDir, worktreeDir, branch, commitMsg, timeout, merge)
	return result, mergeErr
}

// gateAndMergeWith holds the gate -> stale -> merge decision chain over
// injected functions so the sequence is testable without shelling out.
func gateAndMergeWith(
	runGate func(string, time.Duration) (bool, string),
	isStale func(string) (bool, error),
	squashMerge func(string, string, string) error,
	repoDir, worktreeDir, branch, commitMsg string,
	timeout time.Duration, merge bool,
) GateResult {
	moduleDir := gateDir(worktreeDir)
	pass, log := runGate(moduleDir, timeout)
	result := GateResult{GatePassed: pass, Log: log}
	if !pass {
		return result
	}
	stale, err := isStale(branch)
	if err != nil || stale {
		result.Stale = true
		return result
	}
	if !merge {
		return result
	}
	if err := squashMerge(repoDir, branch, commitMsg); err != nil {
		return result
	}
	result.Merged = true
	return result
}

// gateDir returns the Go module directory inside a worktree. In the
// spacetraders monorepo the git root holds gobot/ (where go.mod lives);
// running the gate at the worktree root matches no packages.
func gateDir(worktreeRoot string) string {
	if _, err := os.Stat(filepath.Join(worktreeRoot, "gobot", "go.mod")); err == nil {
		return filepath.Join(worktreeRoot, "gobot")
	}
	return worktreeRoot
}

func ProvisionWorktree(dir string) error {
	common, err := gitRun(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	commonDir := strings.TrimSpace(common)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	repoRoot := filepath.Dir(commonDir)
	for _, sub := range []string{"gobot/pkg/proto/daemon", "gobot/pkg/proto/routing"} {
		src := filepath.Join(repoRoot, sub)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := exec.Command("cp", "-r", src, filepath.Join(dir, filepath.Dir(sub))+"/").Run(); err != nil {
			return err
		}
	}
	return nil
}
