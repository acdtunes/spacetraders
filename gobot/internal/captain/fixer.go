package watchkeeper

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
	return wireWorktreeBeads(dir, repoRoot)
}

// wireWorktreeBeads points a worktree's bd (beads) at the main checkout's
// database. A git worktree checks out the tracked .beads/ files (config.yaml,
// metadata.json, issues.jsonl) but NOT the gitignored Dolt database itself
// (embeddeddolt/), which lives only in the main checkout. Without this, bd run
// from the worktree resolves to an empty/phantom database and reports "no issue
// found" for beads that exist — a fix session stalled this way, unable to find
// sp-sk68. bd's redirect file makes the resolution explicit rather than relying
// on bd's implicit git-worktree heuristic (which is version-dependent and does
// not fire for a non-worktree checkout). An absolute target is used because the
// file is gitignored and regenerated per worktree, so it is never shared across
// clones. chmod 0700 matches bd's recommended permissions and silences its
// per-invocation permissions warning.
func wireWorktreeBeads(worktreeDir, repoRoot string) error {
	mainBeads := filepath.Join(repoRoot, ".beads")
	if info, err := os.Stat(mainBeads); err != nil || !info.IsDir() {
		return nil // repo does not use beads — nothing to wire
	}
	wtBeads := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(wtBeads, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wtBeads, "redirect"), []byte(mainBeads+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(wtBeads, 0o700)
}
