package watchkeeper

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestWorktreeCreateModifyGateMerge(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// gitOut runs git and returns its trimmed output, failing the test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// branchWithFix cuts a fresh worktree branch off the repo HEAD and commits a
// single added file, returning the worktree so the caller can remove it.
func branchWithFix(t *testing.T, repo, branch string) Worktree {
	t.Helper()
	wt, err := CreateWorktree(repo, branch)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "fix.go"),
		[]byte("package main\n\n// Fixed is the fix.\nfunc Fixed() bool { return true }\n"), 0o644))
	runGit(t, wt.Dir, "add", "-A")
	runGit(t, wt.Dir, "commit", "-m", "fix")
	return wt
}

// (a) The guard refuses loudly, naming the offending file, when a peer has an
// unrelated file staged in the shared checkout — a pathspec-less commit would
// otherwise sweep it into the merge commit (realized: Frankenstein commit 71221b2).
func TestSquashMergeGuardAbortsOnForeignStaged(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt := branchWithFix(t, repo, "captain/fix-guard")
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("peer\n"), 0o644))
	runGit(t, repo, "add", "AGENTS.md")

	headBefore := gitOut(t, repo, "rev-parse", "HEAD")
	err := SquashMerge(repo, "captain/fix-guard", "fix: guarded")
	require.Error(t, err, "guard must refuse when a foreign file is staged")
	require.Contains(t, err.Error(), "AGENTS.md", "error must name the offending staged file")

	require.Equal(t, headBefore, gitOut(t, repo, "rev-parse", "HEAD"),
		"no merge may happen when the guard aborts")
	require.NotContains(t, gitOut(t, repo, "ls-tree", "-r", "--name-only", "HEAD"), "AGENTS.md",
		"the foreign file must never reach main")
}

// (b) The guard proceeds when only the beads issues.jsonl export is staged: the
// beads pre-commit hook stages it on every commit, so aborting on it would
// self-brick every gated merge.
func TestSquashMergeGuardProceedsOnBeadsIssuesJsonl(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".beads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte("v0\n"), 0o644))
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add beads")

	wt := branchWithFix(t, repo, "captain/fix-beads")
	defer func() { _ = wt.Remove(repo) }()

	// Simulate the beads hook re-exporting and staging issues.jsonl.
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte("v1-hook-churn\n"), 0o644))
	runGit(t, repo, "add", ".beads/issues.jsonl")

	require.NoError(t, SquashMerge(repo, "captain/fix-beads", "fix: beads-exempt"),
		"guard must proceed when only the beads export is staged")
	require.FileExists(t, filepath.Join(repo, "fix.go"))
}

// (d) A staged issues.jsonl churn does NOT ride along into the merge commit, and
// the staged churn is left undisturbed in the checkout.
func TestSquashMergeDoesNotCommitStagedBeadsIssues(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".beads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte("V0"), 0o644))
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add beads")

	wt := branchWithFix(t, repo, "captain/fix-noride")
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte("PEER-CHURN"), 0o644))
	runGit(t, repo, "add", ".beads/issues.jsonl")

	require.NoError(t, SquashMerge(repo, "captain/fix-noride", "fix: no ride"))

	require.Equal(t, "V0", gitOut(t, repo, "show", "HEAD:.beads/issues.jsonl"),
		"the merge commit must carry the branch's issues.jsonl, not the staged peer churn")
	require.Contains(t, gitOut(t, repo, "ls-tree", "-r", "--name-only", "HEAD"), "fix.go")
	require.Contains(t, gitOut(t, repo, "diff", "--cached", "--name-only"), ".beads/issues.jsonl",
		"the staged churn must remain staged/undisturbed")
}

// (c) The durable clean-commit mechanism: even with a foreign peer file staged in
// the shared checkout, the isolated squash commits ONLY the branch diff and leaves
// the peer's staged work undisturbed. This is defense-in-depth behind the guard.
func TestSquashMergeCleanLeavesForeignStagedUndisturbed(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt := branchWithFix(t, repo, "captain/fix-iso")
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, os.WriteFile(filepath.Join(repo, "peer.txt"), []byte("peer work\n"), 0o644))
	runGit(t, repo, "add", "peer.txt")

	require.NoError(t, squashMergeClean(repo, "captain/fix-iso", "fix: isolated"))

	tree := gitOut(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	require.Contains(t, tree, "fix.go", "branch diff must be in the commit")
	require.NotContains(t, tree, "peer.txt", "peer file must NOT be swept into the commit")

	require.Contains(t, gitOut(t, repo, "diff", "--cached", "--name-only"), "peer.txt",
		"peer's staged work must remain staged/undisturbed")
	require.FileExists(t, filepath.Join(repo, "peer.txt"))

	require.Equal(t, "2", gitOut(t, repo, "rev-list", "--count", "HEAD"),
		"a successful merge advances main to exactly one squash commit on the base")
}

// sp-k0di check 1: a worktree with an uncommitted source edit is dirty, and the
// detail names the offending file so the runner knows what to commit.
func TestWorktreeDirtyDetectsUncommittedSource(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/dirty-src")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	// Edit but never commit — exactly the sp-k0di failure: files, not commits.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "fix.go"),
		[]byte("package main\n\nfunc Fixed() bool { return true }\n"), 0o644))

	dirty, detail, err := WorktreeDirty(wt.Dir)
	require.NoError(t, err)
	require.True(t, dirty, "uncommitted source edit must read dirty")
	require.Contains(t, detail, "fix.go")
}

// A staged-but-uncommitted edit is also dirty: staging is not committing, and the
// squash merges commits.
func TestWorktreeDirtyDetectsStagedUncommitted(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/dirty-staged")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "fix.go"),
		[]byte("package main\n\nfunc Fixed() bool { return true }\n"), 0o644))
	runGit(t, wt.Dir, "add", "fix.go")

	dirty, detail, err := WorktreeDirty(wt.Dir)
	require.NoError(t, err)
	require.True(t, dirty, "staged-but-uncommitted edit must read dirty")
	require.Contains(t, detail, "fix.go")
}

// The beads issues.jsonl export is dirty-noise (the beads hook re-exports it every
// commit) and must NOT, on its own, make a worktree read dirty — else the pre-gate
// check would self-brick every merge in the shared city. Mirrors production: the
// export is a TRACKED file the hook modifies, so git reports it as ` M
// .beads/issues.jsonl` (a fully-untracked .beads/ never occurs — config.yaml and
// metadata.json are always committed).
func TestWorktreeDirtyIgnoresBeadsExport(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/dirty-beads")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, os.MkdirAll(filepath.Join(wt.Dir, ".beads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, ".beads", "issues.jsonl"), []byte("v0\n"), 0o644))
	runGit(t, wt.Dir, "add", ".beads/issues.jsonl")
	runGit(t, wt.Dir, "commit", "-m", "track beads export")
	// The beads hook re-exports the tracked file: a modification, not a new file.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, ".beads", "issues.jsonl"), []byte("v1-hook-churn\n"), 0o644))

	dirty, detail, err := WorktreeDirty(wt.Dir)
	require.NoError(t, err)
	require.False(t, dirty, "beads issues.jsonl export churn alone must not read dirty: %s", detail)
}

// A committed worktree is clean — the happy path the gate is meant to proceed on.
func TestWorktreeDirtyCleanWhenCommitted(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt := branchWithFix(t, repo, "captain/dirty-clean")
	defer func() { _ = wt.Remove(repo) }()

	dirty, detail, err := WorktreeDirty(wt.Dir)
	require.NoError(t, err)
	require.False(t, dirty, "committed worktree must read clean: %s", detail)
}

// sp-k0di check 2: a branch with zero commits ahead of main squashes main's own
// tree back onto main — a message-only commit. The guard refuses it as an empty
// merge and leaves main untouched, instead of reporting Merged=true (the exact bug
// that lost three fixes).
func TestSquashMergeRefusesBranchWithNoCommits(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/no-commits")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	headBefore := gitOut(t, repo, "rev-parse", "HEAD")
	err = SquashMerge(repo, "captain/no-commits", "fix: nothing committed")
	require.Error(t, err, "a branch with no commits ahead must be refused")
	require.True(t, errors.Is(err, errEmptyMerge), "must be an empty-merge refusal: %v", err)
	require.Contains(t, err.Error(), "no commits ahead")
	require.Equal(t, headBefore, gitOut(t, repo, "rev-parse", "HEAD"),
		"an empty merge must not advance main")
}

// sp-k0di check 3 (belt-and-suspenders): a branch WITH a commit ahead whose net
// tree still equals main's (an --allow-empty commit) squashes to a diff-empty
// result. The post-squash net rolls main back to pre-merge and reports the empty
// merge, so no message-only commit survives on main.
func TestSquashMergeRollsBackEmptyDiffMerge(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/empty-diff")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	// A real commit ahead of main (passes check 2) but with no file changes.
	runGit(t, wt.Dir, "commit", "--allow-empty", "-m", "empty commit ahead")

	headBefore := gitOut(t, repo, "rev-parse", "HEAD")
	err = SquashMerge(repo, "captain/empty-diff", "fix: net-empty branch")
	require.Error(t, err, "a net-empty squash must be refused")
	require.True(t, errors.Is(err, errEmptyMerge), "must be an empty-merge refusal: %v", err)
	require.Equal(t, headBefore, gitOut(t, repo, "rev-parse", "HEAD"),
		"the post-squash safety net must roll main back to pre-merge")
}
