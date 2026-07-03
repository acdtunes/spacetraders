package captainsup

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const worktreeRoot = ".captain-worktrees"

type Worktree struct {
	Dir    string
	Branch string
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func CreateWorktree(repoDir, branch string) (Worktree, error) {
	slug := strings.ReplaceAll(branch, "/", "-")
	dir := filepath.Join(repoDir, worktreeRoot, slug)
	if _, err := gitRun(repoDir, "worktree", "add", "-b", branch, dir, "HEAD"); err != nil {
		return Worktree{}, err
	}
	return Worktree{Dir: dir, Branch: branch}, nil
}

func (w Worktree) Remove(repoDir string) error {
	if _, err := gitRun(repoDir, "worktree", "remove", "--force", w.Dir); err != nil {
		return err
	}
	_, err := gitRun(repoDir, "branch", "-D", w.Branch)
	return err
}

// RunGate builds and tests the tree. The supervisor runs this itself; the fix
// session's claims are never trusted (spec: Self-improvement §3).
func RunGate(dir string, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)
	var combined strings.Builder
	for _, args := range [][]string{{"build", "./..."}, {"test", "./..."}} {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, combined.String() + "\ngate timeout"
		}
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		done := make(chan struct{})
		var out []byte
		var err error
		go func() { out, err = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(remaining):
			_ = cmd.Process.Kill()
			return false, combined.String() + "\ngate timeout during go " + args[0]
		}
		combined.WriteString(fmt.Sprintf("$ go %s\n%s\n", strings.Join(args, " "), out))
		if err != nil {
			return false, combined.String()
		}
	}
	return true, combined.String()
}

var shortstatRe = regexp.MustCompile(`(\d+) insertion|(\d+) deletion`)

// DiffLines counts changed lines on branch relative to current HEAD.
func DiffLines(repoDir, branch string) (int, error) {
	out, err := gitRun(repoDir, "diff", "--shortstat", "HEAD.."+branch)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, m := range shortstatRe.FindAllStringSubmatch(out, -1) {
		for _, g := range m[1:] {
			if g != "" {
				n, _ := strconv.Atoi(g)
				total += n
			}
		}
	}
	return total, nil
}

func SquashMerge(repoDir, branch, message string) error {
	if _, err := gitRun(repoDir, "merge", "--squash", branch); err != nil {
		_, _ = gitRun(repoDir, "merge", "--abort")
		return err
	}
	_, err := gitRun(repoDir, "commit", "-m", message)
	return err
}

// BranchContainsMain reports whether the branch is based on the current main
// HEAD. A stale branch (main advanced after the worktree was cut) must never
// be squash-merged: its diff vs main REVERTS every commit main gained since,
// silently undoing shipped fixes (observed 2026-07-03).
func BranchContainsMain(repoDir, branch string) bool {
	_, err := gitRun(repoDir, "merge-base", "--is-ancestor", "main", branch)
	return err == nil
}
