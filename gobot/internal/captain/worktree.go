package watchkeeper

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

// SquashMerge integrates branch into the main checkout as a single squash commit
// containing EXACTLY the branch's own diff — never files other agents have staged
// in the shared checkout, and never the beads hook's issues.jsonl export.
//
// The original ran `git merge --squash` then a pathspec-less `git commit` in the
// shared main checkout, which commits the whole INDEX. In the multi-agent city
// that index routinely holds peers' staged work, so the merge swept in foreign
// files (realized: 2026-07-03 data-loss; Frankenstein commit 71221b2, which swept
// AGENTS.md, CLAUDE.md, .claude/settings.json and the beads exports). Two defenses,
// smallest blast radius first:
//
//  1. Guard (assertNoForeignStaged): refuse loudly if the shared index holds any
//     foreign staged file, so the gate reports it instead of contaminating main.
//  2. Clean commit (squashMergeClean): build the squash commit in isolation from
//     the shared index and fast-forward main to it, so no staged file can ride
//     along and the beads pre-commit hook never fires.
func SquashMerge(repoDir, branch, message string) error {
	if err := assertNoForeignStaged(repoDir); err != nil {
		return err
	}
	return squashMergeClean(repoDir, branch, message)
}

// beadsIssuesExempt reports whether a staged path is a beads issues.jsonl export.
// The beads pre-commit hook (core.hooksPath=.beads/hooks) re-exports and stages it
// on every commit in the shared checkout, so aborting on it would brick every
// gated merge. Nested beads databases (e.g. city/.beads/issues.jsonl) are staged
// by the same hook and are exempt too. A stray worktree-root issues.jsonl is NOT
// exempt: it signals the export misfire and should surface, not pass silently.
func beadsIssuesExempt(path string) bool {
	return path == ".beads/issues.jsonl" || strings.HasSuffix(path, "/.beads/issues.jsonl")
}

// assertNoForeignStaged fails if the shared checkout's index holds any staged file
// other than the exempt beads export. A pathspec-less commit would sweep exactly
// these staged files into the merge commit, so the gate refuses and names them
// rather than contaminating main. Unstaged/untracked dirt is harmless to a
// pathspec-less commit (it commits the index, not the worktree) and never aborts.
func assertNoForeignStaged(repoDir string) error {
	out, err := gitRun(repoDir, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	var foreign []string
	for _, line := range strings.Split(out, "\n") {
		path := strings.TrimSpace(line)
		if path == "" || beadsIssuesExempt(path) {
			continue
		}
		foreign = append(foreign, path)
	}
	if len(foreign) > 0 {
		return fmt.Errorf("refusing squash-merge: shared checkout has foreign staged files a merge commit would sweep in: %s", strings.Join(foreign, ", "))
	}
	return nil
}

// squashMergeClean builds the squash commit from the branch's own tree and
// fast-forwards main to it, never reading the shared index or firing the beads
// pre-commit hook.
//
// Precondition: main is an ancestor of branch (enforced upstream by the staleness
// gate, and re-asserted here). When it holds, the squash-merge result tree is
// exactly the branch tree, so we name it directly via commit-tree instead of
// merging into the shared index. Committing the branch tree onto a main that has
// advanced past the branch would REVERT main's newer commits (observed
// 2026-07-03), so a stale branch is refused rather than merged.
//
// The final fast-forward moves the main ref and syncs the working tree WITHOUT a
// commit, so it can never sweep staged peer files (mirrors the proven manual
// recovery). Peers' staged and unstaged work is left untouched — the index is
// never reset.
func squashMergeClean(repoDir, branch, message string) error {
	if !BranchContainsMain(repoDir, branch) {
		return fmt.Errorf("refusing squash-merge: main is not an ancestor of %s (stale branch would revert main)", branch)
	}
	parent, err := gitRun(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	tree, err := gitRun(repoDir, "rev-parse", branch+"^{tree}")
	if err != nil {
		return err
	}
	commit, err := gitRun(repoDir, "commit-tree", strings.TrimSpace(tree), "-p", strings.TrimSpace(parent), "-m", message)
	if err != nil {
		return err
	}
	if _, err := gitRun(repoDir, "merge", "--ff-only", strings.TrimSpace(commit)); err != nil {
		return err
	}
	return nil
}

// BranchContainsMain reports whether the branch is based on the current main
// HEAD. A stale branch (main advanced after the worktree was cut) must never
// be squash-merged: its diff vs main REVERTS every commit main gained since,
// silently undoing shipped fixes (observed 2026-07-03).
func BranchContainsMain(repoDir, branch string) bool {
	_, err := gitRun(repoDir, "merge-base", "--is-ancestor", "main", branch)
	return err == nil
}
