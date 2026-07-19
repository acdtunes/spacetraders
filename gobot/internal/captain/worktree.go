package watchkeeper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// errEmptyMerge marks a squash-merge refusal because the merge would (or did)
// change nothing on main: a branch with no commits ahead, or one whose net tree
// equals main's. gateAndMergeWith surfaces it as GateResult.EmptyMerge — left
// unrefused, such a merge would report Merged=true while silently losing the
// agent's (uncommitted) fix.
var errEmptyMerge = errors.New("empty merge")

const worktreeRoot = ".captain-worktrees"

type Worktree struct {
	Dir    string
	Branch string
}

func gitRun(dir string, args ...string) (string, error) {
	return gitRunEnv(dir, nil, args...)
}

// gitRunEnv runs git with extra environment variables appended to the process
// environment — used to point plumbing at a private GIT_INDEX_FILE so tree
// construction never reads or writes the shared checkout's real index.
func gitRunEnv(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
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

// WorktreeDirty reports whether the worktree holds uncommitted (staged or
// unstaged) or untracked source changes that a squash-merge would silently drop.
// The gate merges COMMITS, not working-tree files: an agent that edits files but
// never commits leaves a branch with zero commits ahead of main, which
// squash-merges to a message-only commit (sp-k0di). Refusing a dirty worktree
// PRE-gate forces the runner to commit first. The returned detail lists the
// offending porcelain lines for a loud, actionable error.
//
// Beads issue exports (issues.jsonl anywhere) are excluded: the beads pre-commit
// hook re-exports and re-stages them on every commit in the shared city, so they
// are perpetual noise, not the agent's fix. Gitignored provisioning artifacts
// (regenerated proto, .beads/redirect) never appear in --porcelain, so they need
// no exemption.
func WorktreeDirty(worktreeDir string) (bool, string, error) {
	out, err := gitRun(worktreeDir, "status", "--porcelain")
	if err != nil {
		return false, "", err
	}
	var dirty []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if beadsExportNoise(worktreeStatusPath(line)) {
			continue
		}
		dirty = append(dirty, strings.TrimSpace(line))
	}
	if len(dirty) > 0 {
		return true, strings.Join(dirty, "\n"), nil
	}
	return false, "", nil
}

// worktreeStatusPath extracts the (possibly renamed-to) path from a
// `git status --porcelain` v1 line: the first two columns are status codes, then
// a space, then the path — or `ORIG -> NEW` for a rename/copy, where NEW is what
// lands on disk. Quoted paths (those with special chars) are left quoted, which
// only makes them fail the beads-noise exemption and be reported — the safe
// direction for a dirty check.
func worktreeStatusPath(porcelainLine string) string {
	if len(porcelainLine) < 4 {
		return strings.TrimSpace(porcelainLine)
	}
	p := strings.TrimSpace(porcelainLine[3:])
	if i := strings.Index(p, " -> "); i >= 0 {
		p = p[i+len(" -> "):]
	}
	return p
}

// beadsExportNoise reports whether a path is a beads issues.jsonl export — the
// repo root's, .beads/, or a nested city .beads/. The beads pre-commit hook
// re-exports and re-stages these on every shared-checkout commit, so they are
// perpetual noise, never an agent's fix. Every gate stage treats them as such: the
// pre-gate dirty check ignores them (WorktreeDirty), the foreign-staged guard
// exempts them (assertNoForeignStaged), and the squash pins them to main's version
// so none can ride into a merge (branchTreePinnedToMain).
func beadsExportNoise(path string) bool {
	return path == "issues.jsonl" || strings.HasSuffix(path, "/issues.jsonl")
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
// A naive `git merge --squash` + pathspec-less `git commit` in the shared main
// checkout commits the whole INDEX, which in the multi-agent city routinely
// holds peers' staged work and would sweep their files into the merge. Two
// defenses, smallest blast radius first:
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

// assertNoForeignStaged fails LOUDLY if the shared checkout's index holds a staged
// file that is neither the branch's own work nor beads-export noise — a signal that
// something is wrong in the shared checkout (a peer's half-staged edit). It is an
// early, actionable abort: squashMergeClean can no longer sweep the index (it builds
// the commit from the branch tree in a private index), so this guard exists purely to
// surface genuinely foreign staged files rather than merge silently around them.
//
// Beads issues.jsonl exports (root, .beads/, or nested — anything beadsExportNoise
// matches) are EXEMPT: the beads pre-commit hook re-exports and stages them on every
// commit in the shared city, so aborting on them would self-brick every gated merge.
// A stray ROOT issues.jsonl is exempt too — a racing beads auto-committer legitimately
// stages it, and squashMergeClean pins every export path to main's version so none
// can ride in regardless.
func assertNoForeignStaged(repoDir string) error {
	out, err := gitRun(repoDir, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	var foreign []string
	for _, line := range strings.Split(out, "\n") {
		path := strings.TrimSpace(line)
		if path == "" || beadsExportNoise(path) {
			continue
		}
		foreign = append(foreign, path)
	}
	if len(foreign) > 0 {
		return fmt.Errorf("refusing squash-merge: shared checkout has foreign staged files a merge commit would sweep in: %s", strings.Join(foreign, ", "))
	}
	return nil
}

// squashMergeClean builds the squash commit from the branch's own tree (with beads
// exports pinned to main — see branchTreePinnedToMain) and fast-forwards main to it,
// never reading the shared index or firing the beads pre-commit hook.
//
// Precondition: main is an ancestor of branch (enforced upstream by the staleness
// gate, and re-asserted here). When it holds, the squash-merge result tree is the
// branch tree (minus beads-export churn), so we name it directly via commit-tree
// instead of merging into the shared index. Committing the branch tree onto a main
// that has advanced past the branch would REVERT main's newer commits (observed
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
	parent = strings.TrimSpace(parent)

	// PRE-MERGE (sp-k0di check 2): the branch must add at least one commit on top
	// of main. A branch with zero commits ahead squashes main's OWN tree back onto
	// main — a message-only commit with no file changes. Fail loudly before
	// building the commit.
	ahead, err := gitRun(repoDir, "rev-list", "--count", parent+".."+branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ahead) == "0" {
		return fmt.Errorf("%w: branch %s has no commits ahead of main — nothing to merge (commit your worktree changes first)", errEmptyMerge, branch)
	}

	// Build the squash tree from the BRANCH's tree alone, then pin every beads-export
	// path (issues.jsonl anywhere) to main's version. In the shared city the beads
	// pre-commit hook re-exports and stages issues.jsonl INTO the worktree fix commit,
	// so branch^{tree} ITSELF carries a churn the agent never intended (sp-jgtw).
	// Pinning to the parent makes the squash change issues.jsonl by exactly nothing,
	// so no stray — from the branch tree OR a racing auto-committer — can ride in. The
	// tree is assembled in a PRIVATE index (GIT_INDEX_FILE) so the shared checkout's
	// index and working tree are never read or written.
	tree, err := branchTreePinnedToMain(repoDir, branch, parent)
	if err != nil {
		return err
	}
	commit, err := gitRun(repoDir, "commit-tree", tree, "-p", parent, "-m", message)
	if err != nil {
		return err
	}
	commit = strings.TrimSpace(commit)
	if _, err := gitRun(repoDir, "merge", "--ff-only", commit); err != nil {
		return err
	}

	// POST-SQUASH SAFETY NET (sp-k0di check 3): belt-and-suspenders behind check 2.
	// Even with commits ahead, a branch whose net tree equals main's (an
	// --allow-empty commit, or an add+revert) squashes to an empty diff. Verify the
	// merged commit actually changed main; if not, roll the main ref back to
	// pre-merge and fail. `reset --soft` moves only the ref (not the index or
	// working tree), so peers' unstaged work in the shared checkout is untouched —
	// safe because the empty ff changed no files by definition.
	newTree, err := gitRun(repoDir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	parentTree, err := gitRun(repoDir, "rev-parse", parent+"^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(newTree) == strings.TrimSpace(parentTree) {
		if _, rbErr := gitRun(repoDir, "reset", "--soft", parent); rbErr != nil {
			return fmt.Errorf("%w: squash of %s changed nothing vs main AND rollback failed: %v", errEmptyMerge, branch, rbErr)
		}
		return fmt.Errorf("%w: squash of %s produced no change vs main — rolled main back to %s", errEmptyMerge, branch, parent)
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

// treeBlob is a single tree entry's mode and blob object, enough to restore a path
// to a known version via `git update-index --cacheinfo`.
type treeBlob struct{ mode, sha string }

// branchTreePinnedToMain writes a tree equal to branch^{tree} EXCEPT that every
// beads-export path (issues.jsonl, anywhere) is forced to main's (parent's) version:
// present in parent -> parent's blob, absent from parent -> removed. This strips the
// beads pre-commit hook's issues.jsonl churn that rides inside branch^{tree} in the
// shared city, so the resulting squash commit's diff is exactly the branch's real
// change and nothing else.
//
// The tree is assembled in a PRIVATE index file (GIT_INDEX_FILE) so the shared
// checkout's own index and working tree are never read or written: a concurrent beads
// auto-committer staging issues.jsonl in the checkout cannot contribute, and the
// caller's staged/unstaged work is left undisturbed. Returns the written tree's SHA.
func branchTreePinnedToMain(repoDir, branch, parent string) (string, error) {
	branchTree, err := gitRun(repoDir, "rev-parse", branch+"^{tree}")
	if err != nil {
		return "", err
	}
	branchTree = strings.TrimSpace(branchTree)

	idxFile, err := os.CreateTemp("", "captain-squash-index-*")
	if err != nil {
		return "", err
	}
	idxPath := idxFile.Name()
	_ = idxFile.Close()
	_ = os.Remove(idxPath) // let git create a fresh index at this path
	defer func() { _ = os.Remove(idxPath) }()
	env := []string{"GIT_INDEX_FILE=" + idxPath}

	if _, err := gitRunEnv(repoDir, env, "read-tree", branchTree); err != nil {
		return "", err
	}

	parentBeads, err := beadsExportBlobs(repoDir, parent+"^{tree}")
	if err != nil {
		return "", err
	}
	branchBeads, err := beadsExportBlobs(repoDir, branchTree)
	if err != nil {
		return "", err
	}
	// Normalize every beads-export path seen in EITHER tree to main's version, so the
	// pinned tree agrees with main on all of them regardless of what the branch did.
	seen := map[string]struct{}{}
	for path := range parentBeads {
		seen[path] = struct{}{}
	}
	for path := range branchBeads {
		seen[path] = struct{}{}
	}
	for path := range seen {
		if pb, ok := parentBeads[path]; ok {
			if _, err := gitRunEnv(repoDir, env, "update-index", "--add", "--cacheinfo", pb.mode+","+pb.sha+","+path); err != nil {
				return "", err
			}
		} else if _, err := gitRunEnv(repoDir, env, "update-index", "--force-remove", path); err != nil {
			return "", err
		}
	}

	tree, err := gitRunEnv(repoDir, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

// beadsExportBlobs lists the beads-export paths (issues.jsonl anywhere,
// per beadsExportNoise) in a tree, mapped to their mode+blob so the squash can pin
// each to a known version.
func beadsExportBlobs(repoDir, tree string) (map[string]treeBlob, error) {
	out, err := gitRun(repoDir, "ls-tree", "-r", tree)
	if err != nil {
		return nil, err
	}
	blobs := map[string]treeBlob{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok || !beadsExportNoise(path) {
			continue
		}
		fields := strings.Fields(meta) // <mode> <type> <sha>
		if len(fields) < 3 {
			continue
		}
		blobs[path] = treeBlob{mode: fields[0], sha: fields[2]}
	}
	return blobs, nil
}
