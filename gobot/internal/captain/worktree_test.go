package captainsup

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func initScratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestWorktreeCreateModifyGateMerge(t *testing.T) {
	repo := initScratchRepo(t)

	wt, err := CreateWorktree(repo, "captain/fix-test")
	require.NoError(t, err)
	require.DirExists(t, wt.Dir)

	// Simulate a fix session editing code in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "fix.go"),
		[]byte("package main\n\n// Fixed is the fix.\nfunc Fixed() bool { return true }\n"), 0o644))
	runGit(t, wt.Dir, "add", "-A")
	runGit(t, wt.Dir, "commit", "-m", "fix")

	pass, output := RunGate(wt.Dir, time.Minute)
	require.True(t, pass, "gate output: %s", output)

	lines, err := DiffLines(repo, "captain/fix-test")
	require.NoError(t, err)
	require.Greater(t, lines, 0)

	require.NoError(t, SquashMerge(repo, "captain/fix-test", "fix: test"))
	require.FileExists(t, filepath.Join(repo, "fix.go"))

	require.NoError(t, wt.Remove(repo))
	require.NoDirExists(t, wt.Dir)
}

func TestRunGateFailsOnBrokenCode(t *testing.T) {
	repo := initScratchRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "broken.go"),
		[]byte("package main\n\nfunc broken() { undefinedCall() }\n"), 0o644))

	pass, output := RunGate(repo, time.Minute)
	require.False(t, pass)
	require.Contains(t, output, "undefinedCall")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func TestBranchContainsMain(t *testing.T) {
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/fix-fresh")
	require.NoError(t, err)
	require.True(t, BranchContainsMain(repo, "captain/fix-fresh"), "fresh branch contains main")

	// Advance main after the branch was cut: branch is now stale.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "newer.go"), []byte("package main\n"), 0o644))
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "main advances")
	require.False(t, BranchContainsMain(repo, "captain/fix-fresh"),
		"stale branch must be detected: squash-merging it would revert main's newer commits")
	require.NoError(t, wt.Remove(repo))
}
